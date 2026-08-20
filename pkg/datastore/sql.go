package datastore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/NicolasHaas/gospeak/pkg/model"
)

const dbTimeLayout = "2006-01-02 15:04:05"

type DB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type baseProvider struct {
	DB
}

func (p *baseProvider) ZeroTime() time.Time {
	return time.Time{}
}

type nonTxProvider struct {
	baseProvider
}

type txProvider struct {
	baseProvider
	tx *sql.Tx
}

func (c *txProvider) Rollback() error {
	return c.tx.Rollback()
}

func (c *txProvider) Commit() error {
	return c.tx.Commit()
}

// datastore provides database access for all GoSpeak entities.
type ProviderFactory struct {
	DB *sql.DB
}

func (sf ProviderFactory) NonTx() DataStore {
	return &nonTxProvider{
		baseProvider: baseProvider{
			DB: sf.DB,
		},
	}
}

func (sf ProviderFactory) Tx(ctx context.Context) (DataStoreTx, error) {
	tx, err := sf.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}

	return &txProvider{
		baseProvider: baseProvider{
			DB: tx,
		},
		tx: tx,
	}, nil
}

// New opens (or creates) a SQLite database and runs migrations.
func NewProviderFactory(dbPath string) (*ProviderFactory, error) {
	DB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("datastore: open DB: %w", err)
	}

	ctx := context.Background()

	// Enable WAL mode for better concurrent read performance
	if _, err := DB.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		_ = DB.Close()
		return nil, fmt.Errorf("datastore: set WAL: %w", err)
	}
	if _, err := DB.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		_ = DB.Close()
		return nil, fmt.Errorf("datastore: enable FK: %w", err)
	}
	// Set busy timeout to avoid "database is locked" under concurrency
	if _, err := DB.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		_ = DB.Close()
		return nil, fmt.Errorf("datastore: set busy_timeout: %w", err)
	}

	s := &ProviderFactory{DB: DB}
	if err := s.migrate(); err != nil {
		_ = DB.Close()
		return nil, fmt.Errorf("datastore: migrate: %w", err)
	}
	return s, nil
}

// Close closes the database connection pool.
func (s *ProviderFactory) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	return s.DB.Close()
}

