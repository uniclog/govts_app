package wailsui

import (
	"log"
	"strconv"
	"time"

	"uniclog.io/sonoryx/internal/client"
)

// A slider commits several values per second, so the settings file is written
// once the value settles.
const participantVolumeSaveDelay = 400 * time.Millisecond

// participantVolumeAccount names the local account on the current server.
// settingsMu must be held. Empty means the account is not known yet.
func (s *Service) participantVolumeAccount(userID string) string {
	if userID == "" || userID == "0" {
		return ""
	}
	return mediaTrustServerKey(s.serverAddress) + "|" + userID
}

// resetAppliedVolumes starts a new restore pass when the local session changes.
// settingsMu must be held.
func (s *Service) resetAppliedVolumes(account, sessionID string) {
	context := account + "|" + sessionID
	if s.appliedVolumeContext != context || s.appliedVolumes == nil {
		s.appliedVolumeContext = context
		s.appliedVolumes = make(map[string]struct{})
	}
}

type participantVolumeRestore struct {
	sessionID uint64
	volume    float32
}

// pendingParticipantVolumes returns saved values not applied yet, once per
// participant session, because the client clears session volumes on reconnect.
func (s *Service) pendingParticipantVolumes(view ClientViewDTO) []participantVolumeRestore {
	if view.ConnectionStatus != "connected" || !view.SnapshotFresh {
		return nil
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	account := s.participantVolumeAccount(view.UserID)
	if !s.rememberParticipantVolumes || account == "" {
		return nil
	}
	s.resetAppliedVolumes(account, view.SessionID)
	saved := s.participantVolumes[account]
	var pending []participantVolumeRestore
	for _, participant := range view.Participants {
		if participant.Local || participant.UserID == "" || participant.UserID == "0" {
			continue
		}
		if _, done := s.appliedVolumes[participant.SessionID]; done {
			continue
		}
		s.appliedVolumes[participant.SessionID] = struct{}{}
		volume, ok := saved[participant.UserID]
		if !ok {
			continue
		}
		id, err := strconv.ParseUint(participant.SessionID, 10, 64)
		if err != nil {
			continue
		}
		pending = append(pending, participantVolumeRestore{sessionID: id, volume: volume})
	}
	return pending
}

func (s *Service) restoreParticipantVolumes(view ClientViewDTO) {
	for _, item := range s.pendingParticipantVolumes(view) {
		if err := s.client.SetParticipantVolume(item.sessionID, item.volume); err != nil {
			log.Printf("restore participant volume failed: session=%d error=%v", item.sessionID, err)
		}
	}
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
	s.recordParticipantVolume(view.UserID, view.SessionID, remoteUser, sessionID, value)
}

// recordParticipantVolume keeps the value from being replaced by a restore in
// this session and stores it. The default volume is not stored.
func (s *Service) recordParticipantVolume(localUser int64, localSession uint64, remoteUser int64, sessionID uint64, value float32) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	account := s.participantVolumeAccount(strconv.FormatInt(localUser, 10))
	if account == "" || remoteUser == 0 {
		return
	}
	s.resetAppliedVolumes(account, formatUint64(localSession))
	s.appliedVolumes[formatUint64(sessionID)] = struct{}{}
	userID := strconv.FormatInt(remoteUser, 10)
	if !s.rememberParticipantVolumes {
		return
	}
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
	s.settingsMu.Unlock()
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
