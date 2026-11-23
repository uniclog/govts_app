package persist

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"uniclog.io/sonoryx/internal/domain"
)

func testAccount(t *testing.T, store *Store, seed byte, name string) Account {
	t.Helper()
	var key [32]byte
	key[0] = seed
	account, err := store.FindOrCreateAccount(context.Background(), key, name)
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func TestVersionFourMigrationAddsAdminWithoutChangingPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	account := testAccount(t, store, 1, "alice")
	if _, err := store.SetPermission(ctx, account.ID, PermissionKick, true); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`DROP TRIGGER chat_pins_tombstone`, `DROP TABLE chat_pins`, `DROP TRIGGER chat_reactions_tombstone`, `DROP TABLE chat_reactions`, `ALTER TABLE server_settings DROP COLUMN password`, `DROP TABLE chat_changes`, `ALTER TABLE chat_messages DROP COLUMN deleted`, `DROP TABLE chat_message_files`, `DROP TABLE file_attachments`, `DROP TABLE file_uploads`, `DROP TABLE file_policy`, `DROP TABLE file_rights`, `ALTER TABLE users DROP COLUMN admin`, `ALTER TABLE users DROP COLUMN deleted`, `PRAGMA user_version=4`} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version int
	if err := store.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != 12 {
		t.Fatalf("schema version = %d, %v", version, err)
	}
	migrated, err := store.Account(ctx, account.ID)
	if err != nil || migrated.Permissions != PermissionKick {
		t.Fatalf("migrated account = %+v, %v", migrated, err)
	}
}

func TestAdminPermissionIsStoredApartFromModerationBits(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	account := testAccount(t, store, 1, "alice")

	granted, err := store.SetPermissionBy(ctx, "7", account.ID, PermissionAdmin, true)
	if err != nil || granted.Permissions != PermissionAdmin {
		t.Fatalf("grant admin = %+v, %v", granted, err)
	}
	var stored, admin int
	if err := store.db.QueryRowContext(ctx, `SELECT permissions, admin FROM users WHERE id=?`, account.ID).Scan(&stored, &admin); err != nil || stored != 0 || admin != 1 {
		t.Fatalf("columns permissions=%d admin=%d, %v", stored, admin, err)
	}
	withBan, err := store.SetPermissionBy(ctx, "7", account.ID, PermissionBan, true)
	if err != nil || withBan.Permissions != PermissionAdmin|PermissionBan {
		t.Fatalf("grant ban = %+v, %v", withBan, err)
	}
	revoked, err := store.SetPermissionBy(ctx, "7", account.ID, PermissionAdmin, false)
	if err != nil || revoked.Permissions != PermissionBan {
		t.Fatalf("revoke admin = %+v, %v", revoked, err)
	}
	if _, err := store.SetPermissionBy(ctx, "7", account.ID, PrivilegeOwner, true); err == nil {
		t.Fatal("owner flag accepted as a permission")
	}
	if _, err := store.SetPermissionBy(ctx, "7", account.ID, PermissionKick|PermissionBan, true); err == nil {
		t.Fatal("two permission bits accepted at once")
	}
	if _, err := store.SetPermissionBy(ctx, "7", 999, PermissionKick, true); err == nil {
		t.Fatal("missing account accepted")
	}
}

func TestAccountPrivilegesAddOwnerFlag(t *testing.T) {
	account := Account{Permissions: PermissionKick | PermissionAdmin}
	if got := account.Privileges(); got != PermissionKick|PermissionAdmin {
		t.Fatalf("non-owner privileges = %08b", got)
	}
	account.Owner = true
	if got := account.Privileges(); got != PermissionKick|PermissionAdmin|PrivilegeOwner {
		t.Fatalf("owner privileges = %08b", got)
	}
}

