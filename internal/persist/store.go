package persist

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"

	"uniclog.io/sonoryx/internal/domain"
)

const (
	PermissionKick uint8 = 1 << iota
	PermissionBan
	PermissionDrag
	// PermissionAdmin opens the remote administration menu. It is stored in
	// users.admin, apart from the moderation bits in users.permissions.
	PermissionAdmin

	// PrivilegeOwner is a wire-only flag: it marks the server owner in the
	// privileges byte sent to clients and is never stored as a permission.
	PrivilegeOwner uint8 = 1 << 4

	moderationPermissions = PermissionKick | PermissionBan | PermissionDrag
)

// ConsoleActor names the local server console in the audit log.
const ConsoleActor = "local-console"

type Account struct {
	ID          int64
	PublicKey   [32]byte
	DisplayName string
	JoinLevel   uint16
	Permissions uint8
	Banned      bool
	Owner       bool
	BanReason   string
}

// Privileges is the byte sent to the account's clients: permissions plus the
// owner flag. Older clients read only the moderation bits.
func (a Account) Privileges() uint8 {
	if a.Owner {
		return a.Permissions | PrivilegeOwner
	}
	return a.Permissions
}

// ValidPermission reports whether permission is exactly one assignable bit.
func ValidPermission(permission uint8) bool {
	return permission == PermissionKick || permission == PermissionBan || permission == PermissionDrag || permission == PermissionAdmin
}

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.initialize(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) initialize() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, statement := range []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = FULL`,
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure SQLite: %w", err)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version < 0 || version > 9 {
		return fmt.Errorf("unsupported database schema version %d", version)
	}
	if version == 0 {
		for _, statement := range []string{
			`CREATE TABLE users (
				id INTEGER PRIMARY KEY,
				public_key BLOB NOT NULL UNIQUE CHECK(length(public_key)=32),
				display_name TEXT NOT NULL,
				join_level INTEGER NOT NULL DEFAULT 0 CHECK(join_level BETWEEN 0 AND 65535),
				permissions INTEGER NOT NULL DEFAULT 0 CHECK(permissions BETWEEN 0 AND 7),
				banned INTEGER NOT NULL DEFAULT 0 CHECK(banned IN (0,1)),
				owner INTEGER NOT NULL DEFAULT 0 CHECK(owner IN (0,1)),
				ban_reason TEXT NOT NULL DEFAULT '',
				banned_at TEXT,
				created_at TEXT NOT NULL
			)`,
			`CREATE TABLE server_settings (id INTEGER PRIMARY KEY CHECK(id=1), name TEXT NOT NULL, default_channel_id INTEGER NOT NULL)`,
			`CREATE TABLE channels (
				id INTEGER PRIMARY KEY,
				parent_id INTEGER NOT NULL DEFAULT 0,
				name TEXT NOT NULL,
				topic TEXT NOT NULL DEFAULT '',
				description TEXT NOT NULL DEFAULT '',
				position INTEGER NOT NULL DEFAULT 0,
				max_users INTEGER NOT NULL DEFAULT 0,
				min_join_level INTEGER NOT NULL DEFAULT 0 CHECK(min_join_level BETWEEN 0 AND 65535),
				channel_type INTEGER NOT NULL,
				audio_codec INTEGER NOT NULL,
				audio_sample_rate INTEGER NOT NULL,
				audio_channels INTEGER NOT NULL,
				audio_frame_duration_ms INTEGER NOT NULL,
				audio_bitrate INTEGER NOT NULL,
				audio_application INTEGER NOT NULL
			)`,
			`CREATE TABLE audit (id INTEGER PRIMARY KEY, happened_at TEXT NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL, target_user_id INTEGER, detail TEXT NOT NULL DEFAULT '')`,
			`PRAGMA user_version = 3`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migrate database: %w", err)
			}
		}
	} else if version == 1 {
		for _, statement := range []string{
			`ALTER TABLE users ADD COLUMN owner INTEGER NOT NULL DEFAULT 0 CHECK(owner IN (0,1))`,
			`ALTER TABLE users ADD COLUMN ban_reason TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE users ADD COLUMN banned_at TEXT`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migrate database: %w", err)
			}
		}
	}
	if version == 1 || version == 2 {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE server_settings ADD COLUMN default_channel_id INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("migrate default channel: %w", err)
		}
		var initialized int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM server_settings WHERE id=1`).Scan(&initialized); err != nil {
			return err
		}
		if initialized != 0 {
			var defaultID int64
			err := tx.QueryRowContext(ctx, `SELECT id FROM channels WHERE min_join_level=0 AND max_users=0 ORDER BY id LIMIT 1`).Scan(&defaultID)
			if errors.Is(err, sql.ErrNoRows) {
				var maxID int64
				if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM channels`).Scan(&maxID); err != nil {
					return err
				}
				if maxID <= 0 || maxID == int64(^uint64(0)>>1) {
					return errors.New("legacy server has no valid channel for default migration")
				}
				defaultID = maxID + 1
				name := fmt.Sprintf("default-%d", defaultID)
				for suffix := 1; ; suffix++ {
					var existing int
					if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM channels WHERE parent_id=0 AND lower(name)=lower(?)`, name).Scan(&existing); err != nil {
						return err
					}
					if existing == 0 {
						break
					}
					name = fmt.Sprintf("default-%d-%d", defaultID, suffix)
				}
				profile := domain.DefaultAudioProfile()
				_, err = tx.ExecContext(ctx, `INSERT INTO channels(id,parent_id,name,topic,description,position,max_users,min_join_level,channel_type,audio_codec,audio_sample_rate,audio_channels,audio_frame_duration_ms,audio_bitrate,audio_application) VALUES(?,0,?,'','',0,0,0,?,?,?,?,?,?,?)`, defaultID, name, domain.ChannelTypePermanent, profile.Codec, profile.SampleRate, profile.Channels, profile.FrameDurationMS, profile.Bitrate, profile.Application)
			}
			if err != nil {
				return fmt.Errorf("select legacy default channel: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE server_settings SET default_channel_id=? WHERE id=1`, defaultID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `PRAGMA user_version = 3`); err != nil {
			return err
		}
	}
	if version < 4 {
		if err := migrateChat(ctx, tx); err != nil {
			return err
		}
	}
	if version < 5 {
		for _, statement := range []string{
			`ALTER TABLE users ADD COLUMN admin INTEGER NOT NULL DEFAULT 0 CHECK(admin IN (0,1))`,
			`PRAGMA user_version = 5`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migrate admin permission: %w", err)
			}
		}
	}
	if version < 6 {
		for _, statement := range []string{
			`ALTER TABLE users ADD COLUMN deleted INTEGER NOT NULL DEFAULT 0 CHECK(deleted IN (0,1))`,
			`PRAGMA user_version = 6`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migrate account deletion: %w", err)
			}
		}
	}
	if version < 7 {
		if err := migrateFiles(ctx, tx); err != nil {
			return err
		}
	}
	if version < 8 {
		for _, statement := range []string{
			`ALTER TABLE file_uploads ADD COLUMN terminal_at INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE file_uploads ADD COLUMN error TEXT NOT NULL DEFAULT ''`,
			`UPDATE file_uploads SET terminal_at=CAST(strftime('%s','now') AS INTEGER)*1000 WHERE state IN('cancelled','expired','failed')`,
			`CREATE INDEX file_uploads_terminal ON file_uploads(terminal_at) WHERE terminal_at>0`,
			`PRAGMA user_version=8`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migrate file lifecycle: %w", err)
			}
		}
	}
	if version < 9 {
		for _, statement := range []string{
			`ALTER TABLE chat_messages ADD COLUMN deleted INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE chat_message_files ADD COLUMN hidden INTEGER NOT NULL DEFAULT 0`,
			`CREATE TABLE chat_changes(seq INTEGER PRIMARY KEY AUTOINCREMENT,message_id INTEGER NOT NULL UNIQUE REFERENCES chat_messages(id) ON DELETE CASCADE)`,
			`PRAGMA user_version=9`,
		} {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migrate chat deletion: %w", err)
			}
		}
	}
	if version < 10 {
		if err = migrateChatReactions(ctx, tx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const accountColumns = `id, public_key, display_name, join_level, permissions, admin, banned, owner, ban_reason`

type accountScanner interface{ Scan(...any) error }

func scanAccount(row accountScanner) (Account, error) {
	var account Account
	var key []byte
	var level, permissions, admin, banned, owner int
	err := row.Scan(&account.ID, &key, &account.DisplayName, &level, &permissions, &admin, &banned, &owner, &account.BanReason)
	if err != nil {
		return Account{}, err
	}
	if len(key) != 32 || level < 0 || level > 65535 || permissions < 0 || permissions > int(moderationPermissions) {
		return Account{}, errors.New("invalid account in database")
	}
	copy(account.PublicKey[:], key)
	account.JoinLevel = uint16(level)
	account.Permissions = uint8(permissions)
	if admin != 0 {
		account.Permissions |= PermissionAdmin
	}
	account.Banned = banned != 0
	account.Owner = owner != 0
	return account, nil
}

// FindOrCreateAccount returns the account for key, creating it on first use. A
// known account takes the new display name unless it is banned: a banned
// account keeps the name it was banned under.
func (s *Store) FindOrCreateAccount(ctx context.Context, key [32]byte, name string) (Account, error) {
	if name == "" {
		return Account{}, errors.New("account display name is required")
	}
	if key == ([32]byte{}) {
		return Account{}, errors.New("account public key is required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO users(public_key, display_name, created_at) VALUES(?,?,?) ON CONFLICT(public_key) DO UPDATE SET display_name=excluded.display_name WHERE banned=0`, key[:], name, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return Account{}, err
	}
	return scanAccount(s.db.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM users WHERE public_key=?`, key[:]))
}

// AccountExists reports whether an account is registered for key.
func (s *Store) AccountExists(ctx context.Context, key [32]byte) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM users WHERE public_key=?`, key[:]).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) Account(ctx context.Context, id int64) (Account, error) {
	return scanAccount(s.db.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM users WHERE id=? AND deleted=0`, id))
}

func (s *Store) Accounts(ctx context.Context) ([]Account, error) {
	return s.queryAccounts(ctx, `SELECT `+accountColumns+` FROM users WHERE deleted=0 ORDER BY id`)
}

// AccountsPage returns up to limit accounts with id greater than afterID, in
// id order.
func (s *Store) AccountsPage(ctx context.Context, afterID int64, limit int) ([]Account, error) {
	if afterID < 0 || limit <= 0 {
		return nil, errors.New("invalid account page")
	}
	return s.queryAccounts(ctx, `SELECT `+accountColumns+` FROM users WHERE id>? AND deleted=0 ORDER BY id LIMIT ?`, afterID, limit)
}

func (s *Store) queryAccounts(ctx context.Context, query string, args ...any) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []Account
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

func (s *Store) SetJoinLevel(ctx context.Context, id int64, level uint16) error {
	return s.SetJoinLevelBy(ctx, ConsoleActor, id, level)
}

// SetJoinLevelBy sets the account join level and records actor in the audit log.
func (s *Store) SetJoinLevelBy(ctx context.Context, actor string, id int64, level uint16) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE users SET join_level=? WHERE id=? AND deleted=0`, level, id)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("account %d not found", id)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit(happened_at,actor,action,target_user_id,detail) VALUES(?,?,?,?,?)`, time.Now().UTC().Format(time.RFC3339Nano), actor, "set-join-level", id, fmt.Sprint(level)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetPermission(ctx context.Context, id int64, permission uint8, allowed bool) (Account, error) {
	return s.SetPermissionBy(ctx, ConsoleActor, id, permission, allowed)
}

// SetPermissionBy grants or revokes one permission bit and records actor in
// the audit log.
func (s *Store) SetPermissionBy(ctx context.Context, actor string, id int64, permission uint8, allowed bool) (Account, error) {
	if !ValidPermission(permission) {
		return Account{}, errors.New("invalid server permission")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	var result sql.Result
	if permission == PermissionAdmin {
		result, err = tx.ExecContext(ctx, `UPDATE users SET admin=? WHERE id=? AND deleted=0`, allowed, id)
	} else {
		var updated uint8
		if allowed {
			updated = permission
		}
		result, err = tx.ExecContext(ctx, `UPDATE users SET permissions=(permissions & ?) | ? WHERE id=? AND deleted=0`, int(^permission&moderationPermissions), updated, id)
	}
	if err != nil {
		return Account{}, err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return Account{}, fmt.Errorf("account %d not found", id)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit(happened_at,actor,action,target_user_id,detail) VALUES(?,?,?,?,?)`, time.Now().UTC().Format(time.RFC3339Nano), actor, "set-permission", id, fmt.Sprintf("%d=%t", permission, allowed)); err != nil {
		return Account{}, err
	}
	if err := tx.Commit(); err != nil {
		return Account{}, err
	}
	return s.Account(ctx, id)
}

func (s *Store) SetBan(ctx context.Context, actor string, id int64, banned bool, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var at any
	if banned {
		at = time.Now().UTC().Format(time.RFC3339Nano)
	}
	result, err := tx.ExecContext(ctx, `UPDATE users SET banned=?,ban_reason=?,banned_at=? WHERE id=? AND owner=0 AND deleted=0`, banned, reason, at, id)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("account %d not found or protected", id)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit(happened_at,actor,action,target_user_id,detail) VALUES(?,?,?,?,?)`, time.Now().UTC().Format(time.RFC3339Nano), actor, "set-ban", id, reason); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteAccountBy forgets an account and records actor in the audit log. The
// row stays as a hidden record so channel messages keep their sender; its key
// is replaced with random bytes and its rights are cleared, so the old key
// registers a new account on its next connection. Direct messages, dialogs
// and read markers of the account are removed. The owner and banned accounts
// cannot be deleted: deletion would lift the ban.
func (s *Store) DeleteAccountBy(ctx context.Context, actor string, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var name string
	var owner, banned bool
	if err := tx.QueryRowContext(ctx, `SELECT display_name,owner,banned FROM users WHERE id=? AND deleted=0`, id).Scan(&name, &owner, &banned); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("account %d not found", id)
	} else if err != nil {
		return err
	}
	if owner {
		return errors.New("the owner account cannot be deleted")
	}
	if banned {
		return errors.New("a banned account cannot be deleted; unban it first")
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return err
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`UPDATE users SET public_key=?,join_level=0,permissions=0,admin=0,deleted=1 WHERE id=?`, []any{key[:], id}},
		{`DELETE FROM chat_messages WHERE recipient_id=? OR (sender_id=? AND recipient_id IS NOT NULL)`, []any{id, id}},
		{`INSERT OR REPLACE INTO chat_changes(message_id) SELECT DISTINCT message_id FROM chat_reactions WHERE user_id=?`, []any{id}},
		{`DELETE FROM chat_reactions WHERE user_id=?`, []any{id}},
		{`DELETE FROM chat_dialogs WHERE user_low=? OR user_high=?`, []any{id, id}},
		{`DELETE FROM chat_reads WHERE user_id=? OR (kind=2 AND target_id=?)`, []any{id, id}},
		{`INSERT INTO audit(happened_at,actor,action,target_user_id,detail) VALUES(?,?,?,?,?)`, []any{time.Now().UTC().Format(time.RFC3339Nano), actor, "delete-account", id, name}},
	} {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetOwner(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE users SET owner=1 WHERE id=? AND banned=0 AND deleted=0`, id)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("account %d not found or banned", id)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit(happened_at,actor,action,target_user_id) VALUES(?,?,?,?)`, time.Now().UTC().Format(time.RFC3339Nano), "local-console", "set-owner", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) LogAction(ctx context.Context, actor string, action string, targetID int64, detail string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit(happened_at,actor,action,target_user_id,detail) VALUES(?,?,?,?,?)`, time.Now().UTC().Format(time.RFC3339Nano), actor, action, targetID, detail)
	return err
}

