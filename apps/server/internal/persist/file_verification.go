package persist

import (
	"context"
	"time"
)

func (s *Store) BeginFileVerification(ctx context.Context, id string) (FileUpload, error) {
	_, err := s.db.ExecContext(ctx, `UPDATE file_uploads SET state='verifying',updated=?,error='' WHERE id=? AND state='uploading' AND offset=size`, time.Now().UnixMilli(), id)
	if err != nil {
		return FileUpload{}, err
	}
	return s.FileUpload(ctx, id)
}

func (s *Store) FailFileVerification(ctx context.Context, id, reason string) error {
	now := time.Now().UnixMilli()
	_, err := s.db.ExecContext(ctx, `UPDATE file_uploads SET state='failed',error=?,updated=?,terminal_at=? WHERE id=? AND state='verifying'`, reason, now, now, id)
	return err
}

func (s *Store) PendingFileVerifications(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM file_uploads WHERE state='verifying' ORDER BY updated,id LIMIT 64`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
