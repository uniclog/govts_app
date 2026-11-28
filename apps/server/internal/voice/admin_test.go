package voice

import (
	"context"
	"crypto/sha256"
	"errors"
	"net"
	"path/filepath"
	"testing"

	"uniclog.io/sonoryx/apps/server/internal/persist"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

type adminFixture struct {
	store  *persist.Store
	hub    *Hub
	admin  *Administration
	owner  persist.Account
	actor  persist.Account // administrator with kick, level 10
	peer   persist.Account // another administrator
	user   persist.Account // plain account, level 5
	senior persist.Account // plain account above the actor, level 20
	notes  []uint8
}

func newAdminFixture(t *testing.T) *adminFixture {
	t.Helper()
	store, err := persist.Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	f := &adminFixture{store: store, hub: NewHub()}
	f.admin = NewAdministration(store, f.hub)
	f.admin.SetPrivilegeNotifier(func(_ int64, _ uint16, privileges uint8) { f.notes = append(f.notes, privileges) })
	account := func(seed byte, name string, level uint16, permissions ...uint8) persist.Account {
		var key [32]byte
		key[0] = seed
		created, err := store.FindOrCreateAccount(ctx, key, name)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetJoinLevel(ctx, created.ID, level); err != nil {
			t.Fatal(err)
		}
		for _, permission := range permissions {
			if _, err := store.SetPermission(ctx, created.ID, permission, true); err != nil {
				t.Fatal(err)
			}
		}
		created, err = store.Account(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	f.owner = account(1, "owner", 0)
	if err := store.SetOwner(ctx, f.owner.ID); err != nil {
		t.Fatal(err)
	}
	f.owner.Owner = true
	f.actor = account(2, "admin", 10, persist.PermissionAdmin, persist.PermissionKick)
	f.peer = account(3, "peer", 0, persist.PermissionAdmin)
	f.user = account(4, "user", 5)
	f.senior = account(5, "senior", 20)
	return f
}

func actorOf(account persist.Account) AdminActor {
	return AdminActor{UserID: account.ID, JoinLevel: account.JoinLevel, Permissions: account.Permissions, Owner: account.Owner}
}

func addSession(t *testing.T, hub *Hub, account persist.Account, port int) Session {
	t.Helper()
	session, _, err := hub.CreateAuthenticatedSession(account.DisplayName, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}, account.ID, account.JoinLevel, account.Permissions, account.Owner)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestAdministrationRejectsActorsWithoutAdminRight(t *testing.T) {
	f := newAdminFixture(t)
	ctx := context.Background()
	plain := actorOf(f.user)
	if _, err := f.admin.Accounts(ctx, plain, 0); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("Accounts() error = %v", err)
	}
	if _, err := f.admin.Audit(ctx, plain, 0); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("Audit() error = %v", err)
	}
	if _, err := f.admin.SetJoinLevel(ctx, plain, f.senior.ID, 1); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("SetJoinLevel() error = %v", err)
	}
	// A moderator without the admin bit is still not an administrator.
	moderator := AdminActor{UserID: f.user.ID, Permissions: persist.PermissionKick | persist.PermissionBan | persist.PermissionDrag}
	if _, err := f.admin.SetBan(ctx, moderator, f.senior.ID, true, ""); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("SetBan() by moderator error = %v", err)
	}
}

