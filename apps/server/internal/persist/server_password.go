package persist

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"uniclog.io/sonoryx/internal/auth"
)

// ServerPassword returns an administration override. NULL uses the JSON setting;
// an explicitly saved empty string disables password protection.
func (s *Store) ServerPassword(ctx context.Context) (string, bool, error) {
	var password sql.NullString
	if err := s.db.QueryRowContext(ctx, "SELECT password FROM server_settings WHERE id=1").Scan(&password); err != nil {
		return "", false, err
	}
	if err := auth.ValidatePassword(password.String); err != nil {
		return "", false, err
	}
	return password.String, password.Valid, nil
}

// SetServerPassword atomically persists the admission setting and an audit
// event. Like the JSON configuration, this setting contains a plaintext secret;
// neither the password nor its hash is included in the audit event.
func (s *Store) SetServerPassword(ctx context.Context, actor, password string) error {
	if err := auth.ValidatePassword(password); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE server_settings SET password=? WHERE id=1", password)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil {
		return err
	} else if count != 1 {
		return errors.New("server settings are unavailable")
	}
	detail := "enabled"
	if password == "" {
		detail = "disabled"
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(happened_at,actor,action,detail) VALUES(?,?,?,?)", time.Now().UTC().Format(time.RFC3339Nano), actor, "set-server-password", detail); err != nil {
		return err
	}
	return tx.Commit()
}
