package persist

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"uniclog.io/sonoryx/internal/filesignal"
)

var ErrFileConflict = errors.New("file state conflict")
var ErrFileQuota = errors.New("file quota exceeded")

// File payloads are defined by the shared client/server contract.
type (
	FileUpload     = filesignal.FileUpload
	FileAttachment = filesignal.FileAttachment
)

func migrateFiles(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{
		`CREATE TABLE chat_messages_new (id INTEGER PRIMARY KEY AUTOINCREMENT,channel_id INTEGER REFERENCES channels(id),sender_id INTEGER NOT NULL REFERENCES users(id),recipient_id INTEGER REFERENCES users(id),client_id BLOB NOT NULL CHECK(length(client_id)=16),sender_name TEXT NOT NULL,text TEXT NOT NULL CHECK(length(CAST(text AS BLOB)) BETWEEN 0 AND 1000),sent_at_ms INTEGER NOT NULL,UNIQUE(sender_id,client_id),CHECK((channel_id IS NULL)!=(recipient_id IS NULL)),CHECK(sender_id!=recipient_id))`,
		`INSERT INTO chat_messages_new SELECT * FROM chat_messages`,
		`DROP TABLE chat_messages`, `ALTER TABLE chat_messages_new RENAME TO chat_messages`,
		`CREATE INDEX chat_channel_history ON chat_messages(channel_id,id) WHERE channel_id IS NOT NULL`,
		`CREATE INDEX chat_direct_out ON chat_messages(sender_id,recipient_id,id) WHERE recipient_id IS NOT NULL`,
		`CREATE INDEX chat_direct_in ON chat_messages(recipient_id,sender_id,id) WHERE recipient_id IS NOT NULL`,
		`CREATE TABLE file_uploads(id TEXT PRIMARY KEY,request_id TEXT NOT NULL,user_id INTEGER NOT NULL REFERENCES users(id),kind INTEGER NOT NULL CHECK(kind IN(1,2)),target_id INTEGER NOT NULL,name TEXT NOT NULL,size INTEGER NOT NULL CHECK(size>=0),sha256 TEXT NOT NULL CHECK(length(sha256)=64),offset INTEGER NOT NULL DEFAULT 0 CHECK(offset>=0 AND offset<=size),state TEXT NOT NULL DEFAULT 'uploading',attachment_id INTEGER NOT NULL DEFAULT 0,updated INTEGER NOT NULL,UNIQUE(user_id,request_id))`,
		`CREATE TABLE file_attachments(id INTEGER PRIMARY KEY AUTOINCREMENT,upload_id TEXT NOT NULL UNIQUE REFERENCES file_uploads(id),user_id INTEGER NOT NULL REFERENCES users(id),kind INTEGER NOT NULL,target_id INTEGER NOT NULL,name TEXT NOT NULL,size INTEGER NOT NULL,sha256 TEXT NOT NULL,created INTEGER NOT NULL,deleted INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE chat_message_files(message_id INTEGER NOT NULL REFERENCES chat_messages(id) ON DELETE CASCADE,attachment_id INTEGER NOT NULL UNIQUE REFERENCES file_attachments(id),position INTEGER NOT NULL CHECK(position BETWEEN 0 AND 3),PRIMARY KEY(message_id,position))`,
		`CREATE INDEX file_uploads_cleanup ON file_uploads(state,updated)`,
		`CREATE INDEX file_attachments_target ON file_attachments(kind,target_id)`,
		`CREATE INDEX file_attachments_live ON file_attachments(created) WHERE deleted=0`,
		`CREATE TABLE file_policy(id INTEGER PRIMARY KEY CHECK(id=1),data TEXT NOT NULL)`,
		`CREATE TABLE file_rights(user_id INTEGER PRIMARY KEY REFERENCES users(id),upload INTEGER NOT NULL,download INTEGER NOT NULL)`,
		`PRAGMA user_version=7`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("migrate files: %w", err)
		}
	}
	return nil
}