func TestAdministratorHierarchy(t *testing.T) {
	f := newAdminFixture(t)
	ctx := context.Background()
	actor := actorOf(f.actor)

	change, err := f.admin.SetPermission(ctx, actor, f.user.ID, persist.PermissionKick, true)
	if err != nil || change.Account.Permissions != persist.PermissionKick {
		t.Fatalf("grant held permission = %+v, %v", change, err)
	}
	if len(f.notes) != 1 || f.notes[0] != persist.PermissionKick {
		t.Fatalf("privilege notifications = %v", f.notes)
	}
	denied := []struct {
		name string
		call func() error
	}{
		{"grant a permission the actor lacks", func() error {
			_, err := f.admin.SetPermission(ctx, actor, f.user.ID, persist.PermissionBan, true)
			return err
		}},
		{"appoint an administrator", func() error {
			_, err := f.admin.SetPermission(ctx, actor, f.user.ID, persist.PermissionAdmin, true)
			return err
		}},
		{"change another administrator", func() error {
			_, err := f.admin.SetJoinLevel(ctx, actor, f.peer.ID, 1)
			return err
		}},
		{"change the owner", func() error {
			_, err := f.admin.SetPermission(ctx, actor, f.owner.ID, persist.PermissionKick, true)
			return err
		}},
		{"change own account", func() error {
			_, err := f.admin.SetJoinLevel(ctx, actor, f.actor.ID, 3)
			return err
		}},
		{"change an account above own level", func() error {
			_, err := f.admin.SetJoinLevel(ctx, actor, f.senior.ID, 1)
			return err
		}},
		{"raise a level above own", func() error {
			_, err := f.admin.SetJoinLevel(ctx, actor, f.user.ID, 11)
			return err
		}},
		{"ban without the ban permission", func() error {
			_, err := f.admin.SetBan(ctx, actor, f.user.ID, true, "")
			return err
		}},
	}
	for _, item := range denied {
		if err := item.call(); !errors.Is(err, ErrPermissionDenied) {
			t.Errorf("%s: error = %v", item.name, err)
		}
	}
	change, err = f.admin.SetJoinLevel(ctx, actor, f.user.ID, 10)
	if err != nil || change.Account.JoinLevel != 10 {
		t.Fatalf("level equal to own = %+v, %v", change, err)
	}
	entries, err := f.store.AuditPage(ctx, 0, 1)
	if err != nil || len(entries) != 1 || entries[0].Actor != "2" || entries[0].Action != "set-join-level" {
		t.Fatalf("audit entry = %+v, %v", entries, err)
	}
}

func TestOwnerAndConsoleRights(t *testing.T) {
	f := newAdminFixture(t)
	ctx := context.Background()
	owner := actorOf(f.owner)

	change, err := f.admin.SetPermission(ctx, owner, f.user.ID, persist.PermissionAdmin, true)
	if err != nil || change.Account.Permissions != persist.PermissionAdmin {
		t.Fatalf("owner appoints administrator = %+v, %v", change, err)
	}
	if _, err := f.admin.SetJoinLevel(ctx, owner, f.peer.ID, 60000); err != nil {
		t.Fatalf("owner changes administrator: %v", err)
	}
	// An owner edits their own permission bits, and nothing else about themselves.
	if change, err = f.admin.SetPermission(ctx, owner, f.owner.ID, persist.PermissionKick, true); err != nil || change.Account.Permissions&persist.PermissionKick == 0 {
		t.Fatalf("owner grants own permission = %+v, %v", change, err)
	}
	if change, err = f.admin.SetPermission(ctx, owner, f.owner.ID, persist.PermissionKick, false); err != nil || change.Account.Permissions&persist.PermissionKick != 0 {
		t.Fatalf("owner revokes own permission = %+v, %v", change, err)
	}
	if _, err := f.admin.SetJoinLevel(ctx, owner, f.owner.ID, 60000); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("owner changes own join level remotely: %v", err)
	}
	change, err = f.admin.SetPermission(ctx, ConsoleAdmin, f.owner.ID, persist.PermissionKick, true)
	if err != nil || !change.Account.Owner {
		t.Fatalf("console changes owner = %+v, %v", change, err)
	}
	if got := f.notes[len(f.notes)-1]; got != persist.PermissionKick|persist.PrivilegeOwner {
		t.Fatalf("owner notification = %08b", got)
	}
	if _, err := f.admin.SetBan(ctx, ConsoleAdmin, f.owner.ID, true, ""); err == nil {
		t.Fatal("console banned the owner")
	}
	entries, err := f.store.AuditPage(ctx, 0, 1)
	if err != nil || len(entries) != 1 || entries[0].Actor != persist.ConsoleActor {
		t.Fatalf("console audit entry = %+v, %v", entries, err)
	}
}

