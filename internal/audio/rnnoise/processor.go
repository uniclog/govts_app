package rnnoise

import (
	"errors"
	"fmt"
	"math"

	lib "github.com/VoiceBlender/rnnoise-go"
	"github.com/VoiceBlender/rnnoise-go/model"

	"uniclog.io/sonoryx/internal/audio"
)

const (
	SampleRate = 48000
	FrameSize  = SampleRate / 100
)

var ErrClosed = errors.New("RNNoise processor is closed")

var _ audio.PCMFilter = (*Processor)(nil)

// Processor applies the built-in RNNoise model to one mono 48 kHz PCM stream.
// It must be used sequentially because the model keeps history between frames.
type Processor struct {
	state      *lib.Denoiser
	frame      []float32
	settings   Settings
	needsReset bool
}

type Settings interface {
	RNNoiseEnabled() bool
	RNNoiseSensitivity() float32
}

func New(settings ...Settings) (*Processor, error) {
	p := &Processor{frame: make([]float32, FrameSize)}
	if len(settings) > 0 {
		p.settings = settings[0]
	}
	if err := p.Reset(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *Processor) Reset() error {
	if p.state != nil {
		p.state.Reset()
	} else {
		weights, err := model.Load()
		if err != nil {
			return fmt.Errorf("load RNNoise model: %w", err)
		}
		state, err := lib.New(lib.Options{SampleRate: SampleRate, Model: weights})
		if err != nil {
			return fmt.Errorf("initialize RNNoise: %w", err)
		}
		p.state = state
	}
	p.needsReset = false
	if len(p.frame) != FrameSize {
		p.frame = make([]float32, FrameSize)
	}
	return nil
}

func (p *Processor) Process(samples []int16) error {
	if p.state == nil {
		return ErrClosed
	}
	enabled := true
	if p.settings != nil {
		enabled = p.settings.RNNoiseEnabled()
	}
	if !enabled {
		p.needsReset = true
		return nil
	}
	if p.needsReset {
		if err := p.Reset(); err != nil {
			return err
		}
	}
	if len(samples)%FrameSize != 0 {
		return fmt.Errorf("RNNoise requires blocks divisible by %d samples, got %d", FrameSize, len(samples))
	}

	for offset := 0; offset < len(samples); offset += FrameSize {
		for i := range FrameSize {
			p.frame[i] = float32(samples[offset+i])
		}
		if _, err := p.state.Process(p.frame, p.frame); err != nil {
			return fmt.Errorf("process RNNoise frame: %w", err)
		}
		strength := float32(1)
		if p.settings != nil {
			strength = p.settings.RNNoiseSensitivity()
		}
		for i := range FrameSize {
			dry := float32(samples[offset+i])
			samples[offset+i] = pcm16(dry + (p.frame[i]-dry)*strength)
		}
	}
	return nil
}

func (p *Processor) Close() error {
	p.state = nil
	p.frame = nil
	return nil
}

func pcm16(sample float32) int16 {
	if math.IsNaN(float64(sample)) {
		return 0
	}
	if sample > math.MaxInt16 {
		return math.MaxInt16
	}
	if sample < math.MinInt16 {
		return math.MinInt16
	}
	return int16(math.Round(float64(sample)))
}
