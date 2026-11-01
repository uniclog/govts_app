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

// restoreParticipantVolumes applies saved values once per participant session,
// because the client clears session volumes on every reconnect.
func (s *Service) restoreParticipantVolumes(view ClientViewDTO) {
	if view.ConnectionStatus != "connected" || !view.SnapshotFresh {
		return
	}
	type restore struct {
		sessionID uint64
		volume    float32
	}
	var pending []restore
	s.settingsMu.Lock()
	account := s.participantVolumeAccount(view.UserID)
	if !s.rememberParticipantVolumes || account == "" {
		s.settingsMu.Unlock()
		return
	}
	context := account + "|" + view.SessionID
	if s.appliedVolumeContext != context || s.appliedVolumes == nil {
		s.appliedVolumeContext = context
		s.appliedVolumes = make(map[string]struct{})
	}
	saved := s.participantVolumes[account]
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
		pending = append(pending, restore{sessionID: id, volume: volume})
	}
	s.settingsMu.Unlock()
	for _, item := range pending {
		if err := s.client.SetParticipantVolume(item.sessionID, item.volume); err != nil {
			log.Printf("restore participant volume failed: session=%d error=%v", item.sessionID, err)
		}
	}
}

// saveParticipantVolume remembers a value set by the user for the participant's
// account. The default volume is not stored.
func (s *Service) saveParticipantVolume(sessionID uint64, value float32) {
	view := viewDTO(s.client.Snapshot(), s.client.LastError())
	session := formatUint64(sessionID)
	userID := ""
	for _, participant := range view.Participants {
		if participant.SessionID == session {
			userID = participant.UserID
			break
		}
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	account := s.participantVolumeAccount(view.UserID)
	if account == "" || userID == "" || userID == "0" {
		return
	}
	if s.appliedVolumeContext != account+"|"+view.SessionID || s.appliedVolumes == nil {
		s.appliedVolumeContext = account + "|" + view.SessionID
		s.appliedVolumes = make(map[string]struct{})
	}
	s.appliedVolumes[session] = struct{}{}
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
func (s *Service) FlushParticipantVolumes() {
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
