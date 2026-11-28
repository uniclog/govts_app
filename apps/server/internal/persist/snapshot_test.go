package persist

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotIncludesWALWithoutMigratingSource(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "source с пробелом.db")
	db, err := sql.Open("sqlite3", source)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, statement := range []string{`PRAGMA journal_mode=WAL`, `PRAGMA wal_autocheckpoint=0`, `PRAGMA user_version=6`, `CREATE TABLE marker(value TEXT)`, `INSERT INTO marker VALUES('committed WAL data')`} {
		if _, err = db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if stat, err := os.Stat(source + "-wal"); err != nil || stat.Size() == 0 {
		t.Fatal("fixture has no committed WAL", err)
	}
	destination := filepath.Join(t.TempDir(), "snapshot.db")
	if err = SnapshotDatabase(ctx, source, destination); err != nil {
		t.Fatal(err)
	}
	var version int
	if err = db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != 6 {
		t.Fatal("source schema changed", version, err)
	}
	copy, err := openReadOnly(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	var value string
	if err = copy.QueryRowContext(ctx, `SELECT value FROM marker`).Scan(&value); err != nil || value != "committed WAL data" {
		t.Fatal("committed WAL missing", value, err)
	}
	if err = copy.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != 6 {
		t.Fatal("snapshot migrated", version, err)
	}
	if err = SnapshotDatabase(ctx, source, destination); err == nil {
		t.Fatal("overwrote existing snapshot")
	}
}
