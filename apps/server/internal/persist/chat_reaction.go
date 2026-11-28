package persist

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"uniclog.io/sonoryx/internal/domain"
)

func migrateChatReactions(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{
		`CREATE TABLE chat_reactions(message_id INTEGER NOT NULL REFERENCES chat_messages(id) ON DELETE CASCADE,user_id INTEGER NOT NULL REFERENCES users(id),code INTEGER NOT NULL,PRIMARY KEY(message_id,user_id,code)) WITHOUT ROWID`,
		`CREATE INDEX chat_reactions_user ON chat_reactions(user_id)`,
		// Every path that tombstones a message also drops its reactions.
		`CREATE TRIGGER chat_reactions_tombstone AFTER UPDATE OF deleted ON chat_messages WHEN NEW.deleted=1 BEGIN DELETE FROM chat_reactions WHERE message_id=NEW.id; END`,
		`PRAGMA user_version=10`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("migrate chat reactions: %w", err)
		}
	}
	return nil
}

type chatQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// loadChatReactions fills Reactions as seen by user. Callers must have closed
// their result rows, because the store uses a single connection.
func loadChatReactions(ctx context.Context, q chatQuerier, user int64, messages []domain.ChatMessage) error {
	index := make(map[int64]int, len(messages))
	args := []any{user}
	for i := range messages {
		messages[i].Reactions = nil
		if !messages[i].Deleted {
			index[messages[i].ID] = i
			args = append(args, messages[i].ID)
		}
	}
	if len(index) == 0 {
		return nil
	}
	rows, err := q.QueryContext(ctx, `SELECT message_id,code,COUNT(*),MAX(user_id=?) FROM chat_reactions WHERE message_id IN(?`+strings.Repeat(",?", len(index)-1)+`) GROUP BY message_id,code ORDER BY message_id,code`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, count int64
		var code uint8
		var mine bool
		if err := rows.Scan(&id, &code, &count, &mine); err != nil {
			return err
		}
		if count > 65535 {
			count = 65535
		}
		m := &messages[index[id]]
		m.Reactions = append(m.Reactions, domain.ChatReaction{Code: code, Count: uint16(count), Mine: mine})
	}
	return rows.Err()
}

// ReactChat adds or removes the user's reaction and reports whether anything
// changed. A real change takes a new revision, so the existing change log
// delivers it to the other participants.
func (s *Store) ReactChat(ctx context.Context, user int64, target domain.ChatTarget, id int64, code uint8, add bool) (domain.ChatMessage, bool, error) {
	if err := target.Validate(); err != nil {
		return domain.ChatMessage{}, false, err
	}
	if _, ok := domain.ChatReactionEmoji(code); !ok || id <= 0 {
		return domain.ChatMessage{}, false, domain.ChatRejection("некорректная реакция")
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
	query := `DELETE FROM chat_reactions WHERE message_id=? AND user_id=? AND code=?`
	if add {
		query = `INSERT INTO chat_reactions(message_id,user_id,code) VALUES(?,?,?) ON CONFLICT DO NOTHING`
	}
	result, err := tx.ExecContext(ctx, query, id, user, code)
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
