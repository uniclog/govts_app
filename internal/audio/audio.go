package audio

import (
	"encoding/binary"
	"time"

	"uniclog.io/sonoryx/internal/domain"
)

type Frame struct {
	Voice        VoiceIdentity
	Sequence     uint32
	CreatedAt    time.Time
	ControlEpoch uint64
	Data         []byte
	Duration     time.Duration
}

// MediaFrame is an encoded frame received from a remote audio stream.
// SenderID and Sequence identify the stream and the frame order within it.
// Missing marks a placeholder for a frame lost in transit; it has no Data.
type MediaFrame struct {
	DatagramSequence uint32
	ArrivedAt        time.Time
	Redundant        bool
	Voice            VoiceIdentity
	Batch            []MediaFrame
	SenderID         uint64
	Sequence         uint32
	Data             []byte
	Duration         time.Duration
	Missing          bool
}

type PCMFrame struct {
	Voices        []VoiceReference
	PlaybackEpoch uint64
	ControlEpoch  uint64
	Samples       []int16
	Duration      time.Duration
	// Cue marks the first frame of an app sound cue for diagnostics; 0 otherwise.
	Cue uint8
}

// MediaPCMFrame is decoded PCM that still belongs to a remote audio stream.
type MediaPCMFrame struct {
	Voice         VoiceIdentity
	PlaybackEpoch uint64
	SenderID      uint64
	Sequence      uint32
	Samples       []int16
	Duration      time.Duration
	Cue           uint8
}

// A zero identity denotes local audio/effects. Network ingress always assigns
// a nonzero session generation and server-owned source/receiver epochs.
type VoiceIdentity struct {
	Generation    uint64
	SourceEpoch   uint32
	ReceiverEpoch uint32
}

type VoiceReference struct {
	SenderID uint64
	Identity VoiceIdentity
}

// PlaybackStats reports how much audio waits for the output device and how
// long ago the device last pulled data, so a cue handoff can be logged.
type PlaybackStats interface {
	PlaybackStats() (buffered time.Duration, sinceCallback time.Duration)
}

type Encoder interface {
	Encode(samples []int16) ([]byte, error)
}

type Decoder interface {
	Decode(data []byte) ([]int16, error)
}

// LossConcealer is implemented by decoders that can synthesize one frame in
// place of a lost packet from their current state.
type LossConcealer interface {
	ConcealLoss() ([]int16, error)
}

type CodecConfig struct {
	SampleRate      int
	Channels        int
	SamplesPerFrame int
	Bitrate         int
	Application     domain.OpusApplication
}

type Player interface {
	Write(samples []int16) error
	Close() error
}

// SetDeafened serializes with Write and discards application playback buffers.
type DeafenPlayer interface {
	Player
	SetDeafened(bool) error
}

type Recorder interface {
	Read(samples []int16) (int, error)
	Close() error
}

func PCMBytes(samples []int16) []byte {
	data := make([]byte, len(samples)*2)

	for i, sample := range samples {
		binary.LittleEndian.PutUint16(
			data[i*2:i*2+2],
			uint16(sample),
		)
	}

	return data
}
