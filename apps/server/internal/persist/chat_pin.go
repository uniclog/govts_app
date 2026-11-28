package persist

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"uniclog.io/sonoryx/internal/domain"
)

func migrateChatPins(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{
		// A conversation is (1, channel, 0) or (2, lower user, higher user); seq orders pins.
		`CREATE TABLE chat_pins(seq INTEGER PRIMARY KEY AUTOINCREMENT,message_id INTEGER NOT NULL UNIQUE REFERENCES chat_messages(id) ON DELETE CASCADE,kind INTEGER NOT NULL CHECK(kind IN (1,2)),target_low INTEGER NOT NULL,target_high INTEGER NOT NULL,pinned_by INTEGER NOT NULL,pinned_at_ms INTEGER NOT NULL)`,
		`CREATE INDEX chat_pins_conversation ON chat_pins(kind,target_low,target_high,seq)`,
		// Every path that tombstones a message also unpins it.
		`CREATE TRIGGER chat_pins_tombstone AFTER UPDATE OF deleted ON chat_messages WHEN NEW.deleted=1 BEGIN DELETE FROM chat_pins WHERE message_id=NEW.id; END`,
		`PRAGMA user_version=12`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("migrate chat pins: %w", err)
		}
	}
	return nil
}

func chatPinKey(user int64, target domain.ChatTarget) []any {
	if target.Kind == domain.ChatChannel {
		return []any{target.Kind, target.ID, 0}
	}
	low, high := user, target.ID
	if low > high {
		low, high = high, low
	}
	return []any{target.Kind, low, high}
}

const chatPinWhere = `p.kind=? AND p.target_low=? AND p.target_high=?`

// PinChat pins or unpins a message of the conversation and reports whether
// anything changed. A real change takes a new message revision, so the change
// log tells the other participants to reload the pin list.
func (s *Store) PinChat(ctx context.Context, user int64, target domain.ChatTarget, id int64, pin bool) (domain.ChatMessage, bool, error) {
	if err := target.Validate(); err != nil {
		return domain.ChatMessage{}, false, err
	}
	if id <= 0 {
		return domain.ChatMessage{}, false, domain.ChatRejection("некорректное закрепление")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ChatMessage{}, false, err
	}
	defer tx.Rollback()
	var banned bool
	if err = tx.QueryRowContext(ctx, `SELECT banned FROM users WHERE id=?`, user).Scan(&banned); err != nil || banned {
		return domain.ChatMessage{}, false, domain.ChatRejection("доступ к чату запрещён")
	}
	where, args := chatWhere(user, target)
	args = append(args, id)
	m, err := scanChat(tx.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chat_messages WHERE `+where+` AND id=?`, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return m, false, domain.ChatRejection("сообщение недоступно")
	}
	if err != nil {
		return m, false, err
	}
	if m.Deleted {
		return m, false, domain.ChatRejection("сообщение удалено")
	}
	key := chatPinKey(user, target)
	var result sql.Result
	if pin {
		var pinned, count int64
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_pins WHERE message_id=?),(SELECT COUNT(*) FROM chat_pins p WHERE `+chatPinWhere+`)`, append([]any{id}, key...)...).Scan(&pinned, &count); err != nil {
			return m, false, err
		}
		if pinned == 0 && count >= domain.MaxChatPins {
			return m, false, domain.ChatRejection(fmt.Sprintf("можно закрепить не больше %d сообщений", domain.MaxChatPins))
		}
		result, err = tx.ExecContext(ctx, `INSERT INTO chat_pins(message_id,kind,target_low,target_high,pinned_by,pinned_at_ms) VALUES(?,?,?,?,?,?) ON CONFLICT(message_id) DO NOTHING`, append(append([]any{id}, key...), user, time.Now().UnixMilli())...)
	} else {
		result, err = tx.ExecContext(ctx, `DELETE FROM chat_pins WHERE message_id=?`, id)
	}
	if err != nil {
		return m, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return m, false, err
	}
	if affected > 0 {
		if _, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO chat_changes(message_id) VALUES(?)`, id); err != nil {
			return m, false, err
		}
		if m, err = scanChat(tx.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chat_messages WHERE id=?`, id)); err != nil {
			return m, false, err
		}
	}
	page := []domain.ChatMessage{m}
	if err = loadChatReactions(ctx, tx, user, page); err != nil {
		return m, false, err
	}
	return page[0], affected > 0, tx.Commit()
}

// ChatPinned lists the conversation's pinned message IDs, the latest pin first.
func (s *Store) ChatPinned(ctx context.Context, user int64, target domain.ChatTarget) (domain.ChatPage, error) {
	p := domain.ChatPage{UserID: user, PinList: true}
	rows, err := s.db.QueryContext(ctx, `SELECT message_id FROM chat_pins p WHERE `+chatPinWhere+` ORDER BY seq DESC LIMIT `+fmt.Sprint(domain.MaxChatPins), chatPinKey(user, target)...)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return p, err
		}
		p.Pinned = append(p.Pinned, id)
	}
	return p, rows.Err()
}

// ChatPinPage returns pinned messages in pin list order, skipping offset pins.
// Reactions are left out: pin previews do not show them.
func (s *Store) ChatPinPage(ctx context.Context, user int64, target domain.ChatTarget, offset int64) (domain.ChatPage, error) {
	p := domain.ChatPage{UserID: user, Cursor: offset}
	rows, err := s.db.QueryContext(ctx, `SELECT `+chatColumns+` FROM chat_pins p JOIN chat_messages ON chat_messages.id=p.message_id WHERE `+chatPinWhere+` ORDER BY p.seq DESC LIMIT 17 OFFSET ?`, append(chatPinKey(user, target), offset)...)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		m, err := scanChat(rows)
		if err != nil {
			return p, err
		}
		if len(p.Messages) == 16 {
			p.HasMore = true
			break
		}
		p.Messages = append(p.Messages, m)
	}
	return p, rows.Err()
}