func (s *ProviderFactory) migrate() error {
	const schema = `
	CREATE TABLE IF NOT EXISTS users (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		username   TEXT    NOT NULL UNIQUE CHECK(length(username) > 0 AND length(username) <= 32),
		role       INTEGER NOT NULL DEFAULT 0 CHECK(role >= 0 AND role <= 2),
		personal_token_hash TEXT NOT NULL DEFAULT '',
		personal_token_created_at TEXT NOT NULL DEFAULT (datetime('now')),
		created_at TEXT    NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE IF NOT EXISTS channels (
		id                 INTEGER PRIMARY KEY AUTOINCREMENT,
		name               TEXT    NOT NULL,
		description        TEXT    NOT NULL DEFAULT '',
		max_users          INTEGER NOT NULL DEFAULT 0,
		parent_id          INTEGER NOT NULL DEFAULT 0,
		is_temp            INTEGER NOT NULL DEFAULT 0,
		allow_sub_channels INTEGER NOT NULL DEFAULT 0,
		created_at         TEXT    NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE IF NOT EXISTS tokens (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		hash          TEXT    NOT NULL UNIQUE,
		role          INTEGER NOT NULL DEFAULT 0,
		channel_scope INTEGER NOT NULL DEFAULT 0,
		created_by    INTEGER NOT NULL DEFAULT 0,
		max_uses      INTEGER NOT NULL DEFAULT 0,
		use_count     INTEGER NOT NULL DEFAULT 0,
		expires_at    TEXT,
		created_at    TEXT    NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE IF NOT EXISTS bans (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id    INTEGER NOT NULL DEFAULT 0,
		ip         TEXT    NOT NULL DEFAULT '',
		reason     TEXT    NOT NULL DEFAULT '',
		banned_by  INTEGER NOT NULL DEFAULT 0,
		expires_at TEXT,
		created_at TEXT    NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE IF NOT EXISTS messages (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		channel_id INTEGER NOT NULL DEFAULT 0,
		sender_id  INTEGER NOT NULL DEFAULT 0,
		body       TEXT    NOT NULL DEFAULT '',
		created_at TEXT    NOT NULL DEFAULT (datetime('now'))
	);
	`
	ctx := context.Background()
	if err := s.ensureSchemaMigrations(ctx); err != nil {
		return err
	}
	currentVersion, err := s.getSchemaVersion(ctx)
	if err != nil {
		return err
	}

	migrations := []struct {
		version      int
		statements   []string
		ignoreErrors bool
	}{
		{
			version:    1,
			statements: []string{schema},
		},
		{
			version: 2,
			statements: []string{
				"ALTER TABLE channels ADD COLUMN parent_id INTEGER NOT NULL DEFAULT 0",
				"ALTER TABLE channels ADD COLUMN is_temp INTEGER NOT NULL DEFAULT 0",
				"ALTER TABLE channels ADD COLUMN allow_sub_channels INTEGER NOT NULL DEFAULT 0",
			},
			ignoreErrors: true,
		},
		{
			version: 3,
			statements: []string{
				"ALTER TABLE users ADD COLUMN personal_token_hash TEXT NOT NULL DEFAULT ''",
				"ALTER TABLE users ADD COLUMN personal_token_created_at TEXT NOT NULL DEFAULT (datetime('now'))",
			},
			ignoreErrors: true,
		},
		{
			version: 4,
			statements: []string{
				"CREATE INDEX IF NOT EXISTS idx_users_personal_token_hash ON users(personal_token_hash)",
			},
			ignoreErrors: true,
		},
		{
			version: 5,
			statements: []string{
				"ALTER TABLE users ADD COLUMN channel_scope INTEGER NOT NULL DEFAULT 0",
			},
		},
		{
			version: 6,
			statements: []string{
				"CREATE UNIQUE INDEX IF NOT EXISTS idx_channels_parent_name ON channels(parent_id, name)",
			},
		},
	}

	for _, m := range migrations {
		if m.version <= currentVersion {
			continue
		}
		for _, stmt := range m.statements {
			if err := s.execMigration(ctx, stmt, m.ignoreErrors); err != nil {
				return err
			}
		}
		if err := s.setSchemaVersion(ctx, m.version); err != nil {
			return err
		}
	}
	return nil
}

func (s *ProviderFactory) ensureSchemaMigrations(ctx context.Context) error {
	if _, err := s.DB.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER NOT NULL)"); err != nil {
		return fmt.Errorf("datastore: create schema_migrations: %w", err)
	}
	var count int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		return fmt.Errorf("datastore: check schema_migrations: %w", err)
	}
	if count == 0 {
		if _, err := s.DB.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES (0)"); err != nil {
			return fmt.Errorf("datastore: init schema_migrations: %w", err)
		}
	}
	return nil
}

func (s *ProviderFactory) getSchemaVersion(ctx context.Context) (int, error) {
	var version int
	if err := s.DB.QueryRowContext(ctx, "SELECT version FROM schema_migrations LIMIT 1").Scan(&version); err != nil {
		return 0, fmt.Errorf("datastore: read schema version: %w", err)
	}
	return version, nil
}

func (s *ProviderFactory) setSchemaVersion(ctx context.Context, version int) error {
	if _, err := s.DB.ExecContext(ctx, "UPDATE schema_migrations SET version = ?", version); err != nil {
		return fmt.Errorf("datastore: update schema version: %w", err)
	}
	return nil
}

func (s *ProviderFactory) execMigration(ctx context.Context, stmt string, ignoreErrors bool) error {
	if _, err := s.DB.ExecContext(ctx, stmt); err != nil {
		if ignoreErrors {
			return nil
		}
		return fmt.Errorf("datastore: migrate: %w", err)
	}
	return nil
}

func formatDBTime(t time.Time) string {
	return t.UTC().Format(dbTimeLayout)
}

func parseDBTime(value string) (time.Time, error) {
	return time.ParseInLocation(dbTimeLayout, value, time.UTC)
}

// ---- Users ----

