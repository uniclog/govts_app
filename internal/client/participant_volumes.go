package client

import "log"

// SetSavedParticipantVolumes replaces the volumes to apply on the current
// server: local user id, then remote user id, then volume.
func (s *State) SetSavedParticipantVolumes(saved map[int64]map[int64]float32) {
	clone := make(map[int64]map[int64]float32, len(saved))
	for local, volumes := range saved {
		remote := make(map[int64]float32, len(volumes))
		for user, value := range volumes {
			remote[user] = value
		}
		clone[local] = remote
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.savedVolumes = clone
	s.applySavedVolumesLocked()
}

// applySavedVolumesLocked runs whenever the snapshot changes, once per
// participant session, so a later resync cannot replace a value the user set.
func (s *State) applySavedVolumesLocked() {
	var local int64
	for _, participant := range s.snapshot.Participants {
		if participant.SessionID == s.sessionID {
			local = participant.UserID
			break
		}
	}
	if local == 0 {
		return
	}
	for _, participant := range s.snapshot.Participants {
		if participant.SessionID == s.sessionID {
			continue
		}
		if _, done := s.volumeResolved[participant.SessionID]; done {
			continue
		}
		s.volumeResolved[participant.SessionID] = struct{}{}
		value, ok := s.savedVolumes[local][participant.UserID]
		if !ok {
			continue
		}
		if err := s.Audio.SetParticipantVolume(participant.SessionID, value); err != nil {
			log.Printf("restore participant volume failed: session=%d error=%v", participant.SessionID, err)
		}
	}
}

// ParticipantVolumeReady reports whether a sender's voice may be played. Voice
// state and server state arrive separately, so voice can precede the event that
// names the sender. Holding it keeps a muted participant silent from the first frame.
func (s *State) ParticipantVolumeReady(sender uint64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.savedVolumes) == 0 {
		return true
	}
	_, ready := s.volumeResolved[sender]
	return ready
}
