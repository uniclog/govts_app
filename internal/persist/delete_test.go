package persist

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"uniclog.io/sonoryx/internal/domain"
)

func openDeletionStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	audio := domain.DefaultAudioProfile()
	if err := store.SaveInitialServer(context.Background(), ServerState{Name: "server", DefaultChannelID: 1, Channels: []domain.Channel{
		{ID: 1, Name: "default", Type: domain.ChannelTypePermanent, Audio: audio},
		{ID: 2, Name: "room", Type: domain.ChannelTypePermanent, Audio: audio},
		{ID: 3, ParentID: 2, Name: "sub", Type: domain.ChannelTypePermanent, Audio: audio},
	}}); err != nil {
		t.Fatal(err)
	}
	return store
}

func countRows(t *testing.T, store *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := store.db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func sendChat(t *testing.T, store *Store, from Account, target domain.ChatTarget, id byte) domain.ChatMessage {
	t.Helper()
	message, err := store.SaveChat(context.Background(), from.ID, from.DisplayName, target, [16]byte{id}, "hello")
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func TestVersionFiveMigrationAddsDeletedFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	account := testAccount(t, store, 1, "alice")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`DROP TRIGGER chat_pins_tombstone`, `DROP TABLE chat_pins`, `DROP TRIGGER chat_reactions_tombstone`, `DROP TABLE chat_reactions`, `ALTER TABLE server_settings DROP COLUMN password`, `DROP TABLE chat_changes`, `ALTER TABLE chat_messages DROP COLUMN deleted`, `DROP TABLE chat_message_files`, `DROP TABLE file_attachments`, `DROP TABLE file_uploads`, `DROP TABLE file_policy`, `DROP TABLE file_rights`, `ALTER TABLE users DROP COLUMN deleted`, `PRAGMA user_version=5`} {
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
	if version := countRows(t, store, `PRAGMA user_version`); version != 12 {
		t.Fatalf("schema version = %d", version)
	}
	if migrated, err := store.Account(ctx, account.ID); err != nil || migrated.DisplayName != "alice" {
		t.Fatalf("migrated account = %+v, %v", migrated, err)
	}
}

func TestDeleteAccountKeepsChannelMessagesAndForgetsTheRest(t *testing.T) {
	store := openDeletionStore(t)
	ctx := context.Background()
	alice := testAccount(t, store, 1, "alice")
	bob := testAccount(t, store, 2, "bob")
	if _, err := store.SetPermissionBy(ctx, "7", alice.ID, PermissionAdmin, true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetJoinLevelBy(ctx, "7", alice.ID, 9); err != nil {
		t.Fatal(err)
	}
	channel := domain.ChatTarget{Kind: domain.ChatChannel, ID: 1}
	kept := sendChat(t, store, alice, channel, 1)
	sendChat(t, store, alice, domain.ChatTarget{Kind: domain.ChatDirect, ID: bob.ID}, 2)
	reply := sendChat(t, store, bob, domain.ChatTarget{Kind: domain.ChatDirect, ID: alice.ID}, 3)
	sendChat(t, store, bob, channel, 4)
	if err := store.ReadChat(ctx, bob.ID, domain.ChatTarget{Kind: domain.ChatDirect, ID: alice.ID}, reply.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ReadChat(ctx, alice.ID, channel, kept.ID); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteAccountBy(ctx, "7", alice.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Account(ctx, alice.ID); err == nil {
		t.Fatal("deleted account still readable")
	}
	accounts, err := store.Accounts(ctx)
	if err != nil || len(accounts) != 1 || accounts[0].ID != bob.ID {
		t.Fatalf("accounts = %+v, %v", accounts, err)
	}
	if page, err := store.AccountsPage(ctx, 0, 10); err != nil || len(page) != 1 {
		t.Fatalf("accounts page = %+v, %v", page, err)
	}
	if n := countRows(t, store, `SELECT count(*) FROM users WHERE id=? AND deleted=1 AND join_level=0 AND permissions=0 AND admin=0 AND public_key!=?`, alice.ID, alice.PublicKey[:]); n != 1 {
		t.Fatal("hidden record keeps the key or rights")
	}
	history, err := store.ChatHistory(ctx, bob.ID, channel, 0, false)
	if err != nil || len(history.Messages) != 2 || history.Messages[1].ID != kept.ID || history.Messages[1].SenderName != "alice" {
		t.Fatalf("channel history = %+v, %v", history, err)
	}
	if n := countRows(t, store, `SELECT count(*) FROM chat_messages WHERE recipient_id IS NOT NULL`); n != 0 {
		t.Fatalf("direct messages left = %d", n)
	}
	if n := countRows(t, store, `SELECT count(*) FROM chat_dialogs`); n != 0 {
		t.Fatalf("dialogs left = %d", n)
	}
	if n := countRows(t, store, `SELECT count(*) FROM chat_reads WHERE user_id=? OR (kind=2 AND target_id=?)`, alice.ID, alice.ID); n != 0 {
		t.Fatalf("read markers left = %d", n)
	}
	if dialogs, err := store.ChatDialogs(ctx, bob.ID, 0); err != nil || len(dialogs.Dialogs) != 0 {
		t.Fatalf("bob dialogs = %+v, %v", dialogs, err)
	}
	if _, err := store.SaveChat(ctx, bob.ID, "bob", domain.ChatTarget{Kind: domain.ChatDirect, ID: alice.ID}, [16]byte{5}, "hi"); err == nil {
		t.Fatal("direct message to a deleted account accepted")
	}

	// The hidden record accepts no changes.
	if err := store.SetJoinLevelBy(ctx, "7", alice.ID, 1); err == nil {
		t.Fatal("join level of a deleted account changed")
	}
	if _, err := store.SetPermissionBy(ctx, "7", alice.ID, PermissionKick, true); err == nil {
		t.Fatal("permission of a deleted account changed")
	}
	if err := store.SetBan(ctx, "7", alice.ID, true, "x"); err == nil {
		t.Fatal("deleted account banned")
	}
	if err := store.SetOwner(ctx, alice.ID); err == nil {
		t.Fatal("deleted account made owner")
	}
	if err := store.DeleteAccountBy(ctx, "7", alice.ID); err == nil {
		t.Fatal("account deleted twice")
	}

	// The old key registers a new, unprivileged account.
	again, err := store.FindOrCreateAccount(ctx, alice.PublicKey, "alice")
	if err != nil || again.ID == alice.ID || again.JoinLevel != 0 || again.Privileges() != 0 {
		t.Fatalf("account for the old key = %+v, %v", again, err)
	}

	entries, err := store.AuditPage(ctx, 0, 1)
	if err != nil || len(entries) != 1 || entries[0].Action != "delete-account" || entries[0].Actor != "7" || entries[0].TargetUserID != alice.ID || entries[0].Detail != "alice" {
		t.Fatalf("audit = %+v, %v", entries, err)
	}
}

func TestDeleteAccountProtectsOwnerAndBanned(t *testing.T) {
	store := openDeletionStore(t)
	ctx := context.Background()
	owner := testAccount(t, store, 1, "owner")
	banned := testAccount(t, store, 2, "banned")
	if err := store.SetOwner(ctx, owner.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetBan(ctx, "7", banned.ID, true, "spam"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{owner.ID, banned.ID, 999} {
		if err := store.DeleteAccountBy(ctx, "7", id); err == nil {
			t.Fatalf("account %d deleted", id)
		}
	}
	if accounts, err := store.Accounts(ctx); err != nil || len(accounts) != 2 {
		t.Fatalf("accounts = %+v, %v", accounts, err)
	}
}

func TestDeleteChannelBy(t *testing.T) {
	store := openDeletionStore(t)
	ctx := context.Background()
	alice := testAccount(t, store, 1, "alice")
	room := domain.ChatTarget{Kind: domain.ChatChannel, ID: 3}
	message := sendChat(t, store, alice, room, 1)
	sendChat(t, store, alice, domain.ChatTarget{Kind: domain.ChatChannel, ID: 1}, 2)
	if err := store.ReadChat(ctx, alice.ID, room, message.ID); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteChannelBy(ctx, "7", 1); err == nil {
		t.Fatal("default channel deleted")
	}
	if err := store.DeleteChannelBy(ctx, "7", 2); err == nil {
		t.Fatal("channel with subchannels deleted")
	}
	if err := store.DeleteChannelBy(ctx, "7", 99); err == nil {
		t.Fatal("missing channel deleted")
	}
	if err := store.DeleteChannelBy(ctx, "7", 3); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteChannelBy(ctx, "7", 2); err != nil {
		t.Fatalf("emptied parent: %v", err)
	}
	state, _, err := store.LoadServer(ctx)
	if err != nil || len(state.Channels) != 1 || state.Channels[0].ID != 1 {
		t.Fatalf("channels = %+v, %v", state.Channels, err)
	}
	if n := countRows(t, store, `SELECT count(*) FROM chat_messages WHERE channel_id=3`); n != 0 {
		t.Fatalf("messages of deleted channel = %d", n)
	}
	if n := countRows(t, store, `SELECT count(*) FROM chat_messages WHERE channel_id=1`); n != 1 {
		t.Fatalf("messages of default channel = %d", n)
	}
	if n := countRows(t, store, `SELECT count(*) FROM chat_reads WHERE kind=1 AND target_id=3`); n != 0 {
		t.Fatalf("read markers of deleted channel = %d", n)
	}
	entries, err := store.AuditPage(ctx, 0, 2)
	if err != nil || len(entries) != 2 || entries[0].Action != "delete-channel" || entries[0].Detail != `id=2 name="room"` || entries[1].Detail != `id=3 name="sub"` {
		t.Fatalf("audit = %+v, %v", entries, err)
	}
}
