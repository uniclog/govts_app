package client

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"uniclog.io/govts/internal/audio"
	"uniclog.io/govts/internal/audio/voicegate"
)

// SetMuted and Send share the same lock. On return, an earlier send has
// completed and no later send can use an old capture epoch, even after unmute.
type AudioControlState struct {
	mu                 sync.Mutex
	meterMu            sync.RWMutex
	sendMu             sync.Mutex
	muted, deafened    bool
	captureAvailable   bool
	rnnoiseEnabled     bool
	rnnoiseSensitivity float32
	vadEnabled         bool
	vadMode            voicegate.Mode
	vadSensitivity     float32
	vadOpen            bool
	epoch              uint64
	playbackEpoch      uint64
	player             audio.DeafenPlayer
	onChange           func()
	meter              AudioMeterSample
	participantVolumes map[uint64]float32
}

// AudioMeterSample contains normalized microphone levels without retaining or
// exposing PCM samples. Input is measured before filters, Processed after the
// filter, and Transmitted only while the voice gate lets audio through.
type AudioMeterSample struct {
	Input       float32
	Processed   float32
	Transmitted float32
	Sequence    uint64
}

func NewAudioControlState(onChange func()) *AudioControlState {
	return &AudioControlState{
		onChange:           onChange,
		captureAvailable:   true,
		rnnoiseEnabled:     true,
		rnnoiseSensitivity: 1,
		vadMode:            voicegate.ModeHybrid,
		vadSensitivity:     voicegate.DefaultSensitivity,
		participantVolumes: make(map[uint64]float32),
	}
}

const (
	DefaultParticipantVolume = float32(1)
	MaxParticipantVolume     = float32(2)
)

func (a *AudioControlState) ParticipantVolume(id uint64) float32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if value, ok := a.participantVolumes[id]; ok {
		return value
	}
	return DefaultParticipantVolume
}

func (a *AudioControlState) SetParticipantVolume(id uint64, value float32) error {
	if id == 0 {
		return errors.New("participant ID must not be zero")
	}
	if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) || value < 0 || value > MaxParticipantVolume {
		return fmt.Errorf("participant volume must be between 0 and 2, got %g", value)
	}
	a.mu.Lock()
	if value == DefaultParticipantVolume {
		delete(a.participantVolumes, id)
	} else {
		a.participantVolumes[id] = value
	}
	a.mu.Unlock()
	return nil
}

func (a *AudioControlState) RemoveParticipantVolume(id uint64) {
	a.mu.Lock()
	delete(a.participantVolumes, id)
	a.mu.Unlock()
}

func (a *AudioControlState) ClearParticipantVolumes() {
	a.mu.Lock()
	clear(a.participantVolumes)
	a.mu.Unlock()
}

func (a *AudioControlState) SetAudioMeter(input, processed, transmitted float32) {
	a.meterMu.Lock()
	a.meter.Input = clampMeterLevel(input)
	a.meter.Processed = clampMeterLevel(processed)
	a.meter.Transmitted = clampMeterLevel(transmitted)
	a.meter.Sequence++
	a.meterMu.Unlock()
}

func (a *AudioControlState) AudioMeterSnapshot() AudioMeterSample {
	a.meterMu.RLock()
	defer a.meterMu.RUnlock()
	return a.meter
}

func clampMeterLevel(value float32) float32 {
	if value < 0 || math.IsNaN(float64(value)) {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func (a *AudioControlState) RNNoiseEnabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rnnoiseEnabled
}

func (a *AudioControlState) RNNoiseSensitivity() float32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rnnoiseSensitivity
}

func (a *AudioControlState) SetRNNoiseSensitivity(value float32) error {
	if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) || value < 0 || value > 1 {
		return fmt.Errorf("RNNoise sensitivity must be between 0 and 1, got %g", value)
	}
	a.mu.Lock()
	changed := a.rnnoiseSensitivity != value
	a.rnnoiseSensitivity = value
	a.mu.Unlock()
	if changed && a.onChange != nil {
		a.onChange()
	}
	return nil
}

func (a *AudioControlState) SetRNNoiseEnabled(value bool) {
	a.mu.Lock()
	changed := a.rnnoiseEnabled != value
	a.rnnoiseEnabled = value
	a.mu.Unlock()
	if changed && a.onChange != nil {
		a.onChange()
	}
}

type VADSettings struct {
	Enabled     bool
	Mode        voicegate.Mode
	Sensitivity float32
	Open        bool
}

func (a *AudioControlState) VADSnapshot() VADSettings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return VADSettings{Enabled: a.vadEnabled, Mode: a.vadMode, Sensitivity: a.vadSensitivity, Open: a.vadOpen}
}

func (settings VADSettings) GateConfig() voicegate.Config {
	config := voicegate.DefaultConfig()
	if settings.Enabled {
		config.Mode = settings.Mode
	}
	config.Sensitivity = settings.Sensitivity
	return config
}

func (a *AudioControlState) SetVADEnabled(value bool) {
	a.mu.Lock()
	changed := a.vadEnabled != value
	a.vadEnabled = value
	if !value {
		a.vadOpen = false
	}
	a.mu.Unlock()
	if changed && a.onChange != nil {
		a.onChange()
	}
}