type AuditEntry struct {
	ID           int64
	HappenedAt   time.Time
	Actor        string
	Action       string
	TargetUserID int64
	Detail       string
}

// AuditPage returns up to limit audit entries older than beforeID, newest
// first; beforeID 0 starts from the newest entry.
func (s *Store) AuditPage(ctx context.Context, beforeID int64, limit int) ([]AuditEntry, error) {
	if beforeID < 0 || limit <= 0 {
		return nil, errors.New("invalid audit page")
	}
	if beforeID == 0 {
		beforeID = int64(^uint64(0) >> 1)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,happened_at,actor,action,COALESCE(target_user_id,0),detail FROM audit WHERE id<? ORDER BY id DESC LIMIT ?`, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []AuditEntry
	for rows.Next() {
		var entry AuditEntry
		var happened string
		if err := rows.Scan(&entry.ID, &happened, &entry.Actor, &entry.Action, &entry.TargetUserID, &entry.Detail); err != nil {
			return nil, err
		}
		entry.HappenedAt, _ = time.Parse(time.RFC3339Nano, happened)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

type ServerState struct {
	Name             string
	DefaultChannelID domain.ChannelID
	Channels         []domain.Channel
}

func (s *Store) LoadServer(ctx context.Context) (ServerState, bool, error) {
	var state ServerState
	err := s.db.QueryRowContext(ctx, `SELECT name,default_channel_id FROM server_settings WHERE id=1`).Scan(&state.Name, &state.DefaultChannelID)
	if errors.Is(err, sql.ErrNoRows) {
		return state, false, nil
	}
	if err != nil {
		return state, false, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,parent_id,name,topic,description,position,max_users,min_join_level,channel_type,audio_codec,audio_sample_rate,audio_channels,audio_frame_duration_ms,audio_bitrate,audio_application FROM channels ORDER BY id`)
	if err != nil {
		return state, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var channel domain.Channel
		var id, parent, kind, codec, application int64
		var position, maxUsers, minLevel, sampleRate, audioChannels, frameMS, bitrate int64
		if err := rows.Scan(&id, &parent, &channel.Name, &channel.Topic, &channel.Description, &position, &maxUsers, &minLevel, &kind, &codec, &sampleRate, &audioChannels, &frameMS, &bitrate, &application); err != nil {
			return ServerState{}, false, err
		}
		if id <= 0 || parent < 0 || position < 0 || position > 1<<32-1 || maxUsers < 0 || maxUsers > 1<<32-1 || minLevel < 0 || minLevel > 65535 || sampleRate < 0 || sampleRate > 1<<32-1 || audioChannels < 0 || audioChannels > 255 || frameMS < 0 || frameMS > 65535 || bitrate < 0 || bitrate > 1<<32-1 {
			return ServerState{}, false, errors.New("invalid channel numeric field in database")
		}
		channel.ID, channel.ParentID = domain.ChannelID(id), domain.ChannelID(parent)
		channel.Position, channel.MaxUsers, channel.MinJoinLevel = uint32(position), uint32(maxUsers), uint16(minLevel)
		channel.Type = domain.ChannelType(kind)
		channel.Audio = domain.AudioProfile{Codec: domain.AudioCodec(codec), SampleRate: uint32(sampleRate), Channels: uint8(audioChannels), FrameDurationMS: uint16(frameMS), Bitrate: uint32(bitrate), Application: domain.OpusApplication(application)}
		state.Channels = append(state.Channels, channel)
	}
	return state, true, rows.Err()
}

