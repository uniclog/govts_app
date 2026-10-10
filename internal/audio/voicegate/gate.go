package voicegate

import (
	"fmt"
	"math"
	"time"

	"uniclog.io/sonoryx/internal/audio"
	"uniclog.io/sonoryx/internal/audio/vad"
)

type Mode string

const (
	ModeDisabled Mode = "disabled"
	ModeLevel    Mode = "level"
	ModeVAD      Mode = "vad"
	ModeHybrid   Mode = "hybrid"
)

type State uint8

const (
	Closed State = iota
	Candidate
	Open
	Hangover
)

const (
	DefaultSensitivity float32 = 0.5
	DefaultMinSpeech           = 60 * time.Millisecond
	DefaultPreRoll             = 60 * time.Millisecond
	DefaultHangover            = 300 * time.Millisecond
	MaxPreRoll                 = 100 * time.Millisecond
	leastSensitiveDBFS float32 = -20
	mostSensitiveDBFS  float32 = -55
)

type Config struct {
	Mode        Mode
	Sensitivity float32
	MinSpeech   time.Duration
	PreRoll     time.Duration
	Hangover    time.Duration
}

func DefaultConfig() Config {
	return Config{
		Mode:        ModeDisabled,
		Sensitivity: DefaultSensitivity,
		MinSpeech:   DefaultMinSpeech,
		PreRoll:     DefaultPreRoll,
		Hangover:    DefaultHangover,
	}
}

func ParseMode(value string) (Mode, error) {
	mode := Mode(value)
	if mode != ModeDisabled && mode != ModeLevel && mode != ModeVAD && mode != ModeHybrid {
		return "", fmt.Errorf("unknown VAD mode %q", value)
	}
	return mode, nil
}

func (c Config) Validate() error {
	if _, err := ParseMode(string(c.Mode)); err != nil {
		return err
	}
	if math.IsNaN(float64(c.Sensitivity)) || math.IsInf(float64(c.Sensitivity), 0) || c.Sensitivity < 0 || c.Sensitivity > 1 {
		return fmt.Errorf("VAD sensitivity must be between 0 and 1, got %g", c.Sensitivity)
	}
	if c.MinSpeech <= 0 {
		return fmt.Errorf("VAD minimum speech duration must be positive, got %s", c.MinSpeech)
	}
	if c.PreRoll < c.MinSpeech || c.PreRoll > MaxPreRoll {
		return fmt.Errorf("VAD pre-roll must be between minimum speech duration %s and %s, got %s", c.MinSpeech, MaxPreRoll, c.PreRoll)
	}
	if c.Hangover < 0 {
		return fmt.Errorf("VAD hangover must not be negative, got %s", c.Hangover)
	}
	return nil
}

func ThresholdDBFS(sensitivity float32) float32 {
	return leastSensitiveDBFS + (mostSensitiveDBFS-leastSensitiveDBFS)*sensitivity
}

func Active(config Config, result vad.Result) bool {
	levelActive := result.LevelDBFS >= ThresholdDBFS(config.Sensitivity)
	switch config.Mode {
	case ModeDisabled:
		return true
	case ModeLevel:
		return levelActive
	case ModeVAD:
		return result.Speech
	case ModeHybrid:
		return result.Speech && levelActive
	default:
		return false
	}
}

type Gate struct {
	config    Config
	state     State
	preRoll   []audio.PCMFrame
	candidate time.Duration
	inactive  time.Duration
	epoch     uint64
	haveEpoch bool
}

func New(config Config) (*Gate, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Gate{config: config}, nil
}

func (g *Gate) Configure(config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	if g.config != config {
		g.Reset()
		g.config = config
	}
	return nil
}

func (g *Gate) Process(frame audio.PCMFrame, result vad.Result) []audio.PCMFrame {
	if g.config.Mode == ModeDisabled {
		g.resetTransient()
		return []audio.PCMFrame{frame}
	}
	if g.haveEpoch && frame.ControlEpoch != g.epoch {
		g.Reset()
	}
	g.epoch = frame.ControlEpoch
	g.haveEpoch = true
	active := Active(g.config, result)

	switch g.state {
	case Closed:
		g.pushPreRoll(frame)
		if active {
			g.state = Candidate
			g.candidate = frame.Duration
			if g.candidate >= g.config.MinSpeech {
				return g.open()
			}
		}
	case Candidate:
		g.pushPreRoll(frame)
		if !active {
			g.state = Closed
			g.candidate = 0
			return nil
		}
		g.candidate += frame.Duration
		if g.candidate >= g.config.MinSpeech {
			return g.open()
		}
	case Open:
		if active {
			return []audio.PCMFrame{frame}
		}
		g.state = Hangover
		g.inactive = frame.Duration
		if g.inactive < g.config.Hangover {
			return []audio.PCMFrame{frame}
		}
		g.close()
	case Hangover:
		if active {
			g.state = Open
			g.inactive = 0
			return []audio.PCMFrame{frame}
		}
		g.inactive += frame.Duration
		if g.inactive < g.config.Hangover {
			return []audio.PCMFrame{frame}
		}
		g.close()
	}
	return nil
}

func (g *Gate) State() State { return g.state }

func (g *Gate) IsOpen() bool { return g.state == Open || g.state == Hangover }

func (g *Gate) Reset() {
	g.resetTransient()
	g.epoch = 0
	g.haveEpoch = false
}

func (g *Gate) resetTransient() {
	g.state = Closed
	g.candidate = 0
	g.inactive = 0
	for i := range g.preRoll {
		g.preRoll[i] = audio.PCMFrame{}
	}
	g.preRoll = g.preRoll[:0]
}

func (g *Gate) close() {
	g.resetTransient()
}

func (g *Gate) open() []audio.PCMFrame {
	g.state = Open
	g.candidate = 0
	g.inactive = 0
	frames := g.preRoll
	g.preRoll = nil
	return frames
}

func (g *Gate) pushPreRoll(frame audio.PCMFrame) {
	if g.config.PreRoll == 0 || frame.Duration <= 0 {
		return
	}
	copyOfFrame := frame
	copyOfFrame.Samples = append([]int16(nil), frame.Samples...)
	g.preRoll = append(g.preRoll, copyOfFrame)
	var duration time.Duration
	for i := len(g.preRoll) - 1; i >= 0; i-- {
		duration += g.preRoll[i].Duration
		if duration > g.config.PreRoll {
			g.preRoll[i] = audio.PCMFrame{}
			g.preRoll = g.preRoll[i+1:]
			break
		}
	}
}