func TestBanRemovesLiveSessionsAndUnbanClearsReason(t *testing.T) {
	f := newAdminFixture(t)
	ctx := context.Background()
	first := addSession(t, f.hub, f.user, 6001)
	second := addSession(t, f.hub, f.user, 6002)
	bystander := addSession(t, f.hub, f.senior, 6003)

	change, err := f.admin.SetBan(ctx, actorOf(f.owner), f.user.ID, true, "  ")
	if err != nil || !change.Account.Banned || change.Account.BanReason != "administrator action" {
		t.Fatalf("ban = %+v, %v", change, err)
	}
	if len(change.Removed) != 2 || change.Removed[0] != min(first.ID, second.ID) {
		t.Fatalf("removed sessions = %v", change.Removed)
	}
	if _, ok := f.hub.Get(bystander.ID); !ok {
		t.Fatal("ban removed another account's session")
	}
	change, err = f.admin.SetBan(ctx, actorOf(f.owner), f.user.ID, false, "ignored")
	if err != nil || change.Account.Banned || change.Account.BanReason != "" || len(change.Removed) != 0 {
		t.Fatalf("unban = %+v, %v", change, err)
	}
}

func TestChannelJoinLevelLimitsAndRemovals(t *testing.T) {
	f := newAdminFixture(t)
	ctx := context.Background()
	channel, err := f.hub.CreateChannel(domain.Channel{Name: "room"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.SaveInitialServer(ctx, persist.ServerState{Name: "server", DefaultChannelID: DefaultChannelID, Channels: f.hub.ListChannels()}); err != nil {
		t.Fatal(err)
	}
	member := addSession(t, f.hub, f.user, 6101)
	if err := f.hub.JoinChannel(member.ID, channel.ID); err != nil {
		t.Fatal(err)
	}
	actor := actorOf(f.actor)

	if _, err := f.admin.SetChannelJoinLevel(ctx, actor, channel.ID, 11); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("threshold above own level: %v", err)
	}
	removed, err := f.admin.SetChannelJoinLevel(ctx, actor, channel.ID, 8)
	if err != nil || len(removed) != 1 || removed[0] != member.ID {
		t.Fatalf("threshold 8 removed = %v, %v", removed, err)
	}
	if _, err := f.admin.SetChannelJoinLevel(ctx, actorOf(f.owner), channel.ID, 30); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.SetChannelJoinLevel(ctx, actor, channel.ID, 0); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("unlock of a channel above own level: %v", err)
	}
	if _, err := f.admin.SetChannelJoinLevel(ctx, actor, domain.ChannelID(999), 1); err == nil {
		t.Fatal("unknown channel accepted")
	}
	if got, ok := f.hub.GetChannel(channel.ID); !ok || got.MinJoinLevel != 30 {
		t.Fatalf("channel = %+v, found=%t", got, ok)
	}
}

func TestAdminWireConversion(t *testing.T) {
	f := newAdminFixture(t)
	addSession(t, f.hub, f.owner, 6201)
	online := f.hub.onlineUsers()
	if !online[f.owner.ID] || online[f.user.ID] {
		t.Fatalf("online users = %v", online)
	}
	wire := adminAccountWire(f.owner, online[f.owner.ID])
	sum := sha256.Sum256(f.owner.PublicKey[:])
	if wire.ID != uint64(f.owner.ID) || wire.Flags != protocol.AdminAccountOwner|protocol.AdminAccountOnline || string(wire.Fingerprint[:]) != string(sum[:8]) {
		t.Fatalf("owner wire = %+v", wire)
	}
	actor := adminAccountWire(f.actor, false)
	if actor.Permissions != protocol.PrivilegeAdmin|protocol.PrivilegeKick || actor.Flags != 0 || actor.JoinLevel != 10 {
		t.Fatalf("administrator wire = %+v", actor)
	}
}
