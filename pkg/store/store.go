// Package store provides SQLite-backed persistence for users, channels, tokens, and bans.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/NicolasHaas/gospeak/pkg/model"
)

const dbTimeLayout = "2006-01-02 15:04:05"

// Store provides database access for all GoSpeak entities.
type Store struct {
	db *sql.DB
}

// New opens (or creates) a SQLite database and runs migrations.
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("store: open db: %w", err)
	}

	ctx := context.Background()

	// Enable WAL mode for better concurrent read performance
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: set WAL: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: enable FK: %w", err)
	}
	// Set busy timeout to avoid "database is locked" under concurrency
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: set busy_timeout: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return s, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// ZeroTime returns the zero time value (used for no-expiry tokens).
func (s *Store) ZeroTime() time.Time {
	return time.Time{}
}

func (s *Store) migrate() error {
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

func (s *Store) ensureSchemaMigrations(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER NOT NULL)"); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		return fmt.Errorf("store: check schema_migrations: %w", err)
	}
	if count == 0 {
		if _, err := s.db.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES (0)"); err != nil {
			return fmt.Errorf("store: init schema_migrations: %w", err)
		}
	}
	return nil
}

func (s *Store) getSchemaVersion(ctx context.Context) (int, error) {
	var version int
	if err := s.db.QueryRowContext(ctx, "SELECT version FROM schema_migrations LIMIT 1").Scan(&version); err != nil {
		return 0, fmt.Errorf("store: read schema version: %w", err)
	}
	return version, nil
}

func (s *Store) setSchemaVersion(ctx context.Context, version int) error {
	if _, err := s.db.ExecContext(ctx, "UPDATE schema_migrations SET version = ?", version); err != nil {
		return fmt.Errorf("store: update schema version: %w", err)
	}
	return nil
}

func (s *Store) execMigration(ctx context.Context, stmt string, ignoreErrors bool) error {
	if _, err := s.db.ExecContext(ctx, stmt); err != nil {
		if ignoreErrors {
			return nil
		}
		return fmt.Errorf("store: migrate: %w", err)
	}
	return nil
}

func formatDBTime(t time.Time) string {
	return t.UTC().Format(dbTimeLayout)
}

func parseDBTime(value string) (time.Time, error) {
	return time.ParseInLocation(dbTimeLayout, value, time.UTC)
}

func parseDBTimePtr(value sql.NullString) (time.Time, error) {
	if !value.Valid || value.String == "" {
		return time.Time{}, nil
	}
	return parseDBTime(value.String)
}

// ---- Users ----

// CreateUser creates a new user and returns it with the assigned ID.
// It validates the username format and role before inserting.
func (s *Store) CreateUser(username string, role model.Role) (*model.User, error) {
	if err := model.ValidateUsername(username); err != nil {
		return nil, fmt.Errorf("store: create user: %w", err)
	}
	if !role.Valid() {
		return nil, fmt.Errorf("store: create user: %w", model.ErrInvalidRole)
	}
	res, err := s.db.ExecContext(context.Background(), "INSERT INTO users (username, role, personal_token_hash, personal_token_created_at) VALUES (?, ?, ?, ?)", username, int(role), "", formatDBTime(time.Now().UTC()))
	if err != nil {
		return nil, fmt.Errorf("store: create user: %w", err)
	}
	id, _ := res.LastInsertId()
	return &model.User{
		ID:        id,
		Username:  username,
		Role:      role,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// GetUserByID retrieves a user by ID.
func (s *Store) GetUserByID(id int64) (*model.User, error) {
	u := &model.User{}
	var roleInt int
	var createdAt string
	var personalTokenCreatedAt string
	err := s.db.QueryRowContext(context.Background(), "SELECT id, username, role, personal_token_hash, personal_token_created_at, created_at FROM users WHERE id = ?", id).
		Scan(&u.ID, &u.Username, &roleInt, &u.PersonalTokenHash, &personalTokenCreatedAt, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get user: %w", err)
	}
	u.Role = model.Role(roleInt)
	parsed, err := parseDBTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("store: get user: %w", err)
	}
	u.CreatedAt = parsed
	u.PersonalTokenCreatedAt, err = parseDBTime(personalTokenCreatedAt)
	if err != nil {
		return nil, fmt.Errorf("store: get user: %w", err)
	}
	return u, nil
}

// GetUserByPersonalTokenHash retrieves a user by personal token hash.
func (s *Store) GetUserByPersonalTokenHash(hash string) (*model.User, error) {
	u := &model.User{}
	var roleInt int
	var createdAt string
	var personalTokenCreatedAt string
	err := s.db.QueryRowContext(context.Background(), "SELECT id, username, role, personal_token_hash, personal_token_created_at, created_at FROM users WHERE personal_token_hash = ?", hash).
		Scan(&u.ID, &u.Username, &roleInt, &u.PersonalTokenHash, &personalTokenCreatedAt, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get user by token: %w", err)
	}
	u.Role = model.Role(roleInt)
	parsed, err := parseDBTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("store: get user by token: %w", err)
	}
	u.CreatedAt = parsed
	u.PersonalTokenCreatedAt, err = parseDBTime(personalTokenCreatedAt)
	if err != nil {
		return nil, fmt.Errorf("store: get user by token: %w", err)
	}
	return u, nil
}

