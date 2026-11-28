package voice

import (
	"context"
	"errors"
	"strings"
	"testing"

	"uniclog.io/sonoryx/apps/server/internal/persist"
	"uniclog.io/sonoryx/internal/domain"
)

func addTestChannel(t *testing.T, hub *Hub, channel domain.Channel) domain.Channel {
	t.Helper()
	prepared, err := hub.PrepareChannel(channel)
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.AddChannel(prepared); err != nil {
		t.Fatal(err)
	}
	return prepared
}

func lastEvent(t *testing.T, hub *Hub) domain.StateEvent {
	t.Helper()
	events := hub.takeEvents()
	if len(events) == 0 {
		t.Fatal("no event emitted")
	}
	return events[len(events)-1]
}

func TestPrepareChannelChangesNothingUntilAdded(t *testing.T) {
	hub := NewHub()
	revision := hub.Revision()
	prepared, err := hub.PrepareChannel(domain.Channel{Name: "room"})
	if err != nil || prepared.ID == 0 || prepared.Type != domain.ChannelTypePermanent || prepared.Audio != domain.DefaultAudioProfile() {
		t.Fatalf("prepared = %+v, %v", prepared, err)
	}
	if _, ok := hub.GetChannel(prepared.ID); ok || hub.Revision() != revision {
		t.Fatal("PrepareChannel changed hub state")
	}
	if err := hub.AddChannel(prepared); err != nil {
		t.Fatal(err)
	}
	if event := lastEvent(t, hub); event.Kind != domain.ChannelUpdated || event.Channel != prepared || event.Revision != hub.Revision() {
		t.Fatalf("created event = %+v", event)
	}
	if err := hub.AddChannel(prepared); err == nil {
		t.Fatal("same channel added twice")
	}
	if _, err := hub.PrepareChannel(domain.Channel{Name: "ROOM"}); !errors.Is(err, ErrChannelNameTaken) {
		t.Fatalf("duplicate sibling name error = %v", err)
	}
	if _, err := hub.PrepareChannel(domain.Channel{Name: "child", ParentID: 999}); !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("missing parent error = %v", err)
	}
	if _, err := hub.PrepareChannel(domain.Channel{ID: 50, Name: "x"}); err == nil {
		t.Fatal("caller-chosen ID accepted")
	}
}

func TestUpdateChannelMovesWithinTreeLimits(t *testing.T) {
	hub := NewHub()
	a := addTestChannel(t, hub, domain.Channel{Name: "a"})
	b := addTestChannel(t, hub, domain.Channel{Name: "b", ParentID: a.ID})
	c := addTestChannel(t, hub, domain.Channel{Name: "c", ParentID: b.ID})
	hub.takeEvents()

	for name, update := range map[string]domain.Channel{
		"into itself":       {ID: a.ID, Name: "a", ParentID: a.ID},
		"into a descendant": {ID: a.ID, Name: "a", ParentID: c.ID},
		"missing parent":    {ID: a.ID, Name: "a", ParentID: 999},
		"missing channel":   {ID: 999, Name: "x"},
		"invalid name":      {ID: a.ID, Name: " a"},
		"taken name":        {ID: b.ID, Name: "Default", ParentID: 0},
		"bad bitrate":       {ID: a.ID, Name: "a", Audio: domain.AudioProfile{Codec: domain.AudioCodecOpus, SampleRate: 48_000, Channels: 1, FrameDurationMS: 20, Bitrate: 1, Application: domain.OpusApplicationVoIP}},
	} {
		if _, err := hub.UpdateChannel(update); err == nil {
			t.Errorf("%s: update accepted", name)
		}
	}

	// A chain of six levels below the root leaves room for subtrees of two.
	parent := domain.ChannelID(0)
	var chain []domain.Channel
	for i := range 6 {
		parent = addTestChannel(t, hub, domain.Channel{Name: "level" + strings.Repeat("x", i), ParentID: parent}).ID
		chain = append(chain, domain.Channel{ID: parent})
	}
	hub.takeEvents()
	if _, err := hub.UpdateChannel(domain.Channel{ID: a.ID, Name: "a", ParentID: chain[5].ID}); err == nil {
		t.Fatal("move past the depth limit accepted")
	}
	if _, err := hub.UpdateChannel(domain.Channel{ID: b.ID, Name: "b", ParentID: chain[5].ID, Position: 4}); err != nil {
		t.Fatalf("move of a two-level subtree to depth 7: %v", err)
	}
	moved, _ := hub.GetChannel(b.ID)
	if moved.ParentID != chain[5].ID || moved.Position != 4 || moved.Audio != domain.DefaultAudioProfile() {
		t.Fatalf("moved channel = %+v", moved)
	}
	if event := lastEvent(t, hub); event.Kind != domain.ChannelUpdated || event.Channel != moved {
		t.Fatalf("move event = %+v", event)
	}
	if child, _ := hub.GetChannel(c.ID); child.ParentID != b.ID {
		t.Fatalf("child left its parent: %+v", child)
	}

	revision := hub.Revision()
	if removed, err := hub.UpdateChannel(moved); err != nil || removed != nil || hub.Revision() != revision {
		t.Fatalf("no-op update = %v, %v, revision %d → %d", removed, err, revision, hub.Revision())
	}
}

