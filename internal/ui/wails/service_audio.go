package wailsui

import "strings"

func (s *Service) SetMuted(value bool) { _ = s.client.SetMuted(value) }

func (s *Service) SetDeafened(value bool) error {
	if err := s.client.SetDeafened(value); err != nil {
		return err
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
