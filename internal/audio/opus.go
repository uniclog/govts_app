package audio

import (
	"fmt"
	"sync"

	"uniclog.io/sonoryx/internal/domain"
	pionopus "github.com/pion/opus"
)

const maxOpusPacketSize = 4000

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

	pcm := int16ToBytes(samples)

	packet := make([]byte, maxOpusPacketSize)

	n, err := e.encoder.Encode(pcm, packet)
	if err != nil {
		return nil, err
	}

	return packet[:n], nil
}

// Configure updates channel-controlled Opus settings without rebuilding the
// PCM capture pipeline. It is safe to call while encoding is active.
func (e *OpusEncoder) Configure(profile domain.AudioProfile) error {
	if err := domain.ValidateAudioProfile(profile); err != nil {
		return err
	}
	application, err := opusApplication(profile.Application)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.encoder.SetBitrate(int(profile.Bitrate)); err != nil {
		return err
	}
	if err := e.encoder.SetApplication(application); err != nil {
		return err
	}
	e.config.Bitrate = int(profile.Bitrate)
	e.config.Application = profile.Application
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
