package clientapp

import (
	"errors"
	"fmt"
	"time"

	"uniclog.io/sonoryx/internal/audio"
	"uniclog.io/sonoryx/internal/audio/voicegate"
	voiceclient "uniclog.io/sonoryx/internal/client"
)

func (a *App) AudioDevices() (audio.DeviceList, error) { return a.listAudioDevices() }

func (a *App) SetCaptureDevice(id string) error {
	return a.setAudioDevice(id, true)
}

func (a *App) SetPlaybackDevice(id string) error {
	return a.setAudioDevice(id, false)
}

func (a *App) setAudioDevice(id string, capture bool) error {
	a.audioDeviceChangeMu.Lock()
	defer a.audioDeviceChangeMu.Unlock()

	devices, err := a.listAudioDevices()
	if err != nil {
		return err
	}
	available := devices.Playback
	if capture {
		available = devices.Capture
	}
	if id != "" {
		found := false
		for _, device := range available {
			if device.ID == id {
				found = true
				break
			}
		}
		if !found {
			return errors.New("audio device is no longer available")
		}
	}

	a.mu.Lock()
	selection := AudioDeviceSelection{CaptureID: a.captureDeviceID, PlaybackID: a.playbackDeviceID}
	changed := (capture && selection.CaptureID != id) || (!capture && selection.PlaybackID != id)
	active := a.active
	connected := a.state.ConnectionStatus() == voiceclient.ConnectionConnected
	a.mu.Unlock()
	retryCapture := capture && active && connected && !a.state.Audio.CaptureAvailable()
	if !changed && !retryCapture {
		return nil
	}
	if capture {
		selection.CaptureID = id
	} else {
		selection.PlaybackID = id
	}

	if active && connected {
		request := audioDeviceChange{selection: selection, done: make(chan error, 1)}
		timer := time.NewTimer(audioDeviceChangeTimeout)
		defer timer.Stop()
		select {
		case a.audioDeviceChanges <- request:
		case <-timer.C:
			return errors.New("timed out while applying audio device")
		case <-a.lifetimeCtx.Done():
			return ErrClosed
		}
		select {
		case err := <-request.done:
			if err != nil {
				return err
			}
		case <-timer.C:
			return errors.New("timed out while applying audio device")
		case <-a.lifetimeCtx.Done():
			return ErrClosed
		}
	}
	a.mu.Lock()
	a.captureDeviceID = selection.CaptureID
	a.playbackDeviceID = selection.PlaybackID
	a.mu.Unlock()
	if changed {
		a.events.append("audio", "Аудиоустройство изменено", 0)
		a.restartPreview(capture)
	}
	return nil
}

func (a *App) AudioMeterSnapshot() voiceclient.AudioMeterSample {
	return a.state.Audio.AudioMeterSnapshot()
}

func (a *App) SetMuted(value bool) error { return a.state.Audio.SetMuted(value) }

func (a *App) SetSoundPack(id string) error { return a.state.Audio.SetSoundPack(id) }

func (a *App) SetSoundVolume(value float32) error { return a.state.Audio.SetSoundVolume(value) }

func (a *App) SetMuteOnAway(value bool) { a.state.Audio.SetMuteOnAway(value) }

func (a *App) SetDeafenOnAway(value bool) { a.state.Audio.SetDeafenOnAway(value) }

func (a *App) SetSoundCues(cues map[string]bool) { a.state.Audio.SetSoundCues(cues) }

func (a *App) SetSoundCue(id string, enabled bool) error {
	return a.state.Audio.SetSoundCue(id, enabled)
}

func (a *App) PlayCue(kind voiceclient.NotificationSoundKind) {
	a.state.PlayNotification(kind)
}

// CueLead is how long a connected session needs before deafen can cut playback
// without swallowing the cue that was just queued. Zero means nothing will play.
func (a *App) CueLead(kind voiceclient.NotificationSoundKind) time.Duration {
	if a.state.ConnectionStatus() != voiceclient.ConnectionConnected {
		return 0
	}
	if !a.state.Audio.CueEnabled(kind) {
		return 0
	}
	deafened, _ := a.state.Audio.PlaybackSnapshot()
	if deafened {
		return 0
	}
	pack, volume := a.state.Audio.SoundSnapshot()
	if volume <= 0 {
		return 0
	}
	// The playback buffer is 100 ms. Wait it out so deafen does not wipe the
	// cue, and the wake pre-roll the mixer may put in front of it.
	return voiceclient.CueWakePreroll + voiceclient.NotificationCueDuration(pack, kind) + 120*time.Millisecond
}

// MessageReceived plays the incoming message cue. New messages are detected
// by the chat store in the UI, so the trigger comes from there.
func (a *App) MessageReceived() {
	a.state.PlayNotification(voiceclient.NotificationMessageReceived)
}

func (a *App) SetDeafened(value bool) error { return a.state.Audio.SetDeafened(value) }

// AudioSwitches reads the microphone and speaker switches without building a view.
func (a *App) AudioSwitches() (muted, deafened bool) {
	muted, deafened, _ = a.state.Audio.Snapshot()
	return muted, deafened
}

func (a *App) SavedDeafened() bool { return a.state.Audio.SavedDeafened() }

func (a *App) ParticipantVolume(id uint64) float32 { return a.state.Audio.ParticipantVolume(id) }

func (a *App) SetSavedParticipantVolumes(saved map[int64]map[int64]float32) {
	a.state.SetSavedParticipantVolumes(saved)
}

func (a *App) SetParticipantVolume(id uint64, value float32) error {
	if id == a.state.SessionID() {
		return errors.New("cannot change local participant playback volume")
	}
	found := false
	for _, participant := range a.state.Snapshot().Participants {
		if participant.SessionID == id {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("participant %d not found", id)
	}
	return a.state.Audio.SetParticipantVolume(id, value)
}

func (a *App) SetRNNoiseEnabled(value bool) { a.state.Audio.SetRNNoiseEnabled(value) }

func (a *App) SetRNNoiseSensitivity(value float32) error {
	return a.state.Audio.SetRNNoiseSensitivity(value)
}

func (a *App) SetMicrophoneGain(value float32) error {
	return a.state.Audio.SetMicrophoneGain(value)
}

func (a *App) SetVADEnabled(value bool) { a.state.Audio.SetVADEnabled(value) }

func (a *App) SetVADMode(value string) error {
	mode, err := voicegate.ParseMode(value)
	if err != nil {
		return err
	}
	return a.state.Audio.SetVADMode(mode)
}

func (a *App) SetVADSensitivity(value float32) error {
	return a.state.Audio.SetVADSensitivity(value)
}

func (a *App) SetVoiceReserveKiB(value int) error {
	if value < 1 || value > 1048576 {
		return errors.New("voice reserve must be 1..1048576 KiB/s")
	}
	a.state.SetVoiceReserve(value * 1024)
	return nil
}

func (a *App) VoiceReserveKiB() int { return a.state.VoiceReserve() / 1024 }