func TestUpdateChannelKeepsDefaultOpenAndRemovesMembersBelowLevel(t *testing.T) {
	hub := NewHub()
	if _, err := hub.UpdateChannel(domain.Channel{ID: DefaultChannelID, Name: "default", MaxUsers: 5}); err == nil {
		t.Fatal("user limit on the default channel accepted")
	}
	if _, err := hub.UpdateChannel(domain.Channel{ID: DefaultChannelID, Name: "lobby", Topic: "welcome"}); err != nil {
		t.Fatalf("rename of the default channel: %v", err)
	}
	room := addTestChannel(t, hub, domain.Channel{Name: "room"})
	low := addSession(t, hub, persist.Account{ID: 1, DisplayName: "low", JoinLevel: 1}, 7001)
	high := addSession(t, hub, persist.Account{ID: 2, DisplayName: "high", JoinLevel: 9}, 7002)
	for _, session := range []Session{low, high} {
		if err := hub.JoinChannel(session.ID, room.ID); err != nil {
			t.Fatal(err)
		}
	}
	hub.takeEvents()
	room.MinJoinLevel = 5
	removed, err := hub.UpdateChannel(room)
	if err != nil || len(removed) != 1 || removed[0] != low.ID {
		t.Fatalf("raised level removed = %v, %v", removed, err)
	}
	events := hub.takeEvents()
	if len(events) != 2 || events[0].Kind != domain.ChannelUpdated || events[1].Kind != domain.ParticipantLeft || events[1].SessionID != low.ID {
		t.Fatalf("events = %+v", events)
	}
	if _, ok := hub.Get(high.ID); !ok {
		t.Fatal("member above the level was removed")
	}

	if _, err := hub.SetChannelJoinLevel(room.ID, 3); err != nil {
		t.Fatal(err)
	}
	if event := lastEvent(t, hub); event.Kind != domain.ChannelUpdated || event.Channel.MinJoinLevel != 3 {
		t.Fatalf("SetChannelJoinLevel event = %+v", event)
	}
}

func TestAdministrationChannelRights(t *testing.T) {
	f := newAdminFixture(t)
	ctx := context.Background()
	if err := f.store.SaveInitialServer(ctx, persist.ServerState{Name: "server", DefaultChannelID: DefaultChannelID, Channels: f.hub.ListChannels()}); err != nil {
		t.Fatal(err)
	}
	actor := actorOf(f.actor) // level 10

	if _, err := f.admin.CreateChannel(ctx, actor, domain.Channel{Name: "vip", MinJoinLevel: 11}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("create above own level: %v", err)
	}
	created, err := f.admin.CreateChannel(ctx, actor, domain.Channel{Name: "team", MinJoinLevel: 10, Topic: "work"})
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := f.store.LoadServer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var stored domain.Channel
	for _, channel := range state.Channels {
		if channel.ID == created.ID {
			stored = channel
		}
	}
	if stored != created {
		t.Fatalf("stored channel = %+v, want %+v", stored, created)
	}

	locked, err := f.admin.CreateChannel(ctx, actorOf(f.owner), domain.Channel{Name: "locked", MinJoinLevel: 50})
	if err != nil {
		t.Fatal(err)
	}
	locked.Name = "renamed"
	if _, _, err := f.admin.UpdateChannel(ctx, actor, locked); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("update of a channel above own level: %v", err)
	}
	created.ParentID = locked.ID
	created.Position = 2
	if _, _, err := f.admin.UpdateChannel(ctx, actor, created); err != nil {
		t.Fatalf("move under another channel: %v", err)
	}
	if _, _, err := f.admin.UpdateChannel(ctx, actorOf(f.user), created); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("update by a non-administrator: %v", err)
	}
	if _, _, err := f.admin.UpdateChannel(ctx, ConsoleAdmin, domain.Channel{ID: DefaultChannelID, Name: "default", MinJoinLevel: 1}); err == nil {
		t.Fatal("level on the default channel accepted")
	}
	entries, err := f.store.AuditPage(ctx, 0, 3)
	if err != nil || len(entries) != 3 || entries[0].Action != "update-channel" || entries[0].Actor != "2" ||
		entries[1].Action != "create-channel" || entries[1].Actor != "1" || entries[2].Action != "create-channel" {
		t.Fatalf("audit = %+v, %v", entries, err)
	}
}
