package persist

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
	"uniclog.io/sonoryx/internal/domain"
)

func migrateChat(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{
		`CREATE TABLE chat_dialogs (user_low INTEGER NOT NULL REFERENCES users(id), user_high INTEGER NOT NULL REFERENCES users(id), PRIMARY KEY(user_low,user_high), CHECK(user_low<user_high))`,
		`CREATE INDEX chat_dialogs_high ON chat_dialogs(user_high,user_low)`,
		`CREATE TABLE chat_messages (
		 id INTEGER PRIMARY KEY AUTOINCREMENT,
		 channel_id INTEGER REFERENCES channels(id), sender_id INTEGER NOT NULL REFERENCES users(id), recipient_id INTEGER REFERENCES users(id),
		 client_id BLOB NOT NULL CHECK(length(client_id)=16), sender_name TEXT NOT NULL, text TEXT NOT NULL CHECK(length(CAST(text AS BLOB)) BETWEEN 1 AND 1000), sent_at_ms INTEGER NOT NULL,
		 UNIQUE(sender_id,client_id), CHECK((channel_id IS NULL)!=(recipient_id IS NULL)), CHECK(sender_id!=recipient_id))`,
		`CREATE INDEX chat_channel_history ON chat_messages(channel_id,id) WHERE channel_id IS NOT NULL`,
		`CREATE INDEX chat_direct_out ON chat_messages(sender_id,recipient_id,id) WHERE recipient_id IS NOT NULL`,
		`CREATE INDEX chat_direct_in ON chat_messages(recipient_id,sender_id,id) WHERE recipient_id IS NOT NULL`,
		`CREATE TABLE chat_reads (user_id INTEGER NOT NULL REFERENCES users(id), kind INTEGER NOT NULL CHECK(kind IN (1,2)), target_id INTEGER NOT NULL, message_id INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(user_id,kind,target_id))`,
		`PRAGMA user_version=4`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("migrate chat: %w", err)
		}
	}
	return nil
}

const chatColumns = `id,COALESCE(channel_id,0),sender_id,COALESCE(recipient_id,0),client_id,sender_name,text,sent_at_ms`

type chatScanner interface{ Scan(...any) error }

func scanChat(row chatScanner) (domain.ChatMessage, error) {
	var m domain.ChatMessage
	var clientID []byte
	err := row.Scan(&m.ID, &m.ChannelID, &m.SenderID, &m.RecipientID, &clientID, &m.SenderName, &m.Text, &m.SentAtMS)
	if err != nil {
		return m, err
	}
	if len(clientID) != 16 {
		return m, errors.New("invalid stored message identity")
	}
	copy(m.ClientID[:], clientID)
	return m, nil
}

func chatWhere(user int64, target domain.ChatTarget) (string, []any) {
	if target.Kind == domain.ChatChannel {
		return "channel_id=?", []any{target.ID}
	}
	return "((sender_id=? AND recipient_id=?) OR (sender_id=? AND recipient_id=?))", []any{user, target.ID, target.ID, user}
}

func (s *Store) SaveChat(ctx context.Context, user int64, name string, target domain.ChatTarget, clientID [16]byte, text string) (domain.ChatMessage, error) {
	if err := target.Validate(); err != nil {
		return domain.ChatMessage{}, err
	}
	if err := domain.ValidateChatText(text); err != nil {
		return domain.ChatMessage{}, err
	}
	if user <= 0 || clientID == ([16]byte{}) {
		return domain.ChatMessage{}, errors.New("invalid chat sender")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ChatMessage{}, err
	}
	defer tx.Rollback()
	var banned bool
	if err := tx.QueryRowContext(ctx, `SELECT banned FROM users WHERE id=?`, user).Scan(&banned); err != nil || banned {
		return domain.ChatMessage{}, errors.New("отправка запрещена")
	}
	m, err := scanChat(tx.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chat_messages WHERE sender_id=? AND client_id=?`, user, clientID[:]))
	if err == nil {
		if m.Text != text || (target.Kind == domain.ChatChannel && (m.ChannelID != target.ID || m.RecipientID != 0)) || (target.Kind == domain.ChatDirect && (m.RecipientID != target.ID || m.ChannelID != 0)) {
			return m, errors.New("идентификатор уже использован для другого сообщения")
		}
		return m, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return m, err
	}
	var channel, recipient any
	if target.Kind == domain.ChatChannel {
		channel = target.ID
	} else {
		if target.ID == user {
			return m, errors.New("нельзя отправить сообщение самому себе")
		}
		if err := tx.QueryRowContext(ctx, `SELECT banned FROM users WHERE id=?`, target.ID).Scan(&banned); err != nil || banned {
			return m, errors.New("получатель недоступен")
		}
		recipient = target.ID
		low, high := user, target.ID
		if low > high {
			low, high = high, low
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO chat_dialogs(user_low,user_high) VALUES(?,?) ON CONFLICT DO NOTHING`, low, high); err != nil {
			return m, err
		}
	}
	at := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `INSERT INTO chat_messages(channel_id,sender_id,recipient_id,client_id,sender_name,text,sent_at_ms) VALUES(?,?,?,?,?,?,?)`, channel, user, recipient, clientID[:], name, text, at)
	if err != nil {
		return m, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return m, err
	}
	m = domain.ChatMessage{ID: id, SenderID: user, ClientID: clientID, SenderName: name, Text: text, SentAtMS: at}
	if target.Kind == domain.ChatChannel {
		m.ChannelID = target.ID
	} else {
		m.RecipientID = target.ID
	}
	return m, tx.Commit()
}