// CreateUser creates a new user and returns it with the assigned ID.
// It validates the username format and role before inserting.
func (s *baseProvider) CreateUser(username string, role model.Role) (*model.User, error) {
	return s.CreateUserWithChannelScope(username, role, 0)
}

// CreateUserWithChannelScope creates a user whose personal token retains an invite's channel restriction.
func (s *baseProvider) CreateUserWithChannelScope(username string, role model.Role, channelScope int64) (*model.User, error) {
	if err := model.ValidateUsername(username); err != nil {
		return nil, fmt.Errorf("datastore: create user: %w", err)
	}
	if !role.Valid() {
		return nil, fmt.Errorf("datastore: create user: %w", model.ErrInvalidRole)
	}
	res, err := s.ExecContext(
		context.Background(),
		"INSERT INTO users (username, role, channel_scope, personal_token_hash, personal_token_created_at) VALUES (?, ?, ?, ?, ?)",
		username,
		int(role),
		channelScope,
		"",
		formatDBTime(time.Now().UTC()),
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: users.username") {
			return nil, fmt.Errorf("datastore: create user: %w", ErrUsernameTaken)
		}
		return nil, fmt.Errorf("datastore: create user: %w", err)
	}
	id, _ := res.LastInsertId()
	return &model.User{
		ID:           id,
		Username:     username,
		Role:         role,
		ChannelScope: channelScope,
		CreatedAt:    time.Now().UTC(),
	}, nil
}

// GetUserByUsername retrieves a user by username.
func (s *baseProvider) GetUserByUsername(username string) (*model.User, error) {
	u := &model.User{}
	var roleInt int
	var createdAt string
	var personalTokenCreatedAt string
	err := s.QueryRowContext(
		context.Background(),
		"SELECT id, username, role, channel_scope, personal_token_hash, personal_token_created_at, created_at FROM users WHERE username = ?",
		username,
	).Scan(&u.ID, &u.Username, &roleInt, &u.ChannelScope, &u.PersonalTokenHash, &personalTokenCreatedAt, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("datastore: get user: %w", err)
	}
	u.Role = model.Role(roleInt)
	parsed, err := parseDBTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("datastore: get user: %w", err)
	}
	u.CreatedAt = parsed
	u.PersonalTokenCreatedAt, err = parseDBTime(personalTokenCreatedAt)
	if err != nil {
		return nil, fmt.Errorf("datastore: get user: %w", err)
	}
	return u, nil
}

// GetUserByID retrieves a user by ID.
func (s *baseProvider) GetUserByID(id int64) (*model.User, error) {
	u := &model.User{}
	var roleInt int
	var createdAt string
	var personalTokenCreatedAt string
	err := s.QueryRowContext(
		context.Background(),
		"SELECT id, username, role, channel_scope, personal_token_hash, personal_token_created_at, created_at FROM users WHERE id = ?",
		id,
	).Scan(&u.ID, &u.Username, &roleInt, &u.ChannelScope, &u.PersonalTokenHash, &personalTokenCreatedAt, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("datastore: get user: %w", err)
	}
	u.Role = model.Role(roleInt)
	parsed, err := parseDBTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("datastore: get user: %w", err)
	}
	u.CreatedAt = parsed
	u.PersonalTokenCreatedAt, err = parseDBTime(personalTokenCreatedAt)
	if err != nil {
		return nil, fmt.Errorf("datastore: get user: %w", err)
	}
	return u, nil
}

func (s *baseProvider) GetUserByPersonalTokenHash(hash string) (*model.User, error) {
	u := &model.User{}
	var roleInt int
	var createdAt string
	var personalTokenCreatedAt string
	err := s.QueryRowContext(
		context.Background(),
		"SELECT id, username, role, channel_scope, personal_token_hash, personal_token_created_at, created_at FROM users WHERE personal_token_hash = ?",
		hash,
	).Scan(&u.ID, &u.Username, &roleInt, &u.ChannelScope, &u.PersonalTokenHash, &personalTokenCreatedAt, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("datastore: get user by token: %w", err)
	}
	u.Role = model.Role(roleInt)
	u.CreatedAt, err = parseDBTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("datastore: get user by token: %w", err)
	}
	u.PersonalTokenCreatedAt, err = parseDBTime(personalTokenCreatedAt)
	if err != nil {
		return nil, fmt.Errorf("datastore: get user by token: %w", err)
	}
	return u, nil
}

