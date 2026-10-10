package client

import (
	"bytes"
	"strings"
	"testing"

	"uniclog.io/sonoryx/internal/domain"
)

func TestChannelLocatorSurvivesRuntimeIDChanges(t *testing.T) {
	oldSnapshot := domain.ServerSnapshot{Channels: []domain.Channel{{ID: 2, Name: "main"}, {ID: 3, ParentID: 2, Name: "gaming"}}}
	locator, err := BuildChannelLocator(oldSnapshot, 3)
	if err != nil {
		t.Fatal(err)
	}
	newSnapshot := domain.ServerSnapshot{Channels: []domain.Channel{{ID: 20, Name: "main"}, {ID: 30, ParentID: 20, Name: "gaming"}}}
	id, err := ResolveChannelLocator(newSnapshot, locator)
	if err != nil || id != 30 {
		t.Fatalf("resolved = (%d, %v)", id, err)
	}
	if !SameChannelTopology(oldSnapshot, newSnapshot) {
		t.Fatal("runtime ID change altered topology")
	}
}

func TestRenderServerTree(t *testing.T) {
	snapshot := domain.ServerSnapshot{Revision: 7, Info: domain.ServerInfo{Name: "Сервер"}, Channels: []domain.Channel{{ID: 2, ParentID: 1, Name: "игровой", MaxUsers: 4}, {ID: 3, Name: "afk", Position: 2}, {ID: 1, Name: "main", MaxUsers: 0, Position: 1}}, Participants: []domain.Participant{{SessionID: 10, DisplayName: "Алиса", ChannelID: 2}, {SessionID: 20, DisplayName: "bob"}}}
	var output bytes.Buffer
	if err := RenderServerTree(&output, snapshot, 10, 2, false); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Сервер  revision=7  users=2", "main [id=1] users=0/unlimited", "игровой [id=2] users=1/4  <- current", "Алиса [session=10]  <- you", "Unjoined", "bob [session=20]"} {
		if !strings.Contains(output.String(), text) {
			t.Fatalf("output missing %q:\n%s", text, output.String())
		}
	}
	if !strings.Contains(output.String(), "├─") || !strings.Contains(output.String(), "└─") || !strings.Contains(output.String(), "•") {
		t.Fatalf("tree connectors missing:\n%s", output.String())
	}
}

func TestRenderServerTreeRejectsUnknownParticipantChannel(t *testing.T) {
	snapshot := domain.ServerSnapshot{Participants: []domain.Participant{{SessionID: 10, ChannelID: 99}}}
	var output bytes.Buffer
	if err := RenderServerTree(&output, snapshot, 0, 0, false); err == nil {
		t.Fatal("renderer accepted participant with unknown channel")
	}
	if output.Len() != 0 {
		t.Fatalf("renderer wrote partial output: %q", output.String())
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

func TestRenderersEscapeUntrustedNames(t *testing.T) {
	unsafe := "Алиса\x1b[2J\r\n\t\u009b\u2028\u202e"
	snapshot := domain.ServerSnapshot{
		Info:     domain.ServerInfo{Name: unsafe},
		Channels: []domain.Channel{{ID: 1, Name: unsafe}},
		Participants: []domain.Participant{
			{SessionID: 1, ChannelID: 1, DisplayName: unsafe},
			{SessionID: 2, DisplayName: unsafe},
		},
	}
	for _, tree := range []bool{true, false} {
		var output bytes.Buffer
		var err error
		if tree {
			err = RenderServerTree(&output, snapshot, 1, 1, false)
		} else {
			err = RenderChannelMembers(&output, snapshot, 1, 1)
		}
		if err != nil {
			t.Fatal(err)
		}
		text := output.String()
		if strings.ContainsAny(text, "\x1b\r\t\u009b\u2028\u202e") || strings.Contains(text, "[2J\n") {
			t.Fatalf("unsafe terminal output: %q", text)
		}
		if !strings.Contains(text, `Алиса\x1b[2J\r\n\t\u009b\u2028\u202e`) {
			t.Fatalf("missing escaped name: %q", text)
		}
	}
}