func (a *AudioControlState) SetVADMode(value voicegate.Mode) error {
	if value != voicegate.ModeLevel && value != voicegate.ModeVAD && value != voicegate.ModeHybrid {
		return fmt.Errorf("VAD mode must be level, vad, or hybrid, got %q", value)
	}
	a.mu.Lock()
	changed := a.vadMode != value
	a.vadMode = value
	if changed {
		a.vadOpen = false
	}
	a.mu.Unlock()
	if changed && a.onChange != nil {
		a.onChange()
	}
	return nil
}

func (a *AudioControlState) SetVADSensitivity(value float32) error {
	if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) || value < 0 || value > 1 {
		return fmt.Errorf("VAD sensitivity must be between 0 and 1, got %g", value)
	}
	a.mu.Lock()
	changed := a.vadSensitivity != value
	a.vadSensitivity = value
	if changed {
		a.vadOpen = false
	}
	a.mu.Unlock()
	if changed && a.onChange != nil {
		a.onChange()
	}
	return nil
}

func (a *AudioControlState) SetVADOpen(value bool) {
	a.mu.Lock()
	value = value && a.vadEnabled && !a.muted
	changed := a.vadOpen != value
	a.vadOpen = value
	a.mu.Unlock()
	if changed && a.onChange != nil {
		a.onChange()
	}
}
func (a *AudioControlState) Snapshot() (bool, bool, uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.muted, a.deafened, a.epoch
}
func (a *AudioControlState) CaptureAvailable() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.captureAvailable
}
func (a *AudioControlState) SetCaptureAvailable(available bool) {
	a.sendMu.Lock()
	a.mu.Lock()
	changed := a.captureAvailable != available
	a.captureAvailable = available
	if !available && !a.muted {
		a.muted = true
		a.epoch++
		a.vadOpen = false
		changed = true
	}
	a.mu.Unlock()
	a.sendMu.Unlock()
	if changed && a.onChange != nil {
		a.onChange()
	}
}
func (a *AudioControlState) SetMuted(value bool) error {
	a.sendMu.Lock()
	a.mu.Lock()
	if !value && !a.captureAvailable {
		a.mu.Unlock()
		a.sendMu.Unlock()
		return errors.New("microphone is unavailable")
	}
	if a.muted != value {
		a.muted = value
		a.epoch++
		if value {
			a.vadOpen = false
		}
	}
	a.mu.Unlock()
	a.sendMu.Unlock()
	if a.onChange != nil {
		a.onChange()
	}
	return nil
}
func (a *AudioControlState) SetDeafened(value bool) error {
	a.mu.Lock()
	if a.player != nil {
		if err := a.player.SetDeafened(value); err != nil {
			a.mu.Unlock()
			return err
		}
	}
	if a.deafened != value {
		a.playbackEpoch++
	}
	a.deafened = value
	a.mu.Unlock()
	if a.onChange != nil {
		a.onChange()
	}
	return nil
}
func (a *AudioControlState) AttachPlayer(player audio.DeafenPlayer) (func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.player != nil {
		return nil, errors.New("playback already attached")
	}
	if err := player.SetDeafened(a.deafened); err != nil {
		return nil, err
	}
	a.player = player
	return func() { a.mu.Lock(); a.player = nil; a.mu.Unlock() }, nil
}
func (a *AudioControlState) Send(epoch uint64, send func() error) error {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	muted, _, current := a.Snapshot()
	if muted || epoch != current {
		return nil
	}
	return send()
}

func (a *AudioControlState) PlaybackSnapshot() (bool, uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.deafened, a.playbackEpoch
}
func (a *AudioControlState) Play(epoch uint64, write func() error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.deafened || epoch != a.playbackEpoch {
		return nil
	}
	return write()
}

const SpeakingRMSThreshold = 600
const SpeakingHangover = 300 * time.Millisecond

type SpeakingDetector struct{ ActiveUntil time.Time }

func (d *SpeakingDetector) Observe(samples []int16, now time.Time) bool {
	if activePCM(samples) {
		d.ActiveUntil = now.Add(SpeakingHangover)
	}
	return d.Speaking(now)
}
func (d *SpeakingDetector) Speaking(now time.Time) bool { return now.Before(d.ActiveUntil) }
func activePCM(samples []int16) bool {
	if len(samples) == 0 {
		return false
	}
	var sum float64
	for _, sample := range samples {
		v := float64(sample)
		sum += v * v
	}
	return sum/float64(len(samples)) >= SpeakingRMSThreshold*SpeakingRMSThreshold
}

func (s *State) ObserveSpeaking(id uint64, samples []int16, now time.Time) bool {
	return s.observeSpeaking(id, activePCM(samples), now)
}

func (s *State) ObserveVoiceActivity(id uint64, active bool, now time.Time) bool {
	return s.observeSpeaking(id, active, now)
}

func (s *State) observeSpeaking(id uint64, active bool, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == 0 || s.channelID == 0 || !s.snapshotFresh {
		return false
	}
	if id == s.sessionID {
		muted, _, _ := s.Audio.Snapshot()
		if muted {
			return false
		}
	} else {
		found := false
		for _, p := range s.snapshot.Participants {
			if p.SessionID == id && p.ChannelID == s.channelID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if active {
		_, wasSpeaking := s.speaking[id]
		s.speaking[id] = now.Add(SpeakingHangover)
		if !wasSpeaking {
			s.notifyLocked()
		}
	}
	return true
}

func SpeakingLoop(ctx context.Context, state *State) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-ticker.C:
			state.clearSpeakingAt(now)
		}
	}
}
