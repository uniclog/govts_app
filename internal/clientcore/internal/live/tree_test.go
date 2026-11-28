package client

import (
	"testing"

	"uniclog.io/sonoryx/internal/domain"
)

func TestChannelTopologyIgnoresRuntimeIDChanges(t *testing.T) {
	oldSnapshot := domain.ServerSnapshot{Channels: []domain.Channel{{ID: 2, Name: "main"}, {ID: 3, ParentID: 2, Name: "gaming"}}}
	newSnapshot := domain.ServerSnapshot{Channels: []domain.Channel{{ID: 20, Name: "main"}, {ID: 30, ParentID: 20, Name: "gaming"}}}
	if !SameChannelTopology(oldSnapshot, newSnapshot) {
		t.Fatal("runtime ID change altered topology")
	}
}

func TestStateRejectsPublicationFromOldGeneration(t *testing.T) {
	state := NewState(1, "alice")
	generation := state.Generation()
	state.InvalidateSession(ConnectionReconnecting)
	if state.ReplaceSnapshotForGeneration(generation, domain.ServerSnapshot{Revision: 2}) {
		t.Fatal("old generation published snapshot")
	}
	if state.SnapshotFresh() {
		t.Fatal("stale snapshot marked fresh")
	}
}
