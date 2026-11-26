package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

var (
	ErrPacketTooShort    = errors.New("packet too short")
	ErrPacketTooLarge    = errors.New("packet too large")
	ErrPayloadTooLarge   = errors.New("packet payload too large")
	ErrInvalidPacketType = errors.New("invalid packet type")
	ErrRejectedDatagram  = errors.New("rejected datagram")
)

const (
	PacketHello uint8 = iota + 1 // pre-authentication handshake; servers reply "client upgrade required"
	PacketVoice
	PacketHelloAck // reserved: acknowledged the removed plain handshake
	PacketHeartbeat
	PacketDisconnect
	PacketJoinChannel
	PacketJoinChannelAck
	PacketError
	PacketStateSnapshotRequest
	PacketStateSnapshotAck
	PacketHeartbeatAck
	PacketSessionInvalid
	PacketStateEvent
	PacketMediaCredentialRequest
	PacketMediaCredentialAck
	PacketServerVersionTooOld
	PacketAuthInit
	PacketAuthChallenge
	PacketAuthFinish
	PacketAuthAck
	PacketKick
	PacketBan
	PacketDrag
	PacketModerationAck
	PacketAccountPrivileges
	PacketAudioState
	PacketAudioStateAck
	PacketChatRequest
	PacketChatAck
	PacketChatChanged
	// PacketVoiceBundle carries the current and the previous voice frames of
	// one sender; server → client only, for clients that support it.
	PacketVoiceBundle
	PacketServerStatusRequest
	PacketServerStatusAck
	PacketPresence
	PacketPresenceAck
	PacketAttention
	PacketAttentionAck
	// PacketAdminRequest and PacketAdminAck carry the remote administration
	// operations of internal/protocol/admin.go.
	PacketAdminRequest
	PacketAdminAck

	// Adaptive voice protocol. These IDs are reserved while its runtime
	// integration is in progress; existing packet IDs must remain stable.
	PacketVoiceBundleV2
	PacketVoiceSummary
	PacketVoiceReport
	PacketVoiceState
	PacketVoiceStateAck
	PacketVoiceStateRequest

	PacketFileAccessRequest
	PacketFileAccessAck
	PacketEnd
)

type VoicePacket struct {
	Type      uint8
	SessionID uint64
	Sequence  uint32
	RequestID uint32
	Payload   []byte
}

func NewVoicePacket(sessionID uint64, sequence uint32, payload []byte) VoicePacket {
	return VoicePacket{
		Type:      PacketVoice,
		SessionID: sessionID,
		Sequence:  sequence,
		Payload:   payload,
	}
}

// index
// 0        Type       1 byte
// 1..8     SessionID  8 bytes
// 9..12    Sequence   4 bytes
// 13..16   RequestID  4 bytes
// 17..N     Payload

const (
	HeaderSize          = 17
	MaxPayloadSize      = 1200
	MaxWireDatagramSize = HeaderSize + MaxPayloadSize + 33 // encrypted record header and GCM tag

	// MaxDatagramSize is kept as a compatibility alias. New transport code
	// must use MaxWireDatagramSize as the protocol-wide hard wire limit.
	MaxDatagramSize = MaxWireDatagramSize
)

func encodeHeader(packet VoicePacket) []byte {
	header := make([]byte, HeaderSize)
	header[0] = packet.Type
	binary.BigEndian.PutUint64(header[1:9], packet.SessionID)
	binary.BigEndian.PutUint32(header[9:13], packet.Sequence)
	binary.BigEndian.PutUint32(header[13:17], packet.RequestID)
	return header
}

func EncodePacket(packet VoicePacket) ([]byte, error) {
	if !validPacketType(packet.Type) {
		return nil, fmt.Errorf("%w: %d", ErrInvalidPacketType, packet.Type)
	}
	if len(packet.Payload) > MaxPayloadSize {
		return nil, fmt.Errorf(
			"%w: got %d bytes, max %d",
			ErrPayloadTooLarge,
			len(packet.Payload),
			MaxPayloadSize,
		)
	}

	header := encodeHeader(packet)
	headerLen := len(header)
	data := make([]byte, headerLen+len(packet.Payload))
	copy(data, header)
	copy(data[headerLen:], packet.Payload)
	return data, nil
}

func DecodePacket(data []byte) (VoicePacket, error) {
	return decodePacket(data)
}

func decodePacket(data []byte) (VoicePacket, error) {
	if len(data) < HeaderSize {
		return VoicePacket{}, rejectDatagram(ErrPacketTooShort)
	}
	if len(data) > MaxWireDatagramSize {
		return VoicePacket{}, rejectDatagram(fmt.Errorf(
			"%w: got %d bytes, max %d",
			ErrPacketTooLarge,
			len(data),
			MaxWireDatagramSize,
		))
	}
	packetType := data[0]
	if !validPacketType(packetType) {
		return VoicePacket{}, rejectDatagram(fmt.Errorf(
			"%w: %d",
			ErrInvalidPacketType,
			packetType,
		))
	}
	packet := VoicePacket{
		Type:      packetType,
		SessionID: binary.BigEndian.Uint64(data[1:9]),
		Sequence:  binary.BigEndian.Uint32(data[9:13]),
		RequestID: binary.BigEndian.Uint32(data[13:17]),
		Payload:   data[17:],
	}
	return packet, nil
}

func rejectDatagram(err error) error {
	return fmt.Errorf("%w: %w", ErrRejectedDatagram, err)
}

func validPacketType(packetType uint8) bool {
	return packetType > 0 && packetType < PacketEnd
}

func NewErrorPacket(
	sessionID uint64,
	requestID uint32,
	message string,
) VoicePacket {
	return VoicePacket{
		Type:      PacketError,
		SessionID: sessionID,
		RequestID: requestID,
		Payload:   []byte(message),
	}
}
