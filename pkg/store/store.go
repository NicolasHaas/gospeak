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
	_, err := s.db.ExecContext(ctx, schema)
	if err != nil {
		return err
	}

	// Auto-migrate: add new columns if missing (for existing databases)
	migrations := []string{
		"ALTER TABLE channels ADD COLUMN parent_id INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE channels ADD COLUMN is_temp INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE channels ADD COLUMN allow_sub_channels INTEGER NOT NULL DEFAULT 0",
	}
	for _, m := range migrations {
		_, _ = s.db.ExecContext(ctx, m) // ignore errors (column already exists)
	}
	return nil
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
	res, err := s.db.ExecContext(context.Background(), "INSERT INTO users (username, role) VALUES (?, ?)", username, int(role))
	if err != nil {
		return nil, fmt.Errorf("store: create user: %w", err)
	}
	id, _ := res.LastInsertId()
	return &model.User{
		ID:        id,
		Username:  username,
		Role:      role,
		CreatedAt: time.Now(),
	}, nil
}

// GetUserByUsername retrieves a user by username.
func (s *Store) GetUserByUsername(username string) (*model.User, error) {
	u := &model.User{}
	var roleInt int
	var createdAt string
	err := s.db.QueryRowContext(context.Background(), "SELECT id, username, role, created_at FROM users WHERE username = ?", username).
		Scan(&u.ID, &u.Username, &roleInt, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get user: %w", err)
	}
	u.Role = model.Role(roleInt)
	u.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
	return u, nil
}

// GetUserByID retrieves a user by ID.
func (s *Store) GetUserByID(id int64) (*model.User, error) {
	u := &model.User{}
	var roleInt int
	var createdAt string
	err := s.db.QueryRowContext(context.Background(), "SELECT id, username, role, created_at FROM users WHERE id = ?", id).
		Scan(&u.ID, &u.Username, &roleInt, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get user: %w", err)
	}
	u.Role = model.Role(roleInt)
	u.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
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

// ListUsers returns all users.
func (s *Store) ListUsers() ([]model.User, error) {
	rows, err := s.db.QueryContext(context.Background(), "SELECT id, username, role, created_at FROM users ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var users []model.User
	for rows.Next() {
		var u model.User
		var roleInt int
		var createdAt string
		if err := rows.Scan(&u.ID, &u.Username, &roleInt, &createdAt); err != nil {
			return nil, fmt.Errorf("store: scan user: %w", err)
		}
		u.Role = model.Role(roleInt)
		u.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
		users = append(users, u)
	}
	return users, rows.Err()
}

// ---- Channels ----

// CreateChannel creates a new channel.
func (s *Store) CreateChannel(name, description string, maxUsers int) (*model.Channel, error) {
	return s.CreateChannelFull(name, description, maxUsers, 0, false, false)
}

// CreateChannelFull creates a new channel with all options.
func (s *Store) CreateChannelFull(name, description string, maxUsers int, parentID int64, isTemp, allowSubChannels bool) (*model.Channel, error) {
	isTempInt := 0
	if isTemp {
		isTempInt = 1
	}
	allowSubInt := 0
	if allowSubChannels {
		allowSubInt = 1
	}
	res, err := s.db.ExecContext(context.Background(),
		"INSERT INTO channels (name, description, max_users, parent_id, is_temp, allow_sub_channels) VALUES (?, ?, ?, ?, ?, ?)",
		name, description, maxUsers, parentID, isTempInt, allowSubInt)
	if err != nil {
		return nil, fmt.Errorf("store: create channel: %w", err)
	}
	id, _ := res.LastInsertId()
	return &model.Channel{
		ID:               id,
		Name:             name,
		Description:      description,
		MaxUsers:         maxUsers,
		ParentID:         parentID,
		IsTemp:           isTemp,
		AllowSubChannels: allowSubChannels,
		CreatedAt:        time.Now(),
	}, nil
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
		ch.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
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
	ch.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
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
	ch.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
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
		s := expiresAt.UTC().Format("2006-01-02 15:04:05")
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
		exp, _ := time.Parse("2006-01-02 15:04:05", *expiresAt)
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
		es := expiresAt.UTC().Format("2006-01-02 15:04:05")
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
