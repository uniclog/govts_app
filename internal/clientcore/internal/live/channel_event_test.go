package client

import (
	"testing"

	"uniclog.io/sonoryx/internal/domain"
)

func TestChannelUpdatedInsertsReplacesAndKeepsCanonicalOrder(t *testing.T) {
	s := liveTestState()
	gen := s.Generation()
	audio := domain.DefaultAudioProfile()
	created := domain.Channel{ID: 3, ParentID: 1, Name: "sub", Position: 0, Type: domain.ChannelTypePermanent, Audio: audio}
	if !s.ApplyEvent(gen, domain.StateEvent{Kind: domain.ChannelUpdated, Revision: 11, Channel: created}) {
		t.Fatal("created channel not applied")
	}
	if channels := s.Snapshot().Channels; len(channels) != 3 || channels[2] != created {
		t.Fatalf("channels after insert = %+v", channels)
	}

	// Moving "main" behind "other" must re-sort the list, or the next
	// snapshot validation would reject it.
	moved := domain.Channel{ID: 1, Name: "main", Position: 5, MaxUsers: 3, Topic: "t", Type: domain.ChannelTypePermanent, Audio: audio}
	moved.Audio.Bitrate = 64_000
	if !s.ApplyEvent(gen, domain.StateEvent{Kind: domain.ChannelUpdated, Revision: 12, Channel: moved}) {
		t.Fatal("updated channel not applied")
	}
	channels := s.Snapshot().Channels
	if len(channels) != 3 || channels[0].ID != 2 || channels[1] != moved || channels[2].ID != 3 {
		t.Fatalf("channels after update = %+v", channels)
	}
	if err := validateServerSnapshot(s.Snapshot()); err != nil {
		t.Fatalf("snapshot after events: %v", err)
	}
}

func TestChannelDeletedRemovesEmptiedChannel(t *testing.T) {
	s := liveTestState()
	gen := s.Generation()
	// The server moves members out before the deletion.
	if !s.ApplyEvent(gen, domain.StateEvent{Kind: domain.ParticipantMoved, Revision: 11, SessionID: 2, ChannelID: 2}) {
		t.Fatal("move not applied")
	}
	if !s.ApplyEvent(gen, domain.StateEvent{Kind: domain.ParticipantMoved, Revision: 12, SessionID: 2, ChannelID: 1}) {
		t.Fatal("move back not applied")
	}
	if !s.ApplyEvent(gen, domain.StateEvent{Kind: domain.ChannelDeleted, Revision: 13, ChannelID: 2}) {
		t.Fatal("channel deletion not applied")
	}
	snapshot := s.Snapshot()
	if len(snapshot.Channels) != 1 || snapshot.Channels[0].ID != 1 || snapshot.Revision != 13 {
		t.Fatalf("snapshot after deletion = %+v", snapshot)
	}
	if !s.SnapshotFresh() {
		t.Fatal("deletion requested a resync")
	}
}

func TestChannelDeletedResyncsOnUnknownOrOccupiedChannel(t *testing.T) {
	s := liveTestState()
	if s.ApplyEvent(s.Generation(), domain.StateEvent{Kind: domain.ChannelDeleted, Revision: 11, ChannelID: 9}) {
		t.Fatal("unknown channel deletion applied")
	}
	if s.SnapshotFresh() {
		t.Fatal("unknown channel deletion did not request a resync")
	}

	s = liveTestState()
	if s.ApplyEvent(s.Generation(), domain.StateEvent{Kind: domain.ChannelDeleted, Revision: 11, ChannelID: 1}) {
		t.Fatal("deletion of an occupied channel applied")
	}
	if s.SnapshotFresh() || len(s.Snapshot().Channels) != 2 {
		t.Fatal("occupied channel deletion changed state or skipped the resync")
	}
}

func TestChannelUpdatedRejectsCyclesWithResync(t *testing.T) {
	s := liveTestState()
	gen := s.Generation()
	audio := domain.DefaultAudioProfile()
	if !s.ApplyEvent(gen, domain.StateEvent{Kind: domain.ChannelUpdated, Revision: 11, Channel: domain.Channel{ID: 2, ParentID: 1, Name: "other", Type: domain.ChannelTypePermanent, Audio: audio}}) {
		t.Fatal("child channel not applied")
	}
	cycle := domain.StateEvent{Kind: domain.ChannelUpdated, Revision: 12, Channel: domain.Channel{ID: 1, ParentID: 2, Name: "main", Type: domain.ChannelTypePermanent, Audio: audio}}
	if s.ApplyEvent(gen, cycle) {
		t.Fatal("cyclic channel tree applied")
	}
	if s.SnapshotFresh() {
		t.Fatal("cyclic update did not request a resync")
	}
}
