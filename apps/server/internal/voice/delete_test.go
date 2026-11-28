package voice

import (
	"context"
	"errors"
	"testing"

	"uniclog.io/sonoryx/apps/server/internal/persist"
	"uniclog.io/sonoryx/internal/domain"
)

func TestDeleteChannelMovesMembersToDefault(t *testing.T) {
	hub := NewHub()
	room := addTestChannel(t, hub, domain.Channel{Name: "room"})
	sub := addTestChannel(t, hub, domain.Channel{Name: "sub", ParentID: room.ID})
	if _, err := hub.DeleteChannel(DefaultChannelID); !errors.Is(err, ErrInvalidChannel) {
		t.Fatalf("default channel delete error = %v", err)
	}
	if _, err := hub.DeleteChannel(room.ID); !errors.Is(err, ErrInvalidChannel) {
		t.Fatalf("parent channel delete error = %v", err)
	}
	if _, err := hub.DeleteChannel(999); !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("missing channel delete error = %v", err)
	}

	sharer := addSession(t, hub, persist.Account{ID: 1, DisplayName: "sharer"}, 7101)
	member := addSession(t, hub, persist.Account{ID: 2, DisplayName: "member"}, 7102)
	outside := addSession(t, hub, persist.Account{ID: 3, DisplayName: "outside"}, 7103)
	for _, session := range []Session{sharer, member} {
		if err := hub.JoinChannel(session.ID, sub.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := hub.JoinChannel(outside.ID, room.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.StartScreenShare(sharer.ID, 77); err != nil {
		t.Fatal(err)
	}
	hub.takeEvents()
	before := hub.Revision()

	moved, err := hub.DeleteChannel(sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 2 || moved[0] != min(sharer.ID, member.ID) || moved[1] != max(sharer.ID, member.ID) {
		t.Fatalf("moved = %v", moved)
	}
	events := hub.takeEvents()
	if len(events) != 4 {
		t.Fatalf("events = %+v", events)
	}
	var stopped, movedEvents int
	for i, event := range events {
		if event.Revision != before+domain.StateRevision(i+1) {
			t.Fatalf("event %d revision = %d, before %d", i, event.Revision, before)
		}
		switch event.Kind {
		case domain.ScreenStreamStopped:
			stopped++
		case domain.ParticipantMoved:
			if event.ChannelID != DefaultChannelID {
				t.Fatalf("moved to %d", event.ChannelID)
			}
			movedEvents++
		}
	}
	if last := events[len(events)-1]; last.Kind != domain.ChannelDeleted || last.ChannelID != sub.ID || last.Revision != hub.Revision() {
		t.Fatalf("last event = %+v", last)
	}
	if stopped != 1 || movedEvents != 2 {
		t.Fatalf("stopped = %d, moved = %d", stopped, movedEvents)
	}
	for _, id := range moved {
		if session, ok := hub.Get(id); !ok || session.ChannelID != DefaultChannelID {
			t.Fatalf("session %d = %+v, %t", id, session, ok)
		}
	}
	if session, _ := hub.Get(outside.ID); session.ChannelID != room.ID {
		t.Fatalf("outside session moved to %d", session.ChannelID)
	}
	if _, ok := hub.GetChannel(sub.ID); ok {
		t.Fatal("deleted channel still listed")
	}
	if streams := hub.ScreenStreams(); len(streams) != 0 {
		t.Fatalf("screen streams = %+v", streams)
	}
	if err := hub.JoinChannel(member.ID, sub.ID); !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("join deleted channel error = %v", err)
	}

	if moved, err := hub.DeleteChannel(room.ID); err != nil || len(moved) != 1 || moved[0] != outside.ID {
		t.Fatalf("emptied parent delete = %v, %v", moved, err)
	}
}

func TestAdministrationDeleteChannel(t *testing.T) {
	f := newAdminFixture(t)
	ctx := context.Background()
	if err := f.store.SaveInitialServer(ctx, persist.ServerState{Name: "server", DefaultChannelID: DefaultChannelID, Channels: f.hub.ListChannels()}); err != nil {
		t.Fatal(err)
	}
	actor := actorOf(f.actor) // level 10
	locked, err := f.admin.CreateChannel(ctx, actorOf(f.owner), domain.Channel{Name: "locked", MinJoinLevel: 50})
	if err != nil {
		t.Fatal(err)
	}
	team, err := f.admin.CreateChannel(ctx, actor, domain.Channel{Name: "team", MinJoinLevel: 10})
	if err != nil {
		t.Fatal(err)
	}
	child, err := f.admin.CreateChannel(ctx, actor, domain.Channel{Name: "child", ParentID: team.ID})
	if err != nil {
		t.Fatal(err)
	}

	if err := f.admin.DeleteChannel(ctx, actor, locked.ID); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("delete above own level: %v", err)
	}
	if err := f.admin.DeleteChannel(ctx, actorOf(f.user), child.ID); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("delete by a non-administrator: %v", err)
	}
	if err := f.admin.DeleteChannel(ctx, ConsoleAdmin, DefaultChannelID); err == nil {
		t.Fatal("default channel deleted")
	}
	if err := f.admin.DeleteChannel(ctx, actor, team.ID); err == nil {
		t.Fatal("channel with subchannels deleted")
	}
	if err := f.admin.DeleteChannel(ctx, actor, child.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.admin.DeleteChannel(ctx, actor, team.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.admin.DeleteChannel(ctx, actorOf(f.owner), locked.ID); err != nil {
		t.Fatal(err)
	}
	state, _, err := f.store.LoadServer(ctx)
	if err != nil || len(state.Channels) != 1 || state.Channels[0].ID != DefaultChannelID {
		t.Fatalf("stored channels = %+v, %v", state.Channels, err)
	}
	if channels := f.hub.ListChannels(); len(channels) != 1 {
		t.Fatalf("hub channels = %+v", channels)
	}
	entries, err := f.store.AuditPage(ctx, 0, 3)
	if err != nil || len(entries) != 3 || entries[0].Actor != "1" || entries[1].Actor != "2" || entries[2].Actor != "2" {
		t.Fatalf("audit = %+v, %v", entries, err)
	}
	for _, entry := range entries {
		if entry.Action != "delete-channel" {
			t.Fatalf("audit entry = %+v", entry)
		}
	}
}

func TestAdministrationDeleteAccount(t *testing.T) {
	f := newAdminFixture(t)
	ctx := context.Background()
	actor := actorOf(f.actor) // administrator with kick, level 10
	if _, err := f.admin.DeleteAccount(ctx, actor, f.user.ID); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("delete without the ban right: %v", err)
	}
	actor.Permissions |= persist.PermissionBan
	for name, id := range map[string]int64{"owner": f.owner.ID, "self": f.actor.ID, "administrator": f.peer.ID, "higher level": f.senior.ID} {
		if _, err := f.admin.DeleteAccount(ctx, actor, id); !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("delete %s: %v", name, err)
		}
	}
	if _, err := f.admin.DeleteAccount(ctx, actorOf(f.user), f.senior.ID); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("delete by a non-administrator: %v", err)
	}

	first := addSession(t, f.hub, f.user, 6101)
	second := addSession(t, f.hub, f.user, 6102)
	bystander := addSession(t, f.hub, f.senior, 6103)
	change, err := f.admin.DeleteAccount(ctx, actor, f.user.ID)
	if err != nil || len(change.Removed) != 2 || change.Removed[0] != min(first.ID, second.ID) {
		t.Fatalf("delete = %+v, %v", change, err)
	}
	if _, ok := f.hub.Get(first.ID); ok {
		t.Fatal("session of the deleted account survived")
	}
	if _, ok := f.hub.Get(bystander.ID); !ok {
		t.Fatal("delete removed another account's session")
	}
	if _, err := f.store.Account(ctx, f.user.ID); err == nil {
		t.Fatal("deleted account still readable")
	}
	if _, err := f.admin.DeleteAccount(ctx, actor, f.user.ID); err == nil {
		t.Fatal("account deleted twice")
	}
	if _, err := f.admin.SetJoinLevel(ctx, ConsoleAdmin, f.user.ID, 1); err == nil {
		t.Fatal("deleted account changed")
	}

	if _, err := f.admin.DeleteAccount(ctx, actorOf(f.owner), f.peer.ID); err != nil {
		t.Fatalf("owner deletes an administrator: %v", err)
	}
	if _, err := f.admin.SetBan(ctx, ConsoleAdmin, f.senior.ID, true, "spam"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.DeleteAccount(ctx, ConsoleAdmin, f.senior.ID); err == nil {
		t.Fatal("banned account deleted")
	}
	if _, err := f.admin.DeleteAccount(ctx, ConsoleAdmin, f.owner.ID); err == nil {
		t.Fatal("owner deleted")
	}
	accounts, err := f.admin.Accounts(ctx, ConsoleAdmin, 0)
	if err != nil || len(accounts) != 3 {
		t.Fatalf("accounts = %+v, %v", accounts, err)
	}
	entries, err := f.store.AuditPage(ctx, 0, 3)
	if err != nil || entries[1].Action != "delete-account" || entries[1].Actor != "1" || entries[2].Action != "delete-account" || entries[2].Actor != "2" {
		t.Fatalf("audit = %+v, %v", entries, err)
	}
}
