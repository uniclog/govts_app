package audio

import (
	"fmt"
	"sync"

	pionopus "github.com/pion/opus"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

const maxOpusPacketSize = protocol.MaxVoiceFrameSize

type OpusEncoder struct {
	config  CodecConfig
	encoder *pionopus.Encoder
	mu      sync.Mutex
}

type OpusDecoder struct {
	config  CodecConfig
	decoder pionopus.Decoder
}

func NewOpusEncoder(config CodecConfig) (*OpusEncoder, error) {
	options := []pionopus.EncoderOption{
		pionopus.WithSampleRate(config.SampleRate),
		pionopus.WithChannels(config.Channels),
	}
	if config.Bitrate != 0 {
		options = append(options, pionopus.WithBitrate(config.Bitrate))
	}
	application, err := opusApplication(config.Application)
	if err != nil {
		return nil, err
	}
	options = append(options, pionopus.WithApplication(application))

	encoder, err := pionopus.NewEncoder(options...)
	if err != nil {
		return nil, err
	}

	return &OpusEncoder{
		config:  config,
		encoder: encoder,
	}, nil
}

func opusApplication(application domain.OpusApplication) (pionopus.Application, error) {
	switch application {
	case 0, domain.OpusApplicationAudio:
		return pionopus.ApplicationAudio, nil
	case domain.OpusApplicationVoIP:
		return pionopus.ApplicationVoIP, nil
	default:
		return 0, fmt.Errorf("unsupported Opus application: %d", application)
	}
}

func (e *OpusEncoder) Encode(samples []int16) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	pcm := PCMBytes(samples)

	packet := make([]byte, maxOpusPacketSize)

	n, err := e.encoder.Encode(pcm, packet)
	if err != nil {
		return nil, err
	}

	return packet[:n], nil
}

// Configure starts a fresh Opus stream with the channel settings, discarding
// lookahead and prediction history. It is safe alongside encoding.
func (e *OpusEncoder) Configure(profile domain.AudioProfile) error {
	if err := domain.ValidateAudioProfile(profile); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	config := e.config
	config.Bitrate = int(profile.Bitrate)
	config.Application = profile.Application
	// A new source identity must not carry lookahead/predictor samples from
	// the preceding channel or mute period into its first encoded frame.
	fresh, err := NewOpusEncoder(config)
	if err != nil {
		return err
	}
	e.encoder = fresh.encoder
	e.config = config
	return nil
}

func NewOpusDecoder(config CodecConfig) (*OpusDecoder, error) {
	decoder, err := pionopus.NewDecoderWithOutput(
		config.SampleRate,
		config.Channels,
	)
	if err != nil {
		return nil, err
	}

	return &OpusDecoder{
		config:  config,
		decoder: decoder,
	}, nil
}

func (d *OpusDecoder) Decode(data []byte) ([]int16, error) {
	samples := make(
		[]int16,
		d.config.SamplesPerFrame*d.config.Channels,
	)

	n, err := d.decoder.DecodeToInt16(data, samples)
	if err != nil {
		return nil, err
	}

	return samples[:n*d.config.Channels], nil
}

func (d *OpusDecoder) ConcealLoss() ([]int16, error) {
	samples := make(
		[]int16,
		d.config.SamplesPerFrame*d.config.Channels,
	)

	if err := d.decoder.DecodePLC(samples); err != nil {
		return nil, err
	}

	return samples, nil
}
