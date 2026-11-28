package persist

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
)

func openReadOnly(path string) (*sql.DB, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("database must be a regular file")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	filename := filepath.ToSlash(abs)
	// Keep Windows drive paths as file:C:/..., not /C:/... for the Go VFS.
	uri := "file:" + (&url.URL{Path: filename}).EscapedPath() + "?mode=ro"
	db, err := sql.Open("sqlite3", uri)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// SnapshotDatabase includes committed WAL contents without invoking migrations on the source.
// SQLite VACUUM INTO creates a consistent, separate database: https://sqlite.org/lang_vacuum.html
func SnapshotDatabase(ctx context.Context, source, destination string) error {
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return errors.New("snapshot destination must not exist")
	}
	db, err := openReadOnly(source)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, `PRAGMA synchronous=FULL`); err != nil {
		return err
	}
	if _, err = db.ExecContext(ctx, `VACUUM INTO ?`, destination); err != nil {
		_ = os.Remove(destination)
		return err
	}
	return os.Chmod(destination, 0600)
}

func (s *Store) Ready(ctx context.Context) error { return s.db.PingContext(ctx) }
