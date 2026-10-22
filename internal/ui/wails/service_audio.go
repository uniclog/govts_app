package wailsui

import (
	"context"
	"log"
	"strings"
	"time"

	voiceclient "uniclog.io/sonoryx/internal/client"
)

func (s *Service) SetSoundPack(id string) error {
	if err := s.client.SetSoundPack(id); err != nil {
		return err
	}
	return s.saveSettings()
}

func (s *Service) SetSoundVolume(value float32) error {
	if err := s.client.SetSoundVolume(value); err != nil {
		return err
	}
	return s.saveSettings()
}

func (s *Service) SetMuteOnAway(value bool) error {
	s.client.SetMuteOnAway(value)
	return s.saveSettings()
}

func (s *Service) SetDeafenOnAway(value bool) error {
	s.client.SetDeafenOnAway(value)
	return s.saveSettings()
}

func (s *Service) SetSoundCue(id string, enabled bool) error {
	if err := s.client.SetSoundCue(id, enabled); err != nil {
		return err
	}
	return s.saveSettings()
}

func (s *Service) SetAway(away bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return s.client.SetAway(ctx, away)
}

func (s *Service) Wake(sessionID string) error {
	id, err := parseUint64(sessionID, "sessionId", false)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return s.client.Wake(ctx, id)
}

func (s *Service) MessageReceived() { s.client.MessageReceived() }

func (s *Service) PreviewSoundPack() error { return s.client.PreviewSoundPack() }

func (s *Service) StopSoundPreview() { s.client.StopSoundPreview() }

func (s *Service) SetMuted(value bool) {
	s.audioSwitchMu.Lock()
	defer s.audioSwitchMu.Unlock()
	s.applyMuted(value)
}

// toggleMuted flips the microphone as it stands now. The target is read under
// the same lock as a button click, so a stale earlier reading cannot land late.
func (s *Service) toggleMuted() (changed, muted bool) {
	s.audioSwitchMu.Lock()
	defer s.audioSwitchMu.Unlock()
	if !s.client.Snapshot().CaptureAvailable {
		return false, false
	}
	before, _ := s.client.AudioSwitches()
	s.applyMuted(!before)
	after, _ := s.client.AudioSwitches()
	return after != before, after
}

func (s *Service) applyMuted(value bool) {
	before, _ := s.client.AudioSwitches()
	if err := s.client.SetMuted(value); err != nil {
		log.Printf("set muted failed: value=%t error=%v", value, err)
		return
	}
	if before == value {
		return
	}
	if value {
		s.client.PlayCue(voiceclient.NotificationMicMuted)
		return
	}
	s.client.PlayCue(voiceclient.NotificationMicUnmuted)
}

func (s *Service) SetDeafened(value bool) error {
	// The deafen waits out its cue. A second click must wait too, or it
	// reads the old state and is lost.
	s.audioSwitchMu.Lock()
	defer s.audioSwitchMu.Unlock()
	return s.applyDeafened(value)
}

// toggleDeafened flips the speaker as it stands now, under the button lock.
func (s *Service) toggleDeafened() (changed, deafened bool, err error) {
	s.audioSwitchMu.Lock()
	defer s.audioSwitchMu.Unlock()
	_, before := s.client.AudioSwitches()
	if err = s.applyDeafened(!before); err != nil {
		return false, before, err
	}
	_, after := s.client.AudioSwitches()
	return after != before, after, nil
}

func (s *Service) applyDeafened(value bool) error {
	_, before := s.client.AudioSwitches()
	if before != value && value {
		s.client.PlayCue(voiceclient.NotificationSpeakerMuted)
		if lead := s.client.CueLead(voiceclient.NotificationSpeakerMuted); lead > 0 {
			time.Sleep(lead)
		}
	}
	if err := s.client.SetDeafened(value); err != nil {
		return err
	}
	if before != value && !value {
		s.client.PlayCue(voiceclient.NotificationSpeakerUnmuted)
	}
	return s.saveSettings()
}

func (s *Service) SetRNNoiseEnabled(value bool) error {
	s.client.SetRNNoiseEnabled(value)
	return s.saveSettings()
}

func (s *Service) SetRNNoiseSensitivity(value float32) error {
	if err := s.client.SetRNNoiseSensitivity(value); err != nil {
		return err
	}
	return s.saveSettings()
}

func (s *Service) SetMicrophoneGain(value float32) error {
	if err := s.client.SetMicrophoneGain(value); err != nil {
		return err
	}
	return s.saveSettings()
}

func (s *Service) SetVADEnabled(value bool) error {
	s.client.SetVADEnabled(value)
	return s.saveSettings()
}

func (s *Service) SetVADMode(value string) error {
	if err := s.client.SetVADMode(value); err != nil {
		return err
	}
	return s.saveSettings()
}

func (s *Service) SetVADSensitivity(value float32) error {
	if err := s.client.SetVADSensitivity(value); err != nil {
		return err
	}
	return s.saveSettings()
}

// SetMicrophonePreview runs the microphone pipeline for the settings meter
// while disconnected; a connected session already feeds the meter.
func (s *Service) SetMicrophonePreview(enabled bool) { s.client.SetMicrophonePreview(enabled) }

// SetMicrophoneMonitor plays the user's processed microphone back to them; in
// a session it also blocks transmission until it is turned off.
func (s *Service) SetMicrophoneMonitor(enabled bool) error {
	return s.client.SetMicrophoneMonitor(enabled)
}

func (s *Service) AudioDevices() (AudioDevicesDTO, error) {
	devices, err := s.client.AudioDevices()
	if err != nil {
		return AudioDevicesDTO{}, err
	}
	selected := s.client.AudioDeviceSelection()
	return AudioDevicesDTO{Capture: audioDeviceDTOs(devices.Capture), Playback: audioDeviceDTOs(devices.Playback), SelectedCapture: selected.CaptureID, SelectedPlayback: selected.PlaybackID}, nil
}

func (s *Service) SetCaptureDevice(id string) error {
	if err := s.client.SetCaptureDevice(strings.TrimSpace(id)); err != nil {
		return err
	}
	return s.saveSettings()
}

func (s *Service) SetPlaybackDevice(id string) error {
	if err := s.client.SetPlaybackDevice(strings.TrimSpace(id)); err != nil {
		return err
	}
	return s.saveSettings()
}
