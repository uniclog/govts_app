package wailsui

import (
	"strconv"
	"strings"
	"time"

	"uniclog.io/sonoryx/internal/client"
)

// A slider commits several values per second, so the settings file is written
// once the value settles.
const participantVolumeSaveDelay = 400 * time.Millisecond

// savedVolumesForServer returns the values the client applies on this server,
// by local user id and remote user id. It is empty while remembering is off.
func (s *Service) savedVolumesForServer(server string) map[int64]map[int64]float32 {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	result := make(map[int64]map[int64]float32)
	if !s.rememberParticipantVolumes {
		return result
	}
	prefix := mediaTrustServerKey(server) + "|"
	for account, volumes := range s.participantVolumes {
		if !strings.HasPrefix(account, prefix) {
			continue
		}
		local, err := strconv.ParseInt(strings.TrimPrefix(account, prefix), 10, 64)
		if err != nil {
			continue
		}
		remote := make(map[int64]float32, len(volumes))
		for userID, value := range volumes {
			if id, err := strconv.ParseInt(userID, 10, 64); err == nil {
				remote[id] = value
			}
		}
		result[local] = remote
	}
	return result
}

func (s *Service) pushParticipantVolumes(server string) {
	s.client.SetSavedParticipantVolumes(s.savedVolumesForServer(server))
}

// saveParticipantVolume remembers a value set by the user for the participant's
// account.
func (s *Service) saveParticipantVolume(sessionID uint64, value float32) {
	view := s.client.Snapshot()
	var remoteUser int64
	for _, participant := range view.Participants {
		if participant.SessionID == sessionID {
			remoteUser = participant.UserID
			break
		}
	}
	s.recordParticipantVolume(view.UserID, remoteUser, value)
	s.settingsMu.RLock()
	server := s.serverAddress
	s.settingsMu.RUnlock()
	s.pushParticipantVolumes(server)
}

// recordParticipantVolume stores a value under "server|local user id". The
// default volume is not stored.
func (s *Service) recordParticipantVolume(localUser, remoteUser int64, value float32) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	if !s.rememberParticipantVolumes || localUser == 0 || remoteUser == 0 {
		return
	}
	account := mediaTrustServerKey(s.serverAddress) + "|" + strconv.FormatInt(localUser, 10)
	userID := strconv.FormatInt(remoteUser, 10)
	if s.participantVolumes == nil {
		s.participantVolumes = make(map[string]map[string]float32)
	}
	saved := s.participantVolumes[account]
	if value == client.DefaultParticipantVolume {
		delete(saved, userID)
		if len(saved) == 0 {
			delete(s.participantVolumes, account)
		}
	} else {
		if saved == nil {
			saved = make(map[string]float32)
			s.participantVolumes[account] = saved
		}
		saved[userID] = value
	}
	if s.participantVolumeSave != nil {
		s.participantVolumeSave.Stop()
	}
	s.participantVolumeSave = time.AfterFunc(participantVolumeSaveDelay, func() { _ = s.saveSettings() })
}

// FlushParticipantVolumes writes a value still waiting for its delayed save.
// It is a function so that Wails does not expose it to the frontend.
func FlushParticipantVolumes(s *Service) {
	s.settingsMu.Lock()
	pending := s.participantVolumeSave != nil && s.participantVolumeSave.Stop()
	s.settingsMu.Unlock()
	if pending {
		_ = s.saveSettings()
	}
}

func (s *Service) RememberParticipantVolumes() bool {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return s.rememberParticipantVolumes
}

func (s *Service) SetRememberParticipantVolumes(value bool) error {
	s.settingsMu.Lock()
	s.rememberParticipantVolumes = value
	server := s.serverAddress
	s.settingsMu.Unlock()
	s.pushParticipantVolumes(server)
	return s.saveSettings()
}

func cloneParticipantVolumes(source map[string]map[string]float32) map[string]map[string]float32 {
	result := make(map[string]map[string]float32, len(source))
	for account, volumes := range source {
		clone := make(map[string]float32, len(volumes))
		for userID, value := range volumes {
			clone[userID] = value
		}
		result[account] = clone
	}
	return result
}