// UpdateUserRole changes a user's role.
func (s *baseProvider) UpdateUserRole(userID int64, role model.Role) error {
	if !role.Valid() {
		return fmt.Errorf("datastore: update user role: %w", model.ErrInvalidRole)
	}
	_, err := s.ExecContext(context.Background(), "UPDATE users SET role = ? WHERE id = ?", int(role), userID)
	if err != nil {
		return fmt.Errorf("datastore: update user role: %w", err)
	}
	return nil
}

func (s *baseProvider) UpdateUserPersonalToken(userID int64, hash string, createdAt time.Time) error {
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	createdAtStr := formatDBTime(createdAt)
	_, err := s.ExecContext(
		context.Background(),
		"UPDATE users SET personal_token_hash = ?, personal_token_created_at = ? WHERE id = ?",
		hash,
		createdAtStr,
		userID,
	)
	if err != nil {
		return fmt.Errorf("datastore: update personal token: %w", err)
	}
	return nil
}

// ListUsers returns all users.
func (s *baseProvider) ListUsers() ([]model.User, error) {
	rows, err := s.QueryContext(context.Background(), "SELECT id, username, role, channel_scope, personal_token_hash, personal_token_created_at, created_at FROM users ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("datastore: list users: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var users []model.User
	for rows.Next() {
		var u model.User
		var roleInt int
		var createdAt string
		var personalTokenCreatedAt string
		if err := rows.Scan(&u.ID, &u.Username, &roleInt, &u.ChannelScope, &u.PersonalTokenHash, &personalTokenCreatedAt, &createdAt); err != nil {
			return nil, fmt.Errorf("datastore: scan user: %w", err)
		}
		u.Role = model.Role(roleInt)
		u.CreatedAt, err = parseDBTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("datastore: scan user: %w", err)
		}
		u.PersonalTokenCreatedAt, err = parseDBTime(personalTokenCreatedAt)
		if err != nil {
			return nil, fmt.Errorf("datastore: scan user: %w", err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// ---- Channels ----

// CreateChannelFull creates a new channel with all options.
func (s *baseProvider) CreateChannel(channel *model.Channel) error {
	if err := channel.Validate(); err != nil {
		return err
	}

	isTempInt := 0
	if channel.IsTemp {
		isTempInt = 1
	}
	allowSubInt := 0
	if channel.AllowSubChannels {
		allowSubInt = 1
	}
	res, err := s.ExecContext(
		context.Background(),
		"INSERT INTO channels (name, description, max_users, parent_id, is_temp, allow_sub_channels) VALUES (?, ?, ?, ?, ?, ?)",
		channel.Name,
		channel.Description,
		channel.MaxUsers,
		channel.ParentID,
		isTempInt,
		allowSubInt,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: channels.parent_id, channels.name") {
			return fmt.Errorf("datastore: create channel: %w", ErrChannelNameTaken)
		}
		return fmt.Errorf("datastore: create channel: %w", err)
	}
	channel.ID, err = res.LastInsertId()
	if err != nil {
		return fmt.Errorf("datastore: create channel last insert id: %w", err)
	}
	channel.CreatedAt = time.Now().UTC()

	return nil
}

// DeleteChannel deletes a channel by ID.
func (s *baseProvider) DeleteChannel(id int64) error {
	_, err := s.ExecContext(context.Background(), "DELETE FROM channels WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("datastore: delete channel: %w", err)
	}
	return nil
}

// ListChannels returns all channels.
func (s *baseProvider) ListChannels() ([]model.Channel, error) {
	rows, err := s.QueryContext(context.Background(), "SELECT id, name, description, max_users, parent_id, is_temp, allow_sub_channels, created_at FROM channels ORDER BY parent_id, id")
	if err != nil {
		return nil, fmt.Errorf("datastore: list channels: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var channels []model.Channel
	for rows.Next() {
		var ch model.Channel
		var createdAt string
		var isTempInt, allowSubInt int
		if err := rows.Scan(&ch.ID, &ch.Name, &ch.Description, &ch.MaxUsers, &ch.ParentID, &isTempInt, &allowSubInt, &createdAt); err != nil {
			return nil, fmt.Errorf("datastore: scan channel: %w", err)
		}
		ch.IsTemp = isTempInt != 0
		ch.AllowSubChannels = allowSubInt != 0
		parsed, err := parseDBTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("datastore: scan channel: %w", err)
		}
		ch.CreatedAt = parsed
		channels = append(channels, ch)
	}
	return channels, rows.Err()
}

// GetChannel retrieves a channel by ID.
func (s *baseProvider) GetChannel(id int64) (*model.Channel, error) {
	ch := &model.Channel{}
	var createdAt string
	var isTempInt, allowSubInt int
	err := s.QueryRowContext(context.Background(), "SELECT id, name, description, max_users, parent_id, is_temp, allow_sub_channels, created_at FROM channels WHERE id = ?", id).
		Scan(&ch.ID, &ch.Name, &ch.Description, &ch.MaxUsers, &ch.ParentID, &isTempInt, &allowSubInt, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("datastore: get channel: %w", err)
	}
	ch.IsTemp = isTempInt != 0
	ch.AllowSubChannels = allowSubInt != 0
	parsed, err := parseDBTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("datastore: get channel: %w", err)
	}
	ch.CreatedAt = parsed
	return ch, nil
}

// GetChannelByNameAndParent retrieves a channel by name and parent ID.
func (s *baseProvider) GetChannelByNameAndParent(name string, parentID int64) (*model.Channel, error) {
	ch := &model.Channel{}
	var createdAt string
	var isTempInt, allowSubInt int
	err := s.QueryRowContext(context.Background(), "SELECT id, name, description, max_users, parent_id, is_temp, allow_sub_channels, created_at FROM channels WHERE name = ? AND parent_id = ?", name, parentID).
		Scan(&ch.ID, &ch.Name, &ch.Description, &ch.MaxUsers, &ch.ParentID, &isTempInt, &allowSubInt, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("datastore: get channel by name: %w", err)
	}
	ch.IsTemp = isTempInt != 0
	ch.AllowSubChannels = allowSubInt != 0
	parsed, err := parseDBTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("datastore: get channel by name: %w", err)
	}
	ch.CreatedAt = parsed
	return ch, nil
}

// ---- Tokens ----

// HasTokens returns true if any tokens exist in the database.
func (s *baseProvider) HasTokens() (bool, error) {
	var count int
	err := s.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM tokens").Scan(&count)
	if err != nil {
		return false, fmt.Errorf("datastore: count tokens: %w", err)
	}
	return count > 0, nil
}

// CreateToken stores a new token (hash only).
func (s *baseProvider) CreateToken(hash string, role model.Role, channelScope int64, createdBy int64, maxUses int, expiresAt time.Time) error {
	var expStr *string
	if !expiresAt.IsZero() {
		s := formatDBTime(expiresAt)
		expStr = &s
	}
	_, err := s.ExecContext(context.Background(),
		"INSERT INTO tokens (hash, role, channel_scope, created_by, max_uses, expires_at) VALUES (?, ?, ?, ?, ?, ?)",
		hash, int(role), channelScope, createdBy, maxUses, expStr)
	if err != nil {
		return fmt.Errorf("datastore: create token: %w", err)
	}
	return nil
}

// ValidateToken checks if a token hash is valid and returns its authorization data.
// It increments the use count atomically.
func (s *txProvider) ValidateToken(hash string) (*model.Token, error) {
	ctx := context.Background()

	var roleInt int
	var channelScope int64
	var maxUses, useCount int
	var expiresAt *string
	err := s.QueryRowContext(ctx,
		"SELECT role, channel_scope, max_uses, use_count, expires_at FROM tokens WHERE hash = ?", hash).
		Scan(&roleInt, &channelScope, &maxUses, &useCount, &expiresAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("datastore: invalid token")
	}
	if err != nil {
		return nil, fmt.Errorf("datastore: validate token: %w", err)
	}

	if expiresAt != nil {
		exp, err := parseDBTime(*expiresAt)
		if err != nil {
			return nil, fmt.Errorf("datastore: validate token: %w", err)
		}
		if time.Now().After(exp) {
			return nil, fmt.Errorf("datastore: token expired")
		}
	}

	if maxUses > 0 && useCount >= maxUses {
		return nil, fmt.Errorf("datastore: token exhausted")
	}

	if channelScope != 0 {
		var exists int
		if err := s.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM channels WHERE id = ?)", channelScope).Scan(&exists); err != nil {
			return nil, fmt.Errorf("datastore: validate token scope: %w", err)
		}
		if exists == 0 {
			return nil, fmt.Errorf("datastore: token channel scope does not exist")
		}
	}

	if _, err := s.ExecContext(ctx, "UPDATE tokens SET use_count = use_count + 1 WHERE hash = ?", hash); err != nil {
		return nil, fmt.Errorf("datastore: increment use: %w", err)
	}

	return &model.Token{Role: model.Role(roleInt), ChannelScope: channelScope}, nil
}

