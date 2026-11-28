package client

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"uniclog.io/sonoryx/internal/audio"
	"uniclog.io/sonoryx/internal/audio/voicegate"
	"uniclog.io/sonoryx/internal/soundprefs"
)

// SetMuted and Send share the same lock. On return, an earlier send has
// completed and no later send can use an old capture epoch, even after unmute.
type AudioControlState struct {
	mu                    sync.Mutex
	meterMu               sync.RWMutex
	sendMu                sync.Mutex
	voicePaused           bool
	commandCapturePaused  bool
	commandPlaybackPaused bool
	muted, deafened       bool
	captureAvailable      bool
	rnnoiseEnabled        bool
	rnnoiseSensitivity    float32
	microphoneGain        float32
	vadEnabled            bool
	vadMode               voicegate.Mode
	vadSensitivity        float32
	vadOpen               bool
	epoch                 uint64
	playbackEpoch         uint64
	soundPack             string
	soundVolume           float32
	muteOnAway            bool
	deafenOnAway          bool
	soundCues             map[string]bool
	// soundCueView is every cue with its state. It is built once per change
	// and shared read-only with snapshots.
	soundCueView       map[string]bool
	awayMuted          bool
	awayDeafened       bool
	player             audio.DeafenPlayer
	monitor            func([]int16)
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
		microphoneGain:     1,
		vadMode:            voicegate.ModeHybrid,
		vadSensitivity:     voicegate.DefaultSensitivity,
		soundPack:          DefaultSoundPack,
		soundVolume:        DefaultSoundVolume,
		muteOnAway:         true,
		participantVolumes: make(map[uint64]float32),
	}
}

const (
	DefaultParticipantVolume = float32(1)
	MaxParticipantVolume     = float32(9)
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
		return fmt.Errorf("participant volume must be between 0 and %g, got %g", MaxParticipantVolume, value)
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

// SetMonitor routes the frames released by the voice gate to fn instead of
// the encoder: the user hears themselves and nothing is sent to the server.
// A nil fn restores transmission.
func (a *AudioControlState) SetMonitor(fn func([]int16)) {
	a.mu.Lock()
	a.monitor = fn
	a.mu.Unlock()
}

func (a *AudioControlState) Monitor() func([]int16) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.monitor
}

// MaxMicrophoneGain caps the software microphone gain at +6 dB.
const MaxMicrophoneGain = float32(2)

// MicrophoneGain is a linear factor applied to captured PCM before filtering,
// so the meter, RNNoise, VAD and the encoder all see the adjusted level.
func (a *AudioControlState) MicrophoneGain() float32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.microphoneGain
}

func (a *AudioControlState) SetMicrophoneGain(value float32) error {
	if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) || value < 0 || value > MaxMicrophoneGain {
		return fmt.Errorf("microphone gain must be between 0 and %g, got %g", MaxMicrophoneGain, value)
	}
	a.mu.Lock()
	changed := a.microphoneGain != value
	a.microphoneGain = value
	a.mu.Unlock()
	if changed && a.onChange != nil {
		a.onChange()
	}
	return nil
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
	a.awayMuted = false
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
	a.awayDeafened = false
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
	a.mu.Lock()
	allowed := !a.muted && !a.voicePaused && !a.commandCapturePaused && epoch == a.epoch
	a.mu.Unlock()
	if !allowed {
		return nil
	}
	return send()
}

func (a *AudioControlState) SoundSnapshot() (string, float32) {
	a.mu.Lock()
	defer a.mu.Unlock()
	pack := a.soundPack
	if pack == "" {
		pack = DefaultSoundPack
	}
	return pack, a.soundVolume
}

func (a *AudioControlState) SetSoundPack(id string) error {
	pack, ok := NormalizeSoundPack(id)
	if !ok {
		return errors.New("unknown sound pack")
	}
	a.mu.Lock()
	changed := a.soundPack != pack
	a.soundPack = pack
	a.mu.Unlock()
	if changed && a.onChange != nil {
		a.onChange()
	}
	return nil
}

func (a *AudioControlState) SetSoundVolume(value float32) error {
	if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) || value < 0 || value > 1 {
		return errors.New("sound volume must be between 0 and 1")
	}
	a.mu.Lock()
	changed := a.soundVolume != value
	a.soundVolume = value
	a.mu.Unlock()
	if changed && a.onChange != nil {
		a.onChange()
	}
	return nil
}

func (a *AudioControlState) MuteOnAway() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.muteOnAway
}

func (a *AudioControlState) SetMuteOnAway(value bool) {
	a.mu.Lock()
	changed := a.muteOnAway != value
	a.muteOnAway = value
	a.mu.Unlock()
	if changed && a.onChange != nil {
		a.onChange()
	}
}

func (a *AudioControlState) DeafenOnAway() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.deafenOnAway
}

func (a *AudioControlState) SetDeafenOnAway(value bool) {
	a.mu.Lock()
	changed := a.deafenOnAway != value
	a.deafenOnAway = value
	a.mu.Unlock()
	if changed && a.onChange != nil {
		a.onChange()
	}
}

