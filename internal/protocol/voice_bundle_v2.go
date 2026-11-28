package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	VoiceBundleV2Version    = 2
	MaxVoiceBundleV2Frames  = 3
	VoiceBundleV2HeaderSize = 14
	MaxVoiceFrameSize       = MaxPayloadSize - VoiceBundleV2HeaderSize - voiceBundleFrameSize
	MaxVoiceBundleSpan      = 64
	voiceBundleFrameSize    = 6
)

var ErrInvalidVoiceBundle = errors.New("invalid voice bundle")

type VoiceBundleFrame struct {
	Sequence uint32
	Payload  []byte
}

// VoiceBundleV2 separates transport attempts from the source's audio timeline.
// DatagramSequence may wrap through zero; epochs may not be zero or reused.
// ReceiverEpoch is zero upstream and server-assigned downstream.
type VoiceBundleV2 struct {
	DatagramSequence uint32
	SourceEpoch      uint32
	ReceiverEpoch    uint32
	Frames           []VoiceBundleFrame
}

func VoiceBundleV2Size(frames []VoiceBundleFrame) int {
	size := VoiceBundleV2HeaderSize
	for _, frame := range frames {
		size += voiceBundleFrameSize + len(frame.Payload)
	}
	return size
}

func validateVoiceBundleV2(bundle VoiceBundleV2) error {
	if bundle.SourceEpoch == 0 || len(bundle.Frames) == 0 || len(bundle.Frames) > MaxVoiceBundleV2Frames {
		return fmt.Errorf("%w: invalid v2 epoch or frame count", ErrInvalidVoiceBundle)
	}
	if VoiceBundleV2Size(bundle.Frames) > MaxPayloadSize {
		return fmt.Errorf("%w: v2 bundle exceeds %d bytes", ErrPayloadTooLarge, MaxPayloadSize)
	}
	for i, frame := range bundle.Frames {
		if len(frame.Payload) == 0 || len(frame.Payload) > MaxVoiceFrameSize {
			return fmt.Errorf("%w: v2 frame size %d", ErrInvalidVoiceBundle, len(frame.Payload))
		}
		if i > 0 && (int32(frame.Sequence-bundle.Frames[i-1].Sequence) <= 0 || frame.Sequence-bundle.Frames[0].Sequence > MaxVoiceBundleSpan) {
			return fmt.Errorf("%w: v2 frame order or span", ErrInvalidVoiceBundle)
		}
	}
	return nil
}

func EncodeVoiceBundleV2(bundle VoiceBundleV2) ([]byte, error) {
	if err := validateVoiceBundleV2(bundle); err != nil {
		return nil, err
	}
	payload := make([]byte, 0, VoiceBundleV2Size(bundle.Frames))
	payload = append(payload, VoiceBundleV2Version, byte(len(bundle.Frames)))
	payload = binary.BigEndian.AppendUint32(payload, bundle.DatagramSequence)
	payload = binary.BigEndian.AppendUint32(payload, bundle.SourceEpoch)
	payload = binary.BigEndian.AppendUint32(payload, bundle.ReceiverEpoch)
	for _, frame := range bundle.Frames {
		payload = binary.BigEndian.AppendUint32(payload, frame.Sequence)
		payload = binary.BigEndian.AppendUint16(payload, uint16(len(frame.Payload)))
		payload = append(payload, frame.Payload...)
	}
	return payload, nil
}

// DecodeVoiceBundleV2 validates the complete payload before returning any frame.
// Frame payloads borrow the input buffer.
func DecodeVoiceBundleV2(payload []byte) (VoiceBundleV2, error) {
	if len(payload) < VoiceBundleV2HeaderSize || len(payload) > MaxPayloadSize || payload[0] != VoiceBundleV2Version {
		return VoiceBundleV2{}, fmt.Errorf("%w: invalid v2 header", ErrInvalidVoiceBundle)
	}
	count := int(payload[1])
	if count == 0 || count > MaxVoiceBundleV2Frames {
		return VoiceBundleV2{}, fmt.Errorf("%w: v2 frame count %d", ErrInvalidVoiceBundle, count)
	}
	bundle := VoiceBundleV2{
		DatagramSequence: binary.BigEndian.Uint32(payload[2:6]),
		SourceEpoch:      binary.BigEndian.Uint32(payload[6:10]),
		ReceiverEpoch:    binary.BigEndian.Uint32(payload[10:14]),
		Frames:           make([]VoiceBundleFrame, 0, count),
	}
	rest := payload[VoiceBundleV2HeaderSize:]
	for i := 0; i < count; i++ {
		if len(rest) < voiceBundleFrameSize {
			return VoiceBundleV2{}, fmt.Errorf("%w: truncated v2 frame", ErrInvalidVoiceBundle)
		}
		sequence := binary.BigEndian.Uint32(rest[:4])
		length := int(binary.BigEndian.Uint16(rest[4:6]))
		rest = rest[voiceBundleFrameSize:]
		if length == 0 || length > MaxVoiceFrameSize || length > len(rest) {
			return VoiceBundleV2{}, fmt.Errorf("%w: v2 frame length %d", ErrInvalidVoiceBundle, length)
		}
		bundle.Frames = append(bundle.Frames, VoiceBundleFrame{Sequence: sequence, Payload: rest[:length]})
		rest = rest[length:]
	}
	if len(rest) != 0 {
		return VoiceBundleV2{}, fmt.Errorf("%w: v2 trailing bytes", ErrInvalidVoiceBundle)
	}
	if err := validateVoiceBundleV2(bundle); err != nil {
		return VoiceBundleV2{}, err
	}
	return bundle, nil
}

// DecodeVoiceBundleV2Packet also validates fields outside the bundle payload.
// upstream selects the direction, not whether redundancy is enabled.
func DecodeVoiceBundleV2Packet(packet VoicePacket, upstream bool) (VoiceBundleV2, error) {
	if packet.Type != PacketVoiceBundleV2 || packet.SessionID == 0 || packet.RequestID != 0 {
		return VoiceBundleV2{}, fmt.Errorf("%w: v2 packet header", ErrInvalidVoiceBundle)
	}
	bundle, err := DecodeVoiceBundleV2(packet.Payload)
	if err != nil {
		return VoiceBundleV2{}, err
	}
	if (bundle.ReceiverEpoch == 0) != upstream || packet.Sequence != bundle.Frames[len(bundle.Frames)-1].Sequence {
		return VoiceBundleV2{}, fmt.Errorf("%w: v2 direction or current sequence", ErrInvalidVoiceBundle)
	}
	return bundle, nil
}

func NewVoiceBundleV2Packet(senderID uint64, bundle VoiceBundleV2, upstream bool) (VoicePacket, error) {
	if senderID == 0 || (bundle.ReceiverEpoch == 0) != upstream {
		return VoicePacket{}, fmt.Errorf("%w: v2 sender or direction", ErrInvalidVoiceBundle)
	}
	payload, err := EncodeVoiceBundleV2(bundle)
	if err != nil {
		return VoicePacket{}, err
	}
	return VoicePacket{
		Type:      PacketVoiceBundleV2,
		SessionID: senderID,
		Sequence:  bundle.Frames[len(bundle.Frames)-1].Sequence,
		Payload:   payload,
	}, nil
}