func (s *Store) SaveInitialServer(ctx context.Context, state ServerState) error {
	if state.Name == "" || len(state.Channels) == 0 || state.DefaultChannelID == 0 {
		return errors.New("initial server state is empty")
	}
	foundDefault := false
	for _, channel := range state.Channels {
		if channel.ID == state.DefaultChannelID && channel.MinJoinLevel == 0 && channel.MaxUsers == 0 {
			foundDefault = true
		}
	}
	if !foundDefault {
		return errors.New("default channel must exist and be unrestricted")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO server_settings(id,name,default_channel_id) VALUES(1,?,?)`, state.Name, state.DefaultChannelID); err != nil {
		return fmt.Errorf("server already initialized: %w", err)
	}
	for _, c := range state.Channels {
		_, err := tx.ExecContext(ctx, `INSERT INTO channels(id,parent_id,name,topic,description,position,max_users,min_join_level,channel_type,audio_codec,audio_sample_rate,audio_channels,audio_frame_duration_ms,audio_bitrate,audio_application) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, c.ID, c.ParentID, c.Name, c.Topic, c.Description, c.Position, c.MaxUsers, c.MinJoinLevel, c.Type, c.Audio.Codec, c.Audio.SampleRate, c.Audio.Channels, c.Audio.FrameDurationMS, c.Audio.Bitrate, c.Audio.Application)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetChannelJoinLevel(ctx context.Context, channelID domain.ChannelID, level uint16) error {
	return s.SetChannelJoinLevelBy(ctx, ConsoleActor, channelID, level)
}

// SetChannelJoinLevelBy sets the channel threshold and records actor in the
// audit log.
func (s *Store) SetChannelJoinLevelBy(ctx context.Context, actor string, channelID domain.ChannelID, level uint16) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var defaultID domain.ChannelID
	if err := tx.QueryRowContext(ctx, `SELECT default_channel_id FROM server_settings WHERE id=1`).Scan(&defaultID); err != nil {
		return err
	}
	if channelID == defaultID && level != 0 {
		return errors.New("default channel join level must remain zero")
	}
	result, err := tx.ExecContext(ctx, `UPDATE channels SET min_join_level=? WHERE id=?`, level, channelID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("channel %d not found", channelID)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit(happened_at,actor,action,detail) VALUES(?,?,?,?)`, time.Now().UTC().Format(time.RFC3339Nano), actor, "set-channel-join-level", fmt.Sprintf("%d=%d", channelID, level)); err != nil {
		return err
	}
	return tx.Commit()
}

// InsertChannelBy stores a channel created at runtime and records actor in
// the audit log.
func (s *Store) InsertChannelBy(ctx context.Context, actor string, c domain.Channel) error {
	if c.ID == 0 {
		return errors.New("channel ID is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO channels(id,parent_id,name,topic,description,position,max_users,min_join_level,channel_type,audio_codec,audio_sample_rate,audio_channels,audio_frame_duration_ms,audio_bitrate,audio_application) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, c.ID, c.ParentID, c.Name, c.Topic, c.Description, c.Position, c.MaxUsers, c.MinJoinLevel, c.Type, c.Audio.Codec, c.Audio.SampleRate, c.Audio.Channels, c.Audio.FrameDurationMS, c.Audio.Bitrate, c.Audio.Application); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit(happened_at,actor,action,detail) VALUES(?,?,?,?)`, time.Now().UTC().Format(time.RFC3339Nano), actor, "create-channel", channelAuditDetail(c)); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateChannelBy replaces the settings and place of a channel and records
// actor in the audit log. The default channel stays unrestricted.
func (s *Store) UpdateChannelBy(ctx context.Context, actor string, c domain.Channel) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var defaultID domain.ChannelID
	if err := tx.QueryRowContext(ctx, `SELECT default_channel_id FROM server_settings WHERE id=1`).Scan(&defaultID); err != nil {
		return err
	}
	if c.ID == defaultID && (c.MinJoinLevel != 0 || c.MaxUsers != 0) {
		return errors.New("default channel must remain unrestricted")
	}
	result, err := tx.ExecContext(ctx, `UPDATE channels SET parent_id=?,name=?,topic=?,description=?,position=?,max_users=?,min_join_level=?,channel_type=?,audio_codec=?,audio_sample_rate=?,audio_channels=?,audio_frame_duration_ms=?,audio_bitrate=?,audio_application=? WHERE id=?`, c.ParentID, c.Name, c.Topic, c.Description, c.Position, c.MaxUsers, c.MinJoinLevel, c.Type, c.Audio.Codec, c.Audio.SampleRate, c.Audio.Channels, c.Audio.FrameDurationMS, c.Audio.Bitrate, c.Audio.Application, c.ID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("channel %d not found", c.ID)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit(happened_at,actor,action,detail) VALUES(?,?,?,?)`, time.Now().UTC().Format(time.RFC3339Nano), actor, "update-channel", channelAuditDetail(c)); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteChannelBy removes a channel without subchannels together with its
// chat history and read markers, and records actor in the audit log. The
// default channel cannot be removed.
func (s *Store) DeleteChannelBy(ctx context.Context, actor string, id domain.ChannelID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var defaultID domain.ChannelID
	if err := tx.QueryRowContext(ctx, `SELECT default_channel_id FROM server_settings WHERE id=1`).Scan(&defaultID); err != nil {
		return err
	}
	if id == defaultID {
		return errors.New("default channel cannot be deleted")
	}
	var name string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM channels WHERE id=?`, id).Scan(&name); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("channel %d not found", id)
	} else if err != nil {
		return err
	}
	var children int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM channels WHERE parent_id=?`, id).Scan(&children); err != nil {
		return err
	}
	if children != 0 {
		return errors.New("channel has subchannels")
	}
	for _, q := range []string{
		`DELETE FROM chat_reads WHERE kind=1 AND target_id=?`,
		`DELETE FROM chat_messages WHERE channel_id=?`,
		`DELETE FROM channels WHERE id=?`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit(happened_at,actor,action,detail) VALUES(?,?,?,?)`, time.Now().UTC().Format(time.RFC3339Nano), actor, "delete-channel", fmt.Sprintf("id=%d name=%q", id, name)); err != nil {
		return err
	}
	return tx.Commit()
}

func channelAuditDetail(c domain.Channel) string {
	return fmt.Sprintf("id=%d parent=%d pos=%d name=%q max=%d level=%d bitrate=%d", c.ID, c.ParentID, c.Position, c.Name, c.MaxUsers, c.MinJoinLevel, c.Audio.Bitrate)
}