// SoundCues returns every button cue with its state. The map is shared
// between snapshots and must not be modified.
func (a *AudioControlState) SoundCues() map[string]bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.soundCueView == nil {
		a.soundCueView = soundprefs.SoundCueStates(a.soundCues)
	}
	return a.soundCueView
}

func (a *AudioControlState) SetSoundCues(cues map[string]bool) {
	normalized := soundprefs.NormalizeSoundCues(cues)
	a.mu.Lock()
	a.soundCues = normalized
	a.soundCueView = nil
	a.mu.Unlock()
	if a.onChange != nil {
		a.onChange()
	}
}

func (a *AudioControlState) SetSoundCue(id string, enabled bool) error {
	if !soundprefs.KnownCue(id) {
		return errors.New("unknown sound cue")
	}
	a.mu.Lock()
	if a.soundCues == nil {
		a.soundCues = map[string]bool{}
	}
	if enabled {
		delete(a.soundCues, id)
	} else {
		a.soundCues[id] = false
	}
	a.soundCueView = nil
	a.mu.Unlock()
	if a.onChange != nil {
		a.onChange()
	}
	return nil
}

func (a *AudioControlState) CueEnabled(kind NotificationSoundKind) bool {
	id, ok := CueID(kind)
	if !ok {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return soundprefs.SoundCueEnabled(a.soundCues, id)
}

// MuteForAway mutes the microphone and remembers that this transition did it.
// A microphone that was already off is left to the user.
func (a *AudioControlState) MuteForAway() {
	a.sendMu.Lock()
	a.mu.Lock()
	changed := false
	if !a.muted {
		a.muted = true
		a.epoch++
		a.vadOpen = false
		a.awayMuted = true
		changed = true
	}
	a.mu.Unlock()
	a.sendMu.Unlock()
	if changed && a.onChange != nil {
		a.onChange()
	}
}

// DeafenForAway turns playback off and remembers that this transition did it.
// A speaker that was already off is left to the user.
func (a *AudioControlState) DeafenForAway() {
	a.mu.Lock()
	if a.deafened {
		a.mu.Unlock()
		return
	}
	if a.player != nil {
		if err := a.player.SetDeafened(true); err != nil {
			a.mu.Unlock()
			return
		}
	}
	a.deafened = true
	a.playbackEpoch++
	a.awayDeafened = true
	a.mu.Unlock()
	if a.onChange != nil {
		a.onChange()
	}
}

// UndeafenForReturn turns playback back on only when Away deafened it.
// It reports whether playback was turned on, so a failed return can undo it.
func (a *AudioControlState) UndeafenForReturn() (bool, error) {
	a.mu.Lock()
	if !a.awayDeafened {
		a.mu.Unlock()
		return false, nil
	}
	if a.player != nil {
		if err := a.player.SetDeafened(false); err != nil {
			a.mu.Unlock()
			return false, err
		}
	}
	a.awayDeafened = false
	changed := false
	if a.deafened {
		a.deafened = false
		a.playbackEpoch++
		changed = true
	}
	a.mu.Unlock()
	if changed && a.onChange != nil {
		a.onChange()
	}
	return changed, nil
}

// SavedDeafened is the deafen state worth persisting. A deafen made by Away
// belongs to that presence and must not outlive the session.
func (a *AudioControlState) SavedDeafened() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.deafened && !a.awayDeafened
}

// ReleaseAway undoes what Away turned off. The server forgets presence with
// the session, so a new or closed session must not keep it either.
func (a *AudioControlState) ReleaseAway() {
	_, _ = a.UndeafenForReturn()
	_ = a.UnmuteForReturn()
}

// UnmuteForReturn turns the microphone back on only when Away muted it.
func (a *AudioControlState) UnmuteForReturn() error {
	a.sendMu.Lock()
	a.mu.Lock()
	if !a.awayMuted {
		a.mu.Unlock()
		a.sendMu.Unlock()
		return nil
	}
	if !a.captureAvailable {
		a.awayMuted = false
		a.mu.Unlock()
		a.sendMu.Unlock()
		return errors.New("microphone is unavailable")
	}
	a.awayMuted = false
	changed := false
	if a.muted {
		a.muted = false
		a.epoch++
		changed = true
	}
	a.mu.Unlock()
	a.sendMu.Unlock()
	if changed && a.onChange != nil {
		a.onChange()
	}
	return nil
}

func (a *AudioControlState) PlaybackSnapshot() (bool, uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.deafened || a.commandPlaybackPaused, a.playbackEpoch
}
func (a *AudioControlState) Play(epoch uint64, write func() error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.deafened || a.commandPlaybackPaused || epoch != a.playbackEpoch {
		return nil
	}
	return write()
}

const SpeakingHangover = 300 * time.Millisecond

func activePCM(samples []int16) bool {
	// Show even the quietest noise so its sender can be identified.
	for _, sample := range samples {
		if sample != 0 {
			return true
		}
	}
	return false
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
