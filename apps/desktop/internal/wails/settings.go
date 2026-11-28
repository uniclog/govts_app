package wailsui

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
	"uniclog.io/sonoryx/internal/soundprefs"

	"uniclog.io/sonoryx/apps/desktop/internal/nativeaudio"
	"uniclog.io/sonoryx/apps/desktop/internal/settings"
)

type preferences struct {
	settings                   *clientsettings.Store
	settingsMu                 sync.RWMutex
	settingsSaveMu             sync.Mutex // Orders snapshots as well as writes.
	displayName                string
	trustedMediaKeys           map[string]string
	theme                      string
	locale                     string
	closeToTray                bool
	rememberParticipantVolumes bool
	participantVolumes         map[string]map[string]float32
	participantVolumeSave      *time.Timer
}

func (s *Service) enableSettings(path string) error {
	store := clientsettings.NewStore(path)
	s.settings = store
	s.settingsMu.Lock()
	s.rememberParticipantVolumes = true
	s.settingsMu.Unlock()
	settings, err := store.Load()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	s.settingsMu.Lock()
	s.displayName = settings.DisplayName
	s.theme = clientsettings.NormalizeTheme(settings.Theme)
	if locale, ok := clientsettings.NormalizeLocale(settings.Locale); ok {
		s.locale = locale
	} else {
		s.locale = "ru"
	}
	s.closeToTray = settings.CloseToTray
	s.rememberParticipantVolumes = settings.RememberParticipantVolumes
	s.participantVolumes = settings.ParticipantVolumes
	s.recentServers = settings.RecentServers
	s.hotkeyKeys = clientsettings.NormalizeHotkeys(settings.Hotkeys)
	s.hotkeyNotifications = settings.HotkeyNotifications
	s.hotkeysEnabled = settings.HotkeysEnabled
	s.trustedMediaKeys = settings.TrustedMediaKeys
	if s.trustedMediaKeys == nil {
		s.trustedMediaKeys = make(map[string]string)
	}
	s.settingsMu.Unlock()
	var restoreErrors []error
	if err := s.client.SetVoiceReserveKiB(settings.VoiceUplinkReserveKiB); err != nil {
		restoreErrors = append(restoreErrors, err)
	}
	if err := s.client.SetVADMode(settings.VADMode); err != nil {
		restoreErrors = append(restoreErrors, err)
	}
	if err := s.client.SetVADSensitivity(settings.VADSensitivity); err != nil {
		restoreErrors = append(restoreErrors, err)
	}
	if err := s.client.SetRNNoiseSensitivity(settings.RNNoiseSensitivity); err != nil {
		restoreErrors = append(restoreErrors, err)
	}
	if err := s.client.SetMicrophoneGain(settings.MicrophoneGain); err != nil {
		restoreErrors = append(restoreErrors, err)
	}
	s.client.SetRNNoiseEnabled(settings.RNNoiseEnabled)
	s.client.SetVADEnabled(settings.VADEnabled)
	if err := s.client.SetSoundPack(settings.SoundPack); err != nil {
		restoreErrors = append(restoreErrors, err)
	}
	if err := s.client.SetSoundVolume(settings.SoundVolume); err != nil {
		restoreErrors = append(restoreErrors, err)
	}
	s.client.SetMuteOnAway(settings.MuteOnAway)
	s.client.SetDeafenOnAway(settings.DeafenOnAway)
	s.client.SetSoundCues(settings.SoundCues)
	if err := s.client.SetDeafened(settings.Deafened); err != nil {
		restoreErrors = append(restoreErrors, err)
	}
	if s.audio != nil && settings.CaptureDeviceID != "" {
		if err := s.audio.SetCaptureDevice(settings.CaptureDeviceID); err != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("restore capture device: %w", err))
		}
	}
	if s.audio != nil && settings.PlaybackDeviceID != "" {
		if err := s.audio.SetPlaybackDevice(settings.PlaybackDeviceID); err != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("restore playback device: %w", err))
		}
	}
	return errors.Join(restoreErrors...)
}

func (s *Service) saveSettings() error {
	s.settingsSaveMu.Lock()
	defer s.settingsSaveMu.Unlock()
	return s.saveSettingsLocked()
}

