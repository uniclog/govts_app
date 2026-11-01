package client

import (
	"testing"

	"uniclog.io/sonoryx/internal/domain"
)

func volumeTestState(saved map[int64]map[int64]float32) *State {
	s := NewState(1, "alice")
	s.SetSavedParticipantVolumes(saved)
	s.ReplaceSnapshot(domain.ServerSnapshot{Revision: 10, Info: domain.ServerInfo{Name: "server"},
		Channels:     []domain.Channel{{ID: 1, Name: "main", Type: domain.ChannelTypePermanent, Audio: domain.DefaultAudioProfile()}},
		Participants: []domain.Participant{{SessionID: 1, UserID: 10, DisplayName: "alice", ChannelID: 1}, {SessionID: 2, UserID: 20, DisplayName: "bob", ChannelID: 1}}})
	s.SetChannelID(1)
	return s
}

func TestSavedVolumeAppliesFromSnapshot(t *testing.T) {
	s := volumeTestState(map[int64]map[int64]float32{10: {20: 0}})
	if got := s.Audio.ParticipantVolume(2); got != 0 {
		t.Fatalf("volume = %v, want 0", got)
	}
	if !s.ParticipantVolumeReady(2) {
		t.Fatal("known participant must be ready")
	}
}

func TestSavedVolumeAppliesInTheJoinEvent(t *testing.T) {
	s := volumeTestState(map[int64]map[int64]float32{10: {30: 0}})
	if s.ParticipantVolumeReady(3) {
		t.Fatal("voice from an unknown sender must wait for its account")
	}
	join := domain.StateEvent{Kind: domain.ParticipantJoined, Revision: 11,
		Participant: domain.Participant{SessionID: 3, UserID: 30, DisplayName: "carol", ChannelID: 1}}
	if !s.ApplyEvent(s.Generation(), join) {
		t.Fatal("join event not applied")
	}
	if !s.ParticipantVolumeReady(3) {
		t.Fatal("joined participant must be ready")
	}
	if got := s.Audio.ParticipantVolume(3); got != 0 {
		t.Fatalf("volume = %v, want 0 before any UI request", got)
	}
}

func TestSavedVolumeBelongsToTheLocalAccount(t *testing.T) {
	s := volumeTestState(map[int64]map[int64]float32{11: {20: 0}})
	if got := s.Audio.ParticipantVolume(2); got != DefaultParticipantVolume {
		t.Fatalf("volume of another account was applied: %v", got)
	}
}

func TestSavedVolumeDoesNotReplaceManualChangeOnResync(t *testing.T) {
	s := volumeTestState(map[int64]map[int64]float32{10: {20: 0}})
	if err := s.Audio.SetParticipantVolume(2, 2); err != nil {
		t.Fatal(err)
	}
	snapshot := s.Snapshot()
	snapshot.Revision++
	s.ReplaceSnapshot(snapshot)
	if got := s.Audio.ParticipantVolume(2); got != 2 {
		t.Fatalf("resync replaced a manual change: %v", got)
	}
}

func TestSavedVolumeAppliesAgainAfterParticipantRejoins(t *testing.T) {
	s := volumeTestState(map[int64]map[int64]float32{10: {20: 0}})
	left := domain.StateEvent{Kind: domain.ParticipantLeft, Revision: 11, SessionID: 2}
	if !s.ApplyEvent(s.Generation(), left) {
		t.Fatal("leave event not applied")
	}
	join := domain.StateEvent{Kind: domain.ParticipantJoined, Revision: 12,
		Participant: domain.Participant{SessionID: 4, UserID: 20, DisplayName: "bob", ChannelID: 1}}
	if !s.ApplyEvent(s.Generation(), join) {
		t.Fatal("join event not applied")
	}
	if got := s.Audio.ParticipantVolume(4); got != 0 {
		t.Fatalf("volume after rejoin = %v, want 0", got)
	}
}

func TestSavedVolumeAppliesAgainAfterLocalReconnect(t *testing.T) {
	s := volumeTestState(map[int64]map[int64]float32{10: {20: 0}})
	snapshot := s.Snapshot()
	s.InvalidateSession(ConnectionReconnecting)
	if got := s.Audio.ParticipantVolume(2); got != DefaultParticipantVolume {
		t.Fatalf("session volumes were not cleared: %v", got)
	}
	generation, err := s.StartSession(1)
	if err != nil {
		t.Fatal(err)
	}
	if s.ParticipantVolumeReady(2) {
		t.Fatal("voice must wait for the new snapshot")
	}
	if !s.ReplaceSnapshotForGeneration(generation, snapshot) {
		t.Fatal("snapshot not applied")
	}
	if got := s.Audio.ParticipantVolume(2); got != 0 {
		t.Fatalf("volume after reconnect = %v, want 0", got)
	}
}

func TestVoiceIsNotHeldWithoutSavedVolumes(t *testing.T) {
	s := volumeTestState(nil)
	if !s.ParticipantVolumeReady(3) {
		t.Fatal("voice must not wait when nothing is saved")
	}
}

func TestParticipantWithoutUserIDIsNotHeld(t *testing.T) {
	s := volumeTestState(map[int64]map[int64]float32{10: {20: 0}})
	join := domain.StateEvent{Kind: domain.ParticipantJoined, Revision: 11,
		Participant: domain.Participant{SessionID: 3, DisplayName: "guest", ChannelID: 1}}
	if !s.ApplyEvent(s.Generation(), join) {
		t.Fatal("join event not applied")
	}
	if !s.ParticipantVolumeReady(3) {
		t.Fatal("a participant without an account must stay audible")
	}
}