// UpdateUserRole changes a user's role.
func (s *Store) UpdateUserRole(userID int64, role model.Role) error {
	if !role.Valid() {
		return fmt.Errorf("store: update user role: %w", model.ErrInvalidRole)
	}
	_, err := s.db.ExecContext(context.Background(), "UPDATE users SET role = ? WHERE id = ?", int(role), userID)
	if err != nil {
		return fmt.Errorf("store: update user role: %w", err)
	}
	return nil
}

// UpdateUserPersonalToken sets the personal token hash and timestamp for a user.
func (s *Store) UpdateUserPersonalToken(userID int64, hash string, createdAt time.Time) error {
	var createdAtStr *string
	if !createdAt.IsZero() {
		value := formatDBTime(createdAt)
		createdAtStr = &value
	}
	_, err := s.db.ExecContext(context.Background(), "UPDATE users SET personal_token_hash = ?, personal_token_created_at = ? WHERE id = ?", hash, createdAtStr, userID)
	if err != nil {
		return fmt.Errorf("store: update personal token: %w", err)
	}
	return nil
}

// ListUsers returns all users.
func (s *Store) ListUsers() ([]model.User, error) {
	rows, err := s.db.QueryContext(context.Background(), "SELECT id, username, role, personal_token_hash, personal_token_created_at, created_at FROM users ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var users []model.User
	for rows.Next() {
		var u model.User
		var roleInt int
		var createdAt string
		var personalTokenCreatedAt string
		if err := rows.Scan(&u.ID, &u.Username, &roleInt, &u.PersonalTokenHash, &personalTokenCreatedAt, &createdAt); err != nil {
			return nil, fmt.Errorf("store: scan user: %w", err)
		}
		u.Role = model.Role(roleInt)
		parsed, err := parseDBTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("store: scan user: %w", err)
		}
		u.CreatedAt = parsed
		u.PersonalTokenCreatedAt, err = parseDBTime(personalTokenCreatedAt)
		if err != nil {
			return nil, fmt.Errorf("store: scan user: %w", err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// ---- Channels ----

// CreateChannelFull creates a new channel with all options.
func (s *Store) CreateChannel(channel *model.Channel) error {
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
	res, err := s.db.ExecContext(
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
		return fmt.Errorf("store: create channel: %w", err)
	}
	channel.ID, _ = res.LastInsertId()
	channel.CreatedAt = time.Now().UTC()

	return nil
}

// DeleteChannel deletes a channel by ID.
func (s *Store) DeleteChannel(id int64) error {
	_, err := s.db.ExecContext(context.Background(), "DELETE FROM channels WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("store: delete channel: %w", err)
	}
	return nil
}

// ListChannels returns all channels.
func (s *Store) ListChannels() ([]model.Channel, error) {
	rows, err := s.db.QueryContext(context.Background(), "SELECT id, name, description, max_users, parent_id, is_temp, allow_sub_channels, created_at FROM channels ORDER BY parent_id, id")
	if err != nil {
		return nil, fmt.Errorf("store: list channels: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var channels []model.Channel
	for rows.Next() {
		var ch model.Channel
		var createdAt string
		var isTempInt, allowSubInt int
		if err := rows.Scan(&ch.ID, &ch.Name, &ch.Description, &ch.MaxUsers, &ch.ParentID, &isTempInt, &allowSubInt, &createdAt); err != nil {
			return nil, fmt.Errorf("store: scan channel: %w", err)
		}
		ch.IsTemp = isTempInt != 0
		ch.AllowSubChannels = allowSubInt != 0
		parsed, err := parseDBTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("store: scan channel: %w", err)
		}
		ch.CreatedAt = parsed
		channels = append(channels, ch)
	}
	return channels, rows.Err()
}

// GetChannel retrieves a channel by ID.
func (s *Store) GetChannel(id int64) (*model.Channel, error) {
	ch := &model.Channel{}
	var createdAt string
	var isTempInt, allowSubInt int
	err := s.db.QueryRowContext(context.Background(), "SELECT id, name, description, max_users, parent_id, is_temp, allow_sub_channels, created_at FROM channels WHERE id = ?", id).
		Scan(&ch.ID, &ch.Name, &ch.Description, &ch.MaxUsers, &ch.ParentID, &isTempInt, &allowSubInt, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get channel: %w", err)
	}
	ch.IsTemp = isTempInt != 0
	ch.AllowSubChannels = allowSubInt != 0
	parsed, err := parseDBTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("store: get channel: %w", err)
	}
	ch.CreatedAt = parsed
	return ch, nil
}

// GetChannelByNameAndParent retrieves a channel by name and parent ID.
func (s *Store) GetChannelByNameAndParent(name string, parentID int64) (*model.Channel, error) {
	ch := &model.Channel{}
	var createdAt string
	var isTempInt, allowSubInt int
	err := s.db.QueryRowContext(context.Background(), "SELECT id, name, description, max_users, parent_id, is_temp, allow_sub_channels, created_at FROM channels WHERE name = ? AND parent_id = ?", name, parentID).
		Scan(&ch.ID, &ch.Name, &ch.Description, &ch.MaxUsers, &ch.ParentID, &isTempInt, &allowSubInt, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get channel by name: %w", err)
	}
	ch.IsTemp = isTempInt != 0
	ch.AllowSubChannels = allowSubInt != 0
	parsed, err := parseDBTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("store: get channel by name: %w", err)
	}
	ch.CreatedAt = parsed
	return ch, nil
}

// ---- Tokens ----

// HasTokens returns true if any tokens exist in the database.
func (s *Store) HasTokens() (bool, error) {
	var count int
	err := s.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM tokens").Scan(&count)
	if err != nil {
		return false, fmt.Errorf("store: count tokens: %w", err)
	}
	return count > 0, nil
}

// CreateToken stores a new token (hash only).
func (s *Store) CreateToken(hash string, role model.Role, channelScope int64, createdBy int64, maxUses int, expiresAt time.Time) error {
	var expStr *string
	if !expiresAt.IsZero() {
		s := formatDBTime(expiresAt)
		expStr = &s
	}
	_, err := s.db.ExecContext(context.Background(),
		"INSERT INTO tokens (hash, role, channel_scope, created_by, max_uses, expires_at) VALUES (?, ?, ?, ?, ?, ?)",
		hash, int(role), channelScope, createdBy, maxUses, expStr)
	if err != nil {
		return fmt.Errorf("store: create token: %w", err)
	}
	return nil
}

// ValidateToken checks if a token hash is valid and returns the associated role.
// It increments the use count atomically.
func (s *Store) ValidateToken(hash string) (model.Role, error) {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var roleInt int
	var maxUses, useCount int
	var expiresAt *string
	err = tx.QueryRowContext(ctx,
		"SELECT role, max_uses, use_count, expires_at FROM tokens WHERE hash = ?", hash).
		Scan(&roleInt, &maxUses, &useCount, &expiresAt)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("store: invalid token")
	}
	if err != nil {
		return 0, fmt.Errorf("store: validate token: %w", err)
	}

	// Check expiration
	if expiresAt != nil {
		exp, err := parseDBTime(*expiresAt)
		if err != nil {
			return 0, fmt.Errorf("store: validate token: %w", err)
		}
		if time.Now().After(exp) {
			return 0, fmt.Errorf("store: token expired")
		}
	}

	// Check uses
	if maxUses > 0 && useCount >= maxUses {
		return 0, fmt.Errorf("store: token exhausted")
	}

	// Increment use count
	if _, err := tx.ExecContext(ctx, "UPDATE tokens SET use_count = use_count + 1 WHERE hash = ?", hash); err != nil {
		return 0, fmt.Errorf("store: increment use: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit: %w", err)
	}

	return model.Role(roleInt), nil
}

// ---- Bans ----

// CreateBan adds a ban record.
func (s *Store) CreateBan(userID int64, ip, reason string, bannedBy int64, expiresAt time.Time) error {
	var expStr *string
	if !expiresAt.IsZero() {
		es := formatDBTime(expiresAt)
		expStr = &es
	}
	_, err := s.db.ExecContext(context.Background(),
		"INSERT INTO bans (user_id, ip, reason, banned_by, expires_at) VALUES (?, ?, ?, ?, ?)",
		userID, ip, reason, bannedBy, expStr)
	if err != nil {
		return fmt.Errorf("store: create ban: %w", err)
	}
	return nil
}

// IsUserBanned checks if a user ID is currently banned.
func (s *Store) IsUserBanned(userID int64) (bool, error) {
	var count int
	err := s.db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM bans WHERE user_id = ? AND (expires_at IS NULL OR expires_at > datetime('now'))",
		userID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("store: check ban: %w", err)
	}
	return count > 0, nil
}