func (s *Store) ChatHistory(ctx context.Context, user int64, target domain.ChatTarget, cursor int64, forward bool) (domain.ChatPage, error) {
	p := domain.ChatPage{UserID: user, Cursor: cursor}
	where, args := chatWhere(user, target)
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM chat_messages WHERE `+where, args...).Scan(&p.LatestID); err != nil {
		return p, err
	}
	err := s.db.QueryRowContext(ctx, `SELECT message_id FROM chat_reads WHERE user_id=? AND kind=? AND target_id=?`, user, target.Kind, target.ID).Scan(&p.ReadID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	unreadArgs := append(append([]any(nil), args...), user, p.ReadID, p.LatestID)
	var unread int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chat_messages WHERE `+where+` AND sender_id!=? AND id>? AND id<=?`, unreadArgs...).Scan(&unread); err != nil {
		return p, err
	}
	if unread > int64(^uint32(0)) {
		unread = int64(^uint32(0))
	}
	p.Unread = uint32(unread)
	order := "DESC"
	if forward {
		where += " AND id>?"
		args = append(args, cursor)
		order = "ASC"
	} else if cursor > 0 {
		where += " AND id<?"
		args = append(args, cursor)
	}
	// The observed high watermark bounds this page while new messages arrive.
	where += " AND id<=?"
	args = append(args, p.LatestID)
	rows, err := s.db.QueryContext(ctx, `SELECT `+chatColumns+` FROM chat_messages WHERE `+where+` ORDER BY id `+order+` LIMIT 17`, args...)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		m, err := scanChat(rows)
		if err != nil {
			return p, err
		}
		p.Messages = append(p.Messages, m)
	}
	return p, rows.Err()
}

func (s *Store) ReadChat(ctx context.Context, user int64, target domain.ChatTarget, cursor int64) error {
	where, args := chatWhere(user, target)
	args = append(args, cursor)
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM chat_messages WHERE `+where+` AND id<=?`, args...).Scan(&id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO chat_reads(user_id,kind,target_id,message_id) VALUES(?,?,?,?) ON CONFLICT(user_id,kind,target_id) DO UPDATE SET message_id=MAX(message_id,excluded.message_id)`, user, target.Kind, target.ID, id)
	return err
}

func (s *Store) ChatDialogs(ctx context.Context, user, cursor int64) (domain.ChatPage, error) {
	p := domain.ChatPage{UserID: user, Cursor: cursor}
	rows, err := s.db.QueryContext(ctx, `WITH peers AS (SELECT CASE WHEN user_low=? THEN user_high ELSE user_low END AS peer FROM chat_dialogs WHERE user_low=? OR user_high=?)
	 SELECT u.id,u.display_name,COALESCE((SELECT MAX(id) FROM chat_messages WHERE (sender_id=? AND recipient_id=u.id) OR (sender_id=u.id AND recipient_id=?)),0),COALESCE(r.message_id,0),
	 (SELECT COUNT(*) FROM chat_messages WHERE sender_id=u.id AND recipient_id=? AND id>COALESCE(r.message_id,0))
	 FROM peers JOIN users u ON u.id=peers.peer LEFT JOIN chat_reads r ON r.user_id=? AND r.kind=2 AND r.target_id=u.id WHERE u.id>? ORDER BY u.id LIMIT 17`, user, user, user, user, user, user, user, cursor)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var d domain.ChatDialog
		var unread int64
		if err := rows.Scan(&d.UserID, &d.DisplayName, &d.LatestID, &d.ReadID, &unread); err != nil {
			return p, err
		}
		if unread > int64(^uint32(0)) {
			unread = int64(^uint32(0))
		}
		d.Unread = uint32(unread)
		p.Dialogs = append(p.Dialogs, d)
	}
	return p, rows.Err()
}