// ---- Bans ----

// CreateBan adds a ban record.
func (s *baseProvider) CreateBan(userID int64, ip, reason string, bannedBy int64, expiresAt time.Time) error {
	var expStr *string
	if !expiresAt.IsZero() {
		es := formatDBTime(expiresAt)
		expStr = &es
	}
	_, err := s.ExecContext(context.Background(),
		"INSERT INTO bans (user_id, ip, reason, banned_by, expires_at) VALUES (?, ?, ?, ?, ?)",
		userID, ip, reason, bannedBy, expStr)
	if err != nil {
		return fmt.Errorf("datastore: create ban: %w", err)
	}
	return nil
}

// IsUserBanned checks if a user ID is currently banned.
func (s *baseProvider) IsUserBanned(userID int64) (bool, error) {
	var count int

	err := s.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM bans WHERE user_id = ? AND (expires_at IS NULL OR expires_at > datetime('now'))",
		userID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("datastore: check ban: %w", err)
	}
	return count > 0, nil
}

// ---- Bans ----

func (s *baseProvider) CreateMessage(message *model.Message) error {
	if err := message.Validate(); err != nil {
		return fmt.Errorf("datastore: message failed validation: %w", err)
	}

	res, err := s.ExecContext(
		context.Background(),
		"INSERT INTO messages (channel_id, sender_id, body) VALUES (?, ?, ?)",
		message.ChannelID, message.SenderID, message.Body)
	if err != nil {
		return fmt.Errorf("datastore: create message: %w", err)
	}
	message.ID, err = res.LastInsertId()
	if err != nil {
		return fmt.Errorf("datastore: create message last insert id: %w", err)
	}
	message.CreatedAt = time.Now().UTC()

	return nil
}