func (s *Store) FilePolicy(ctx context.Context) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM file_policy WHERE id=1`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}
func (s *Store) SaveFilePolicy(ctx context.Context, actor, data string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO file_policy(id,data) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data`, data); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit(happened_at,actor,action,detail) VALUES(?,?,?,?)`, time.Now().UTC().Format(time.RFC3339Nano), actor, "file-policy", data); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) FileRights(ctx context.Context, id int64) (upload, download bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT upload,download FROM file_rights WHERE user_id=?`, id).Scan(&upload, &download)
	if errors.Is(err, sql.ErrNoRows) {
		return true, true, nil
	}
	return
}
func (s *Store) SetFileRights(ctx context.Context, actor string, id int64, upload, download bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO file_rights(user_id,upload,download) VALUES(?,?,?) ON CONFLICT(user_id) DO UPDATE SET upload=excluded.upload,download=excluded.download`, id, upload, download); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit(happened_at,actor,action,target_user_id,detail) VALUES(?,?,?,?,?)`, time.Now().UTC().Format(time.RFC3339Nano), actor, "file-rights", id, fmt.Sprintf("upload=%t download=%t", upload, download)); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) DeleteFile(ctx context.Context, actor string, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, `UPDATE file_attachments SET deleted=1 WHERE id=? AND deleted=0`, id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrFileConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE file_uploads SET state='expired',terminal_at=? WHERE attachment_id=? AND state NOT IN('cancelled','expired','failed')`, time.Now().UnixMilli(), id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit(happened_at,actor,action,detail) VALUES(?,?,?,?)`, time.Now().UTC().Format(time.RFC3339Nano), actor, "delete-file", fmt.Sprint(id)); err != nil {
		return err
	}
	if err = syncDeletedChatFiles(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) FileUsage(ctx context.Context) (map[string]int64, error) {
	var total, reserved, count int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(size),0),COALESCE(SUM(CASE WHEN state IN('uploading','verifying') THEN size ELSE 0 END),0),COUNT(*) FROM file_uploads WHERE state IN('uploading','verifying','ready','attached')`).Scan(&total, &reserved, &count)
	return map[string]int64{"logicalBytes": total, "reservedBytes": reserved, "uploads": count}, err
}
func (s *Store) FileAdminList(ctx context.Context, cursor int64) ([]FileAttachment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+fileColumns+` FROM file_attachments a WHERE a.id>? ORDER BY a.id LIMIT 50`, cursor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []FileAttachment{}
	for rows.Next() {
		var a FileAttachment
		if err = rows.Scan(&a.ID, &a.UploadID, &a.UserID, &a.Kind, &a.TargetID, &a.Name, &a.Size, &a.SHA256, &a.MessageID, &a.Deleted, &a.Created); err != nil {
			return nil, err
		}
		list = append(list, a)
	}
	return list, rows.Err()
}

const uploadColumns = `id,request_id,user_id,kind,target_id,name,size,sha256,offset,state,attachment_id,updated,terminal_at,error`

func scanUpload(row chatScanner) (u FileUpload, err error) {
	err = row.Scan(&u.ID, &u.RequestID, &u.UserID, &u.Kind, &u.TargetID, &u.Name, &u.Size, &u.SHA256, &u.Offset, &u.State, &u.AttachmentID, &u.Updated, &u.TerminalAt, &u.Error)
	return
}
func (s *Store) FileUpload(ctx context.Context, id string) (FileUpload, error) {
	return scanUpload(s.db.QueryRowContext(ctx, `SELECT `+uploadColumns+` FROM file_uploads WHERE id=?`, id))
}

func (s *Store) CreateFileUpload(ctx context.Context, u FileUpload, userQuota, totalQuota int64) (FileUpload, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return u, err
	}
	defer tx.Rollback()
	old, err := scanUpload(tx.QueryRowContext(ctx, `SELECT `+uploadColumns+` FROM file_uploads WHERE user_id=? AND request_id=?`, u.UserID, u.RequestID))
	if err == nil {
		if old.Kind != u.Kind || old.TargetID != u.TargetID || old.Name != u.Name || old.Size != u.Size || old.SHA256 != u.SHA256 {
			return u, ErrFileConflict
		}
		return old, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return u, err
	}
	var own, total int64
	var ownCount, totalCount, pending int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN user_id=? THEN 1 ELSE 0 END),0),COUNT(*),COALESCE(SUM(CASE WHEN user_id=? AND state IN('uploading','verifying','ready') THEN 1 ELSE 0 END),0) FROM file_uploads`, u.UserID, u.UserID).Scan(&ownCount, &totalCount, &pending); err != nil {
		return u, err
	}
	if ownCount >= 4096 || totalCount >= 100000 || pending >= 32 {
		return u, ErrFileQuota
	}
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN user_id=? THEN size ELSE 0 END),0),COALESCE(SUM(size),0) FROM file_uploads WHERE state IN ('uploading','verifying','ready','attached')`, u.UserID).Scan(&own, &total); err != nil {
		return u, err
	}
	if u.Size > userQuota-own || u.Size > totalQuota-total {
		return u, ErrFileQuota
	}
	u.Updated = time.Now().UnixMilli()
	u.State = "uploading"
	_, err = tx.ExecContext(ctx, `INSERT INTO file_uploads(id,request_id,user_id,kind,target_id,name,size,sha256,updated) VALUES(?,?,?,?,?,?,?,?,?)`, u.ID, u.RequestID, u.UserID, u.Kind, u.TargetID, u.Name, u.Size, u.SHA256, u.Updated)
	if err != nil {
		return u, err
	}
	return u, tx.Commit()
}

func (s *Store) CheckpointFile(ctx context.Context, id string, old, next int64) error {
	r, err := s.db.ExecContext(ctx, `UPDATE file_uploads SET offset=?,updated=? WHERE id=? AND state='uploading' AND offset=?`, next, time.Now().UnixMilli(), id, old)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n != 1 {
		return ErrFileConflict
	}
	return err
}

func (s *Store) CompleteFile(ctx context.Context, id string) (FileUpload, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FileUpload{}, err
	}
	defer tx.Rollback()
	u, err := scanUpload(tx.QueryRowContext(ctx, `SELECT `+uploadColumns+` FROM file_uploads WHERE id=?`, id))
	if err != nil {
		return u, err
	}
	if u.State == "ready" || u.State == "attached" {
		return u, tx.Commit()
	}
	if (u.State != "uploading" && u.State != "verifying") || u.Offset != u.Size {
		return u, ErrFileConflict
	}
	r, err := tx.ExecContext(ctx, `INSERT INTO file_attachments(upload_id,user_id,kind,target_id,name,size,sha256,created) VALUES(?,?,?,?,?,?,?,?)`, id, u.UserID, u.Kind, u.TargetID, u.Name, u.Size, u.SHA256, time.Now().UnixMilli())
	if err != nil {
		return u, err
	}
	u.AttachmentID, err = r.LastInsertId()
	if err != nil {
		return u, err
	}
	u.State = "ready"
	_, err = tx.ExecContext(ctx, `UPDATE file_uploads SET state='ready',attachment_id=?,updated=? WHERE id=?`, u.AttachmentID, time.Now().UnixMilli(), id)
	if err != nil {
		return u, err
	}
	return u, tx.Commit()
}

const fileColumns = `a.id,a.upload_id,a.user_id,a.kind,a.target_id,a.name,a.size,a.sha256,COALESCE((SELECT message_id FROM chat_message_files WHERE attachment_id=a.id),0),a.deleted,a.created`

func (s *Store) FileAttachment(ctx context.Context, id int64) (a FileAttachment, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT `+fileColumns+` FROM file_attachments a WHERE a.id=?`, id).Scan(&a.ID, &a.UploadID, &a.UserID, &a.Kind, &a.TargetID, &a.Name, &a.Size, &a.SHA256, &a.MessageID, &a.Deleted, &a.Created)
	return
}