func (s *Service) saveSettingsLocked() (saveErr error) {
	defer func() {
		if saveErr != nil {
			log.Printf("save client settings failed: error=%v", saveErr)
		}
	}()
	if s.settings == nil {
		return nil
	}
	view := s.client.Snapshot()
	var devices nativeaudio.DeviceSelection
	if s.audio != nil {
		devices = s.audio.DeviceSelection()
	}
	s.settingsMu.RLock()
	displayName := s.displayName
	trustedMediaKeys := cloneStringMap(s.trustedMediaKeys)
	theme := clientsettings.NormalizeTheme(s.theme)
	locale, localeOK := clientsettings.NormalizeLocale(s.locale)
	if !localeOK {
		locale = "ru"
	}
	closeToTray := s.closeToTray
	recentServers := append([]clientsettings.RecentServer(nil), s.recentServers...)
	hotkeyKeys := cloneStringMap(s.hotkeyKeys)
	hotkeyNotifications := s.hotkeyNotifications
	hotkeysEnabled := s.hotkeysEnabled
	rememberParticipantVolumes := s.rememberParticipantVolumes
	participantVolumes := cloneParticipantVolumes(s.participantVolumes)
	s.settingsMu.RUnlock()
	return s.settings.Save(clientsettings.Settings{
		DisplayName:                displayName,
		VoiceUplinkReserveKiB:      s.client.VoiceReserveKiB(),
		CaptureDeviceID:            devices.CaptureID,
		PlaybackDeviceID:           devices.PlaybackID,
		Deafened:                   s.client.SavedDeafened(),
		RNNoiseEnabled:             view.RNNoiseEnabled,
		RNNoiseSensitivity:         view.RNNoiseSensitivity,
		MicrophoneGain:             view.MicrophoneGain,
		VADEnabled:                 view.VADEnabled,
		VADMode:                    view.VADMode,
		VADSensitivity:             view.VADSensitivity,
		TrustedMediaKeys:           trustedMediaKeys,
		Theme:                      theme,
		Locale:                     locale,
		CloseToTray:                closeToTray,
		SoundPack:                  view.SoundPack,
		SoundVolume:                view.SoundVolume,
		MuteOnAway:                 view.MuteOnAway,
		DeafenOnAway:               view.DeafenOnAway,
		SoundCues:                  soundprefs.NormalizeSoundCues(view.SoundCues),
		RecentServers:              recentServers,
		Hotkeys:                    hotkeyKeys,
		HotkeyNotifications:        hotkeyNotifications,
		HotkeysEnabled:             hotkeysEnabled,
		RememberParticipantVolumes: rememberParticipantVolumes,
		ParticipantVolumes:         participantVolumes,
	})
}

func cloneStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func (s *Service) setDisplayName(value string) error {
	s.settingsMu.Lock()
	s.displayName = strings.TrimSpace(value)
	s.settingsMu.Unlock()
	return s.saveSettings()
}

func EnableDefaultSettings(service *Service) error {
	path, err := clientsettings.DefaultPath()
	if err != nil {
		return err
	}
	return service.enableSettings(path)
}

func (s *Service) SavedDisplayName() string {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return s.displayName
}

func (s *Service) Theme() string {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return clientsettings.NormalizeTheme(s.theme)
}

func (s *Service) SetTheme(value string) error {
	normalized := clientsettings.NormalizeTheme(strings.TrimSpace(value))
	if normalized != value {
		return &validationError{Field: "theme", Message: "неизвестная тема"}
	}
	s.settingsMu.Lock()
	s.theme = normalized
	s.settingsMu.Unlock()
	return s.saveSettings()
}

func (s *Service) Locale() string {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	locale, ok := clientsettings.NormalizeLocale(s.locale)
	if !ok {
		return "ru"
	}
	return locale
}

func (s *Service) SetLocale(value string) error {
	value = strings.TrimSpace(value)
	locale, ok := clientsettings.NormalizeLocale(value)
	if !ok || value == "" {
		return &validationError{Field: "locale", Message: "неизвестный язык"}
	}
	s.settingsMu.Lock()
	s.locale = locale
	s.settingsMu.Unlock()
	return s.saveSettings()
}

func (s *Service) CloseToTray() bool {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return s.closeToTray
}

func (s *Service) SetCloseToTray(value bool) error {
	s.settingsMu.Lock()
	s.closeToTray = value
	s.settingsMu.Unlock()
	return s.saveSettings()
}
