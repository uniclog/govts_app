package persist

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"uniclog.io/sonoryx/internal/domain"
)

// Go strings.TrimSpace's Unicode whitespace set, used by SQLite trim below.
const chatWhitespace = "\t\n\v\f\r \u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000"

// Publish attachment revocations through the same change log as author deletion.
// A zero attachmentID reconciles all newly deleted files during cleanup.
func syncDeletedChatFiles(ctx context.Context, tx *sql.Tx, attachmentID int64) error {
	var previous int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM chat_changes`).Scan(&previous); err != nil {
		return err
	}
	filter := `a.deleted=1 AND f.hidden=0`
	files := `deleted=1`
	var args []any
	if attachmentID > 0 {
		filter += ` AND a.id=?`
		files += ` AND id=?`
		args = append(args, attachmentID)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO chat_changes(message_id) SELECT DISTINCT f.message_id FROM chat_message_files f JOIN file_attachments a ON a.id=f.attachment_id WHERE `+filter, args...); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE chat_message_files SET hidden=1 WHERE hidden=0 AND attachment_id IN(SELECT id FROM file_attachments WHERE `+files+`)`, args...); err != nil {
		return err
	}
	// Clearing the last attachment must not leave an unencodable empty message.
	_, err := tx.ExecContext(ctx, `UPDATE chat_messages SET deleted=1,text='' WHERE deleted=0 AND id IN(SELECT message_id FROM chat_changes WHERE seq>?) AND trim(text,?)='' AND NOT EXISTS(SELECT 1 FROM chat_message_files f WHERE f.message_id=chat_messages.id AND f.hidden=0)`, previous, chatWhitespace)
	return err
}

// DeleteChat leaves a durable tombstone and revokes associated files in the same transaction.
// attachmentID=0 deletes the message; a positive ID deletes only that attachment.
func (s *Store) DeleteChat(ctx context.Context, user int64, target domain.ChatTarget, id, attachmentID int64) (domain.ChatMessage, error) {
	if err := target.Validate(); err != nil {
		return domain.ChatMessage{}, err
	}
	if id <= 0 || attachmentID < 0 {
		return domain.ChatMessage{}, domain.ChatRejection("некорректное удаление")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ChatMessage{}, err
	}
	defer tx.Rollback()
	where, args := chatWhere(user, target)
	args = append(args, id, user)
	m, err := scanChat(tx.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chat_messages WHERE `+where+` AND id=? AND sender_id=?`, args...))
	if err == sql.ErrNoRows {
		return m, domain.ChatRejection("можно удалить только своё сообщение")
	}
	if err != nil {
		return m, err
	}
	var banned bool
	if err = tx.QueryRowContext(ctx, `SELECT banned FROM users WHERE id=?`, user).Scan(&banned); err != nil {
		return m, err
	}
	if banned {
		return m, domain.ChatRejection("доступ к чату запрещён")
	}
	page := []domain.ChatMessage{m}
	if err = loadChatReactions(ctx, tx, user, page); err != nil {
		return m, err
	}
	m = page[0]
	if attachmentID != 0 {
		var deleted bool
		if err = tx.QueryRowContext(ctx, `SELECT a.deleted FROM file_attachments a JOIN chat_message_files f ON f.attachment_id=a.id WHERE f.message_id=? AND a.id=? AND a.user_id=?`, id, attachmentID, user).Scan(&deleted); err == sql.ErrNoRows {
			return m, domain.ChatRejection("вложение недоступно")
		}
		if err != nil {
			return m, err
		}
		if deleted || m.Deleted {
			return m, tx.Commit()
		}
	} else if m.Deleted {
		return m, tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, `UPDATE file_attachments SET deleted=1 WHERE id IN(SELECT attachment_id FROM chat_message_files WHERE message_id=?) AND (?=0 OR id=?)`, id, attachmentID, attachmentID); err != nil {
		return m, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE chat_message_files SET hidden=1 WHERE message_id=? AND (?=0 OR attachment_id=?)`, id, attachmentID, attachmentID); err != nil {
		return m, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE file_uploads SET state='expired',terminal_at=? WHERE attachment_id IN(SELECT attachment_id FROM chat_message_files WHERE message_id=?) AND (?=0 OR attachment_id=?) AND state NOT IN('cancelled','expired','failed')`, time.Now().UnixMilli(), id, attachmentID, attachmentID); err != nil {
		return m, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE chat_messages SET deleted=1,text='' WHERE id=? AND (?=0 OR (? AND NOT EXISTS(SELECT 1 FROM chat_message_files f JOIN file_attachments a ON a.id=f.attachment_id WHERE f.message_id=chat_messages.id AND a.deleted=0)))`, id, attachmentID, strings.TrimSpace(m.Text) == ""); err != nil {
		return m, err
	}
	// One retained revision per message bounds the change log to message count.
	if _, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO chat_changes(message_id) VALUES(?)`, id); err != nil {
		return m, err
	}
	m, err = scanChat(tx.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chat_messages WHERE id=?`, id))
	if err != nil {
		return m, err
	}
	page[0] = m
	if err = loadChatReactions(ctx, tx, user, page); err != nil {
		return m, err
	}
	return page[0], tx.Commit()
}

func (s *Store) ChatChanges(ctx context.Context, user int64, target domain.ChatTarget, cursor int64) (domain.ChatPage, error) {
	p := domain.ChatPage{UserID: user, Cursor: cursor}
	where, args := chatWhere(user, target)
	args = append(args, cursor)
	rows, err := s.db.QueryContext(ctx, `SELECT `+chatColumns+` FROM chat_messages JOIN chat_changes c ON c.message_id=chat_messages.id WHERE `+where+` AND c.seq>? ORDER BY c.seq LIMIT 17`, args...)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		m, e := scanChat(rows)
		if e != nil {
			return p, e
		}
		if len(p.Messages) == 16 {
			p.HasMore = true
			break
		}
		p.Messages = append(p.Messages, m)
		p.Cursor = m.Revision
	}
	if err := rows.Err(); err != nil {
		return p, err
	}
	// The store holds one connection; the next query needs it released.
	rows.Close()
	return p, loadChatReactions(ctx, s.db, user, p.Messages)
}

func (s *Store) ChatChangeWatermark(ctx context.Context, user int64, target domain.ChatTarget) (domain.ChatPage, error) {
	p := domain.ChatPage{UserID: user}
	where, args := chatWhere(user, target)
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(c.seq),0) FROM chat_messages JOIN chat_changes c ON c.message_id=chat_messages.id WHERE `+where, args...).Scan(&p.Cursor)
	return p, err
}