func (s *Store) CancelFile(ctx context.Context, id string) error {
	now := time.Now().UnixMilli()
	r, err := s.db.ExecContext(ctx, `UPDATE file_uploads SET state='cancelled',updated=?,terminal_at=? WHERE id=? AND state IN('uploading','verifying','ready')`, now, now, id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrFileConflict
	}
	_, err = s.db.ExecContext(ctx, `UPDATE file_attachments SET deleted=1 WHERE upload_id=?`, id)
	return err
}

func (s *Store) ExpireFiles(ctx context.Context, ttl, retention time.Duration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	_, err = tx.ExecContext(ctx, `UPDATE file_uploads SET state='expired',terminal_at=? WHERE state IN('uploading','verifying','ready') AND updated<?`, now, now-ttl.Milliseconds())
	if err != nil {
		return err
	}
	// Message/channel/account deletion revokes file references even with soft-deleted users.
	_, err = tx.ExecContext(ctx, `UPDATE file_attachments SET deleted=1 WHERE deleted=0 AND (upload_id IN(SELECT id FROM file_uploads WHERE state IN('expired','cancelled')) OR user_id IN(SELECT id FROM users WHERE deleted=1) OR (kind=1 AND target_id NOT IN(SELECT id FROM channels)) OR (kind=2 AND target_id IN(SELECT id FROM users WHERE deleted=1)) OR (upload_id IN(SELECT id FROM file_uploads WHERE state='attached') AND id NOT IN(SELECT attachment_id FROM chat_message_files)))`)
	if err != nil {
		return err
	}
	if retention > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE file_attachments SET deleted=1 WHERE created<?`, now-retention.Milliseconds()); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE file_uploads SET state='expired',terminal_at=? WHERE state NOT IN('cancelled','expired','failed') AND attachment_id IN(SELECT id FROM file_attachments WHERE deleted=1)`, now)
	if err != nil {
		return err
	}
	cutoff := now - (24 * time.Hour).Milliseconds()
	if err = syncDeletedChatFiles(ctx, tx, 0); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM file_attachments WHERE id IN(SELECT a.id FROM file_attachments a JOIN file_uploads u ON u.id=a.upload_id WHERE u.state IN('cancelled','expired','failed') AND u.terminal_at>0 AND u.terminal_at<? AND NOT EXISTS(SELECT 1 FROM chat_message_files m WHERE m.attachment_id=a.id) LIMIT 200)`, cutoff); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM file_uploads WHERE id IN(SELECT u.id FROM file_uploads u WHERE u.state IN('cancelled','expired','failed') AND u.terminal_at>0 AND u.terminal_at<? AND NOT EXISTS(SELECT 1 FROM file_attachments a WHERE a.upload_id=u.id) LIMIT 200)`, cutoff); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FileUploads(ctx context.Context) ([]FileUpload, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+uploadColumns+` FROM file_uploads WHERE state IN('uploading','verifying','ready','attached') ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []FileUpload
	for rows.Next() {
		u, e := scanUpload(rows)
		if e != nil {
			return nil, e
		}
		list = append(list, u)
	}
	return list, rows.Err()
}

// FileCleanupRefs returns the blob hashes and temporary upload IDs that are
// still in use. It reads only these columns: cleanup runs every minute and the
// table keeps a row for every attached file.
func (s *Store) FileCleanupRefs(ctx context.Context) (blobs, temps map[string]bool, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,sha256,state FROM file_uploads WHERE state IN('uploading','verifying','ready','attached')`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	blobs, temps = make(map[string]bool), make(map[string]bool)
	for rows.Next() {
		var id, hash, state string
		if err = rows.Scan(&id, &hash, &state); err != nil {
			return nil, nil, err
		}
		blobs[hash] = true
		if state == "uploading" || state == "verifying" {
			temps[id] = true
		}
	}
	return blobs, temps, rows.Err()
}