func TestAccountsPageAndAuditPage(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	var ids []int64
	for seed := byte(1); seed <= 5; seed++ {
		ids = append(ids, testAccount(t, store, seed, string('a'+rune(seed))).ID)
	}

	first, err := store.AccountsPage(ctx, 0, 2)
	if err != nil || len(first) != 2 || first[0].ID != ids[0] || first[1].ID != ids[1] {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	rest, err := store.AccountsPage(ctx, first[1].ID, 10)
	if err != nil || len(rest) != 3 || rest[0].ID != ids[2] {
		t.Fatalf("second page = %+v, %v", rest, err)
	}
	if _, err := store.AccountsPage(ctx, -1, 2); err == nil {
		t.Fatal("negative cursor accepted")
	}

	if err := store.SetJoinLevelBy(ctx, "11", ids[0], 5); err != nil {
		t.Fatal(err)
	}
	if err := store.SetBan(ctx, "12", ids[1], true, "spam"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetJoinLevel(ctx, ids[2], 1); err != nil {
		t.Fatal(err)
	}
	entries, err := store.AuditPage(ctx, 0, 2)
	if err != nil || len(entries) != 2 {
		t.Fatalf("audit page = %+v, %v", entries, err)
	}
	if entries[0].Actor != ConsoleActor || entries[0].Action != "set-join-level" || entries[0].TargetUserID != ids[2] {
		t.Fatalf("newest audit entry = %+v", entries[0])
	}
	if entries[1].Actor != "12" || entries[1].Action != "set-ban" || entries[1].Detail != "spam" || entries[1].HappenedAt.IsZero() {
		t.Fatalf("second audit entry = %+v", entries[1])
	}
	older, err := store.AuditPage(ctx, entries[1].ID, 10)
	if err != nil || len(older) != 1 || older[0].Actor != "11" || older[0].Detail != "5" {
		t.Fatalf("older audit page = %+v, %v", older, err)
	}
}

func TestBanReasonIsReadBack(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	account := testAccount(t, store, 1, "alice")
	if err := store.SetBan(ctx, "7", account.ID, true, "flood"); err != nil {
		t.Fatal(err)
	}
	banned, err := store.Account(ctx, account.ID)
	if err != nil || !banned.Banned || banned.BanReason != "flood" {
		t.Fatalf("banned account = %+v, %v", banned, err)
	}
}

func TestInsertAndUpdateChannelBy(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	audio := domain.DefaultAudioProfile()
	if err := store.SaveInitialServer(ctx, ServerState{Name: "server", DefaultChannelID: 1, Channels: []domain.Channel{
		{ID: 1, Name: "default", Type: domain.ChannelTypePermanent, Audio: audio},
	}}); err != nil {
		t.Fatal(err)
	}
	room := domain.Channel{ID: 2, ParentID: 1, Name: "room", Topic: "t", Position: 3, MaxUsers: 4, MinJoinLevel: 5, Type: domain.ChannelTypePermanent, Audio: audio}
	if err := store.InsertChannelBy(ctx, "7", room); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertChannelBy(ctx, "7", room); err == nil {
		t.Fatal("duplicate channel ID inserted")
	}
	room.ParentID, room.Name, room.Audio.Bitrate = 0, "moved", 96_000
	if err := store.UpdateChannelBy(ctx, "8", room); err != nil {
		t.Fatal(err)
	}
	state, _, err := store.LoadServer(ctx)
	if err != nil || len(state.Channels) != 2 || state.Channels[1] != room {
		t.Fatalf("loaded channels = %+v, %v", state.Channels, err)
	}
	if err := store.UpdateChannelBy(ctx, "8", domain.Channel{ID: 1, Name: "default", MaxUsers: 2, Type: domain.ChannelTypePermanent, Audio: audio}); err == nil {
		t.Fatal("restricted default channel stored")
	}
	if err := store.UpdateChannelBy(ctx, "8", domain.Channel{ID: 99, Name: "x", Type: domain.ChannelTypePermanent, Audio: audio}); err == nil {
		t.Fatal("missing channel updated")
	}
	entries, err := store.AuditPage(ctx, 0, 2)
	if err != nil || len(entries) != 2 || entries[0].Action != "update-channel" || entries[0].Actor != "8" || entries[1].Action != "create-channel" ||
		!strings.Contains(entries[0].Detail, `name="moved"`) {
		t.Fatalf("audit = %+v, %v", entries, err)
	}
}
