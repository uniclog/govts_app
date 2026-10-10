package persist

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	"uniclog.io/sonoryx/internal/domain"
)

func TestMigrateVersionOneAndPreserveAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, public_key BLOB NOT NULL UNIQUE CHECK(length(public_key)=32), display_name TEXT NOT NULL, join_level INTEGER NOT NULL DEFAULT 0 CHECK(join_level BETWEEN 0 AND 65535), permissions INTEGER NOT NULL DEFAULT 0 CHECK(permissions BETWEEN 0 AND 7), banned INTEGER NOT NULL DEFAULT 0 CHECK(banned IN (0,1)), created_at TEXT NOT NULL)`,
		`CREATE TABLE server_settings (id INTEGER PRIMARY KEY CHECK(id=1), name TEXT NOT NULL)`,
		`CREATE TABLE audit (id INTEGER PRIMARY KEY, happened_at TEXT NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL, target_user_id INTEGER, detail TEXT NOT NULL DEFAULT '')`,
		`PRAGMA user_version = 1`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	var key [32]byte
	key[0] = 42
	if _, err := db.Exec(`INSERT INTO users(id,public_key,display_name,join_level,permissions,banned,created_at) VALUES(1,?,?,?,?,0,'2026-01-01')`, key[:], "alice", 25, PermissionKick); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	account, err := store.Account(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if account.PublicKey != key || account.JoinLevel != 25 || account.Permissions != PermissionKick || account.Owner || account.Banned {
		t.Fatalf("migrated account = %+v", account)
	}
	if err := store.SetOwner(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	account, err = store.Account(context.Background(), 1)
	if err != nil || !account.Owner {
		t.Fatalf("owner after migration = %+v, %v", account, err)
	}
}

func TestConcurrentEnrollmentUsesOneAccount(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var key [32]byte
	key[0] = 99
	const workers = 12
	accounts := make([]Account, workers)
	errs := make([]error, workers)
	var group sync.WaitGroup
	for i := range accounts {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			accounts[i], errs[i] = store.FindOrCreateAccount(context.Background(), key, "alice")
		}(i)
	}
	group.Wait()
	for i := range accounts {
		if errs[i] != nil || accounts[i].ID != accounts[0].ID || accounts[i].ID <= 0 {
			t.Fatalf("enrollment %d = %+v, %v", i, accounts[i], errs[i])
		}
	}
	all, err := store.Accounts(context.Background())
	if err != nil || len(all) != 1 {
		t.Fatalf("accounts = %+v, %v", all, err)
	}
}

func TestVersionTwoMigrationSelectsUnrestrictedDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.SaveInitialServer(ctx, ServerState{Name: "server", DefaultChannelID: 1, Channels: []domain.Channel{
		{ID: 1, Name: "legacy", Type: domain.ChannelTypePermanent, Audio: domain.DefaultAudioProfile()},
		{ID: 2, Name: "open", Type: domain.ChannelTypePermanent, Audio: domain.DefaultAudioProfile()},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE channels SET min_join_level=25,max_users=10 WHERE id=1`,
		`ALTER TABLE server_settings DROP COLUMN default_channel_id`,
		`DROP TABLE chat_reads`,
		`DROP TABLE chat_messages`,
		`DROP TABLE chat_dialogs`,
		`PRAGMA user_version=2`,
	} {
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
	state, initialized, err := store.LoadServer(ctx)
	if err != nil || !initialized || state.DefaultChannelID != 2 {
		t.Fatalf("migrated server = %+v, initialized=%t, err=%v", state, initialized, err)
	}
	if err := store.SetChannelJoinLevel(ctx, 2, 25); err == nil {
		t.Fatal("default channel threshold was increased")
	}
}

func TestVersionTwoMigrationPreservesRestrictedChannels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.SaveInitialServer(ctx, ServerState{Name: "server", DefaultChannelID: 1, Channels: []domain.Channel{
		{ID: 1, Name: "private", Type: domain.ChannelTypePermanent, Audio: domain.DefaultAudioProfile()},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE channels SET min_join_level=25,max_users=10 WHERE id=1`,
		`ALTER TABLE server_settings DROP COLUMN default_channel_id`,
		`DROP TABLE chat_reads`,
		`DROP TABLE chat_messages`,
		`DROP TABLE chat_dialogs`,
		`PRAGMA user_version=2`,
	} {
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
	state, initialized, err := store.LoadServer(ctx)
	if err != nil || !initialized || state.DefaultChannelID != 2 || len(state.Channels) != 2 {
		t.Fatalf("migrated server = %+v, initialized=%t, err=%v", state, initialized, err)
	}
	if state.Channels[0].MinJoinLevel != 25 || state.Channels[0].MaxUsers != 10 || state.Channels[1].MinJoinLevel != 0 || state.Channels[1].MaxUsers != 0 {
		t.Fatalf("migration changed restricted channel or limited default: %+v", state.Channels)
	}
}
