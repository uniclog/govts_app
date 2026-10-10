package audio

import (
	"encoding/binary"
	"time"

	"uniclog.io/sonoryx/internal/domain"
)

type Frame struct {
	ControlEpoch uint64
	Data         []byte
	Duration     time.Duration
}

// MediaFrame is an encoded frame received from a remote audio stream.
// SenderID and Sequence identify the stream and the frame order within it.
// Missing marks a placeholder for a frame lost in transit; it has no Data.
type MediaFrame struct {
	SenderID uint64
	Sequence uint32
	Data     []byte
	Duration time.Duration
	Missing  bool
}

type PCMFrame struct {
	PlaybackEpoch uint64
	ControlEpoch  uint64
	Samples       []int16
	Duration      time.Duration
}

// MediaPCMFrame is decoded PCM that still belongs to a remote audio stream.
type MediaPCMFrame struct {
	PlaybackEpoch uint64
	SenderID      uint64
	Sequence      uint32
	Samples       []int16
	Duration      time.Duration
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

type PCM16Encoder struct {
	config CodecConfig
}

func NewPCM16Encoder(config CodecConfig) *PCM16Encoder {
	return &PCM16Encoder{
		config: config,
	}
}

func (PCM16Encoder) Encode(samples []int16) ([]byte, error) {
	return int16ToBytes(samples), nil
}

func int16ToBytes(samples []int16) []byte {
	data := make([]byte, len(samples)*2)

	for i, sample := range samples {
		binary.LittleEndian.PutUint16(
			data[i*2:i*2+2],
			uint16(sample),
		)
	}

	return data
}