func (s *baseProvider) ListMessages(filters model.MessageFilters) ([]model.Message, error) {
	query := `
		SELECT id, channel_id, sender_id, body, created_at
		FROM messages
		WHERE (? IS NULL OR channel_id = ?)
		AND (? IS NULL OR sender_id = ?)
		ORDER BY id DESC
		LIMIT COALESCE(?, 100)
		OFFSET COALESCE(?, 0)
	`

	rows, err := s.QueryContext(
		context.Background(),
		query,
		filters.LimitToChannelID, filters.LimitToChannelID,
		filters.LimitToSenderID, filters.LimitToSenderID,
		filters.PageSize,
		filters.Offset,
	)
	if err != nil {
		return nil, fmt.Errorf("datastore: list messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var messages []model.Message
	for rows.Next() {
		var m model.Message
		var createdAt string
		if err := rows.Scan(&m.ID, &m.ChannelID, &m.SenderID, &m.Body, &createdAt); err != nil {
			return nil, fmt.Errorf("datastore: scan message: %w", err)
		}
		parsed, err := parseDBTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("datastore: scan channel: %w", err)
		}
		m.CreatedAt = parsed
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

func (s *baseProvider) DeleteMessage(messageID int64) error {
	_, err := s.ExecContext(context.Background(), "DELETE FROM messages WHERE id = ?", messageID)
	if err != nil {
		return fmt.Errorf("datastore: delete message: %w", err)
	}
	return nil
}
