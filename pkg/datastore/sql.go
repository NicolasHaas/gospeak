package datastore

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/NicolasHaas/gospeak/pkg/model"
)

const (
	dbTimeLayout       = "2006-01-02 15:04:05"
	tokenKindInvite    = 0
	tokenKindBootstrap = 1
)

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

type migrationStep struct {
	statement string
	table     string
	column    string
}

type schemaMigration struct {
	version int
	steps   []migrationStep
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
	dsn, err := sqliteConnectionDSN(dbPath)
	if err != nil {
		return nil, fmt.Errorf("datastore: prepare DB path: %w", err)
	}
	filePath, hasFile, createFile, err := sqliteFilesystemPath(dbPath)
	if err != nil {
		return nil, fmt.Errorf("datastore: prepare DB path: %w", err)
	}
	if hasFile {
		if err := prepareSQLiteFiles(filePath, createFile); err != nil {
			return nil, fmt.Errorf("datastore: protect DB files: %w", err)
		}
	}
	DB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("datastore: open DB: %w", err)
	}

	ctx := context.Background()

	// Enable WAL mode for better concurrent read performance
	if _, err := DB.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		_ = DB.Close()
		return nil, fmt.Errorf("datastore: set WAL: %w", err)
	}
	s := &ProviderFactory{DB: DB}
	if err := s.migrate(); err != nil {
		_ = DB.Close()
		return nil, fmt.Errorf("datastore: migrate: %w", err)
	}
	if hasFile {
		if err := protectSQLiteFiles(filePath); err != nil {
			_ = DB.Close()
			return nil, fmt.Errorf("datastore: protect DB files: %w", err)
		}
	}
	return s, nil
}

func sqliteConnectionDSN(dbPath string) (string, error) {
	if dbPath == "" {
		return "", fmt.Errorf("database path is empty")
	}
	base, rawQuery, _ := strings.Cut(dbPath, "?")
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", fmt.Errorf("parse SQLite query parameters: %w", err)
	}
	// The factory owns connection pragmas. Dropping every caller-supplied
	// pragma avoids SQLite syntax variants changing these required values
	// after the driver sorts and executes repeated _pragma parameters.
	query.Del("_pragma")
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	return base + "?" + query.Encode(), nil
}

func sqliteFilesystemPath(dbPath string) (string, bool, bool, error) {
	base, rawQuery, _ := strings.Cut(dbPath, "?")
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", false, false, fmt.Errorf("parse SQLite query parameters: %w", err)
	}
	if base == ":memory:" {
		return "", false, false, nil
	}
	vfs, err := sqliteURIParameter(query, "vfs")
	if err != nil {
		return "", false, false, err
	}
	if vfs != "" && vfs != "memdb" {
		return "", false, false, fmt.Errorf("unsupported SQLite VFS %q", vfs)
	}
	if !strings.HasPrefix(base, "file:") {
		if vfs == "memdb" {
			return "", false, false, nil
		}
		return base, true, true, nil
	}
	mode, err := sqliteURIParameter(query, "mode")
	if err != nil {
		return "", false, false, err
	}
	if mode != "" && mode != "ro" && mode != "rw" && mode != "rwc" && mode != "memory" {
		return "", false, false, fmt.Errorf("unsupported SQLite mode %q", mode)
	}
	if mode == "memory" || vfs == "memdb" {
		return "", false, false, nil
	}

	uri, err := url.Parse(base)
	if err != nil {
		return "", false, false, fmt.Errorf("parse SQLite file URI: %w", err)
	}
	if uri.Host != "" && uri.Host != "localhost" {
		return "", false, false, fmt.Errorf("unsupported SQLite file URI authority %q", uri.Host)
	}
	path, err := sqliteFileURIPath(uri, runtime.GOOS)
	if err != nil {
		return "", false, false, err
	}
	if path == "" {
		return "", false, false, fmt.Errorf("temporary SQLite databases are unsupported")
	}
	if path == ":memory:" {
		return "", false, false, nil
	}
	return path, true, mode == "" || mode == "rwc", nil
}

func sqliteURIParameter(query url.Values, name string) (string, error) {
	values := query[name]
	if len(values) > 1 {
		return "", fmt.Errorf("SQLite URI contains multiple %s parameters", name)
	}
	if len(values) == 0 {
		return "", nil
	}
	return values[0], nil
}

func sqliteFileURIPath(uri *url.URL, targetOS string) (string, error) {
	path := uri.Path
	if uri.Opaque != "" {
		decoded, err := url.PathUnescape(uri.Opaque)
		if err != nil {
			return "", fmt.Errorf("decode SQLite file URI: %w", err)
		}
		path = decoded
	}

	if targetOS == "windows" && len(path) >= 3 && path[0] == '/' && path[2] == ':' &&
		((path[1] >= 'A' && path[1] <= 'Z') || (path[1] >= 'a' && path[1] <= 'z')) {
		path = path[1:]
	}
	if targetOS == "windows" {
		return strings.ReplaceAll(path, "/", `\`), nil
	}
	return filepath.FromSlash(path), nil
}

func prepareSQLiteFiles(dbPath string, create bool) error {
	parent := filepath.Dir(dbPath)
	info, err := os.Stat(parent)
	if os.IsNotExist(err) {
		if !create {
			return fmt.Errorf("database directory does not exist: %w", os.ErrNotExist)
		}
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return fmt.Errorf("create database directory: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect database directory: %w", err)
	} else if !info.IsDir() {
		return fmt.Errorf("database parent is not a directory")
	}

	// Existing parents may intentionally be shared (for example "." or
	// /tmp), so MkdirAll's owner-only mode applies only to directories it
	// creates. The database artifacts themselves are always owner-only.
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	file, err := os.OpenFile(dbPath, flags, 0o600) //nolint:gosec // The server operator explicitly selects the SQLite path.
	if err != nil {
		return fmt.Errorf("open database file: %w", err)
	}
	if err := verifySQLiteFileOwner(file); err != nil {
		_ = file.Close()
		return fmt.Errorf("verify database file owner: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("protect database file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close database file: %w", err)
	}
	return protectSQLiteFiles(dbPath)
}

func protectSQLiteFiles(dbPath string) error {
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		file, err := os.Open(path) //nolint:gosec // Sidecar paths are derived from the operator-selected SQLite path.
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("open %s: %w", filepath.Base(path), err)
		}
		if err := verifySQLiteFileOwner(file); err != nil {
			_ = file.Close()
			return fmt.Errorf("verify %s owner: %w", filepath.Base(path), err)
		}
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return fmt.Errorf("protect %s: %w", filepath.Base(path), err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close %s: %w", filepath.Base(path), err)
		}
	}
	return nil
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
		created_by         INTEGER NOT NULL DEFAULT 0,
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
		banned_by  INTEGER NOT NULL DEFAULT 0,
		expires_at TEXT,
		created_at TEXT    NOT NULL DEFAULT (datetime('now')),
		CHECK ((user_id > 0 AND ip = '') OR (user_id = 0 AND ip <> ''))
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
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("datastore: acquire migration connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("datastore: begin migration transaction: %w", err)
	}
	if err := migrateSchema(ctx, conn, schema); err != nil {
		return rollbackMigration(ctx, conn, err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return rollbackMigration(ctx, conn, fmt.Errorf("datastore: commit migration transaction: %w", err))
	}
	return nil
}

func migrateSchema(ctx context.Context, db DB, schema string) error {
	if err := ensureSchemaMigrations(ctx, db); err != nil {
		return err
	}
	currentVersion, err := getSchemaVersion(ctx, db)
	if err != nil {
		return err
	}

	migrations := []schemaMigration{
		{
			version: 1,
			steps:   []migrationStep{{statement: schema}},
		},
		{
			version: 2,
			steps: []migrationStep{
				{statement: "ALTER TABLE channels ADD COLUMN parent_id INTEGER NOT NULL DEFAULT 0", table: "channels", column: "parent_id"},
				{statement: "ALTER TABLE channels ADD COLUMN is_temp INTEGER NOT NULL DEFAULT 0", table: "channels", column: "is_temp"},
				{statement: "ALTER TABLE channels ADD COLUMN allow_sub_channels INTEGER NOT NULL DEFAULT 0", table: "channels", column: "allow_sub_channels"},
			},
		},
		{
			version: 3,
			steps: []migrationStep{
				{statement: "ALTER TABLE users ADD COLUMN personal_token_hash TEXT NOT NULL DEFAULT ''", table: "users", column: "personal_token_hash"},
				{statement: "ALTER TABLE users ADD COLUMN personal_token_created_at TEXT NOT NULL DEFAULT '1970-01-01 00:00:00'", table: "users", column: "personal_token_created_at"},
				{statement: "UPDATE users SET personal_token_created_at = datetime('now') WHERE personal_token_created_at = '1970-01-01 00:00:00'"},
			},
		},
		{
			version: 4,
			steps: []migrationStep{
				{statement: "CREATE INDEX IF NOT EXISTS idx_users_personal_token_hash ON users(personal_token_hash)"},
			},
		},
		{
			version: 5,
			steps: []migrationStep{
				{statement: "ALTER TABLE users ADD COLUMN channel_scope INTEGER NOT NULL DEFAULT 0", table: "users", column: "channel_scope"},
			},
		},
		{
			version: 6,
			steps: []migrationStep{
				{statement: "CREATE UNIQUE INDEX IF NOT EXISTS idx_channels_parent_name ON channels(parent_id, name)"},
			},
		},
		{
			version: 7,
			steps: []migrationStep{
				{statement: "ALTER TABLE tokens ADD COLUMN kind INTEGER NOT NULL DEFAULT 0 CHECK(kind IN (0, 1))", table: "tokens", column: "kind"},
			},
		},
		{
			version: 8,
			steps: []migrationStep{
				{statement: "ALTER TABLE channels ADD COLUMN created_by INTEGER NOT NULL DEFAULT 0", table: "channels", column: "created_by"},
			},
		},
		{
			// Versions 2-4 historically swallowed every SQLite error. Reconcile
			// their required schema once so databases carrying an overstated
			// version are repaired without guessing from error strings.
			version: 9,
			steps: []migrationStep{
				{statement: "ALTER TABLE channels ADD COLUMN parent_id INTEGER NOT NULL DEFAULT 0", table: "channels", column: "parent_id"},
				{statement: "ALTER TABLE channels ADD COLUMN is_temp INTEGER NOT NULL DEFAULT 0", table: "channels", column: "is_temp"},
				{statement: "ALTER TABLE channels ADD COLUMN allow_sub_channels INTEGER NOT NULL DEFAULT 0", table: "channels", column: "allow_sub_channels"},
				{statement: "ALTER TABLE users ADD COLUMN personal_token_hash TEXT NOT NULL DEFAULT ''", table: "users", column: "personal_token_hash"},
				{statement: "ALTER TABLE users ADD COLUMN personal_token_created_at TEXT NOT NULL DEFAULT '1970-01-01 00:00:00'", table: "users", column: "personal_token_created_at"},
				{statement: "UPDATE users SET personal_token_created_at = datetime('now') WHERE personal_token_created_at = '1970-01-01 00:00:00'"},
				{statement: "CREATE INDEX IF NOT EXISTS idx_users_personal_token_hash ON users(personal_token_hash)"},
			},
		},
		{
			version: 10,
			steps: []migrationStep{
				{statement: `CREATE TABLE IF NOT EXISTS bans (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					user_id INTEGER NOT NULL DEFAULT 0,
					ip TEXT NOT NULL DEFAULT '',
					banned_by INTEGER NOT NULL DEFAULT 0,
					expires_at TEXT,
					created_at TEXT NOT NULL DEFAULT (datetime('now'))
				)`},
				{statement: `CREATE TABLE bans_v10 (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					user_id INTEGER NOT NULL DEFAULT 0,
					ip TEXT NOT NULL DEFAULT '',
					banned_by INTEGER NOT NULL DEFAULT 0,
					expires_at TEXT,
					created_at TEXT NOT NULL DEFAULT (datetime('now')),
					CHECK ((user_id > 0 AND ip = '') OR (user_id = 0 AND ip <> ''))
				)`},
				{statement: `INSERT INTO bans_v10 (id, user_id, ip, banned_by, expires_at, created_at)
					SELECT id, user_id, '', banned_by, expires_at, created_at FROM bans WHERE user_id > 0`},
				{statement: "DROP TABLE bans"},
				{statement: "ALTER TABLE bans_v10 RENAME TO bans"},
				{statement: "CREATE INDEX idx_bans_ip ON bans(ip) WHERE user_id = 0"},
				{statement: "CREATE INDEX idx_bans_user_id ON bans(user_id) WHERE user_id > 0"},
			},
		},
	}

	for _, m := range migrations {
		if m.version <= currentVersion {
			continue
		}
		for _, step := range m.steps {
			if step.column != "" {
				exists, err := tableColumnExists(ctx, db, step.table, step.column)
				if err != nil {
					return err
				}
				if exists {
					continue
				}
			}
			if _, err := db.ExecContext(ctx, step.statement); err != nil {
				return fmt.Errorf("datastore: apply schema migration %d: %w", m.version, err)
			}
		}
		if err := setSchemaVersion(ctx, db, m.version); err != nil {
			return err
		}
	}
	return nil
}

func rollbackMigration(ctx context.Context, db DB, cause error) error {
	if _, err := db.ExecContext(ctx, "ROLLBACK"); err != nil {
		return fmt.Errorf("%w; rollback migration transaction: %v", cause, err)
	}
	return cause
}

func tableColumnExists(ctx context.Context, db DB, table, column string) (bool, error) {
	var query string
	switch table {
	case "users":
		query = "SELECT EXISTS(SELECT 1 FROM pragma_table_info('users') WHERE name = ?)"
	case "tokens":
		query = "SELECT EXISTS(SELECT 1 FROM pragma_table_info('tokens') WHERE name = ?)"
	case "channels":
		query = "SELECT EXISTS(SELECT 1 FROM pragma_table_info('channels') WHERE name = ?)"
	default:
		return false, fmt.Errorf("datastore: inspect unsupported schema table %q", table)
	}
	var exists int
	if err := db.QueryRowContext(ctx, query, column).Scan(&exists); err != nil {
		return false, fmt.Errorf("datastore: inspect schema column: %w", err)
	}
	return exists != 0, nil
}

func ensureSchemaMigrations(ctx context.Context, db DB) error {
	if _, err := db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER NOT NULL)"); err != nil {
		return fmt.Errorf("datastore: create schema_migrations: %w", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		return fmt.Errorf("datastore: check schema_migrations: %w", err)
	}
	if count == 0 {
		if _, err := db.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES (0)"); err != nil {
			return fmt.Errorf("datastore: init schema_migrations: %w", err)
		}
	}
	return nil
}

func getSchemaVersion(ctx context.Context, db DB) (int, error) {
	var version int
	if err := db.QueryRowContext(ctx, "SELECT version FROM schema_migrations LIMIT 1").Scan(&version); err != nil {
		return 0, fmt.Errorf("datastore: read schema version: %w", err)
	}
	return version, nil
}

func setSchemaVersion(ctx context.Context, db DB, version int) error {
	if _, err := db.ExecContext(ctx, "UPDATE schema_migrations SET version = ?", version); err != nil {
		return fmt.Errorf("datastore: update schema version: %w", err)
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

// IsBootstrapUser reports whether userID owns the durable bootstrap marker.
func (s *baseProvider) IsBootstrapUser(userID int64) (bool, error) {
	if userID <= 0 {
		return false, nil
	}
	var exists int
	if err := s.QueryRowContext(context.Background(),
		"SELECT EXISTS(SELECT 1 FROM tokens WHERE created_by = ? AND kind = ? AND use_count = 1)",
		userID, tokenKindBootstrap,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("datastore: inspect bootstrap user: %w", err)
	}
	return exists != 0, nil
}

// BootstrapUserID returns the durable bootstrap owner after first use.
func (s *baseProvider) BootstrapUserID() (int64, bool, error) {
	var userID int64
	err := s.QueryRowContext(context.Background(),
		"SELECT created_by FROM tokens WHERE kind = ? AND created_by > 0 AND use_count = 1 LIMIT 1",
		tokenKindBootstrap,
	).Scan(&userID)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("datastore: read bootstrap user: %w", err)
	}
	return userID, true, nil
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
		"INSERT INTO channels (name, description, max_users, parent_id, is_temp, allow_sub_channels, created_by) VALUES (?, ?, ?, ?, ?, ?, ?)",
		channel.Name,
		channel.Description,
		channel.MaxUsers,
		channel.ParentID,
		isTempInt,
		allowSubInt,
		channel.CreatedBy,
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
	rows, err := s.QueryContext(context.Background(), "SELECT id, name, description, max_users, parent_id, is_temp, allow_sub_channels, created_by, created_at FROM channels ORDER BY parent_id, id")
	if err != nil {
		return nil, fmt.Errorf("datastore: list channels: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var channels []model.Channel
	for rows.Next() {
		var ch model.Channel
		var createdAt string
		var isTempInt, allowSubInt int
		if err := rows.Scan(&ch.ID, &ch.Name, &ch.Description, &ch.MaxUsers, &ch.ParentID, &isTempInt, &allowSubInt, &ch.CreatedBy, &createdAt); err != nil {
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
	err := s.QueryRowContext(context.Background(), "SELECT id, name, description, max_users, parent_id, is_temp, allow_sub_channels, created_by, created_at FROM channels WHERE id = ?", id).
		Scan(&ch.ID, &ch.Name, &ch.Description, &ch.MaxUsers, &ch.ParentID, &isTempInt, &allowSubInt, &ch.CreatedBy, &createdAt)
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
	err := s.QueryRowContext(context.Background(), "SELECT id, name, description, max_users, parent_id, is_temp, allow_sub_channels, created_by, created_at FROM channels WHERE name = ? AND parent_id = ?", name, parentID).
		Scan(&ch.ID, &ch.Name, &ch.Description, &ch.MaxUsers, &ch.ParentID, &isTempInt, &allowSubInt, &ch.CreatedBy, &createdAt)
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

func (s *baseProvider) BootstrapTokenState() (BootstrapTokenState, error) {
	rows, err := s.QueryContext(context.Background(),
		"SELECT max_uses, use_count, created_by FROM tokens WHERE kind = ?", tokenKindBootstrap,
	)
	if err != nil {
		return BootstrapTokenAbsent, fmt.Errorf("datastore: read bootstrap token state: %w", err)
	}
	defer rows.Close()
	state := BootstrapTokenAbsent
	for rows.Next() {
		if state != BootstrapTokenAbsent {
			return BootstrapTokenAbsent, fmt.Errorf("datastore: multiple bootstrap token records")
		}
		var maxUses, useCount int
		var createdBy int64
		if err := rows.Scan(&maxUses, &useCount, &createdBy); err != nil {
			return BootstrapTokenAbsent, fmt.Errorf("datastore: scan bootstrap token state: %w", err)
		}
		switch {
		case maxUses == -1 && useCount == 0 && createdBy == 0:
			state = BootstrapTokenPending
		case maxUses == -1 && useCount == 1 && createdBy > 0:
			state = BootstrapTokenPending
		case maxUses == 1 && useCount == 1 && createdBy > 0:
			state = BootstrapTokenFinalized
		default:
			return BootstrapTokenAbsent, fmt.Errorf("datastore: invalid bootstrap token state")
		}
	}
	if err := rows.Err(); err != nil {
		return BootstrapTokenAbsent, fmt.Errorf("datastore: iterate bootstrap token state: %w", err)
	}
	return state, nil
}

// CreateToken stores a new token (hash only).
func (s *baseProvider) CreateToken(hash string, role model.Role, channelScope int64, createdBy int64, maxUses int, expiresAt time.Time) error {
	if maxUses < 0 {
		return fmt.Errorf("datastore: create token: max uses must not be negative")
	}
	var expStr *string
	if !expiresAt.IsZero() {
		value := formatDBTime(expiresAt)
		expStr = &value
	}
	_, err := s.ExecContext(context.Background(),
		"INSERT INTO tokens (hash, role, channel_scope, created_by, kind, max_uses, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		hash, int(role), channelScope, createdBy, tokenKindInvite, maxUses, expStr)
	if err != nil {
		return fmt.Errorf("datastore: create token: %w", err)
	}
	return nil
}

func (s *baseProvider) CreateBootstrapToken(hash string) error {
	_, err := s.ExecContext(context.Background(),
		"INSERT INTO tokens (hash, role, channel_scope, created_by, kind, max_uses) VALUES (?, ?, 0, 0, ?, -1)",
		hash, int(model.RoleAdmin), tokenKindBootstrap)
	if err != nil {
		return fmt.Errorf("datastore: create bootstrap token: %w", err)
	}
	return nil
}

// FinalizeBootstrapToken makes a provisioned bootstrap credential permanently
// exhausted after the client proves possession of its personal token.
func (s *baseProvider) FinalizeBootstrapToken(userID int64) (bool, error) {
	ctx := context.Background()
	var belongs int
	if err := s.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM tokens WHERE created_by = ? AND kind = ? AND max_uses IN (-1, 1) AND use_count = 1)",
		userID, tokenKindBootstrap,
	).Scan(&belongs); err != nil {
		return false, fmt.Errorf("datastore: inspect bootstrap finalization: %w", err)
	}
	if belongs == 0 {
		return false, nil
	}
	if _, err := s.ExecContext(ctx,
		"UPDATE tokens SET max_uses = 1 WHERE created_by = ? AND kind = ? AND max_uses = -1 AND use_count = 1",
		userID, tokenKindBootstrap,
	); err != nil {
		return false, fmt.Errorf("datastore: finalize bootstrap token: %w", err)
	}
	return true, nil
}

// ValidateToken checks if a token hash is valid and returns its authorization data.
// It increments the use count atomically.
func (s *txProvider) ValidateToken(hash string) (*model.Token, error) {
	ctx := context.Background()

	var roleInt int
	var channelScope int64
	var kind, maxUses, useCount int
	var expiresAt *string
	err := s.QueryRowContext(ctx,
		"SELECT role, channel_scope, kind, max_uses, use_count, expires_at FROM tokens WHERE hash = ?", hash).
		Scan(&roleInt, &channelScope, &kind, &maxUses, &useCount, &expiresAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("datastore: invalid token")
	}
	if err != nil {
		return nil, fmt.Errorf("datastore: validate token: %w", err)
	}
	if kind != tokenKindInvite || maxUses < 0 {
		return nil, fmt.Errorf("datastore: invalid token")
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

// ProvisionUser atomically redeems an invite, creates its user, and installs
// the personal credential. Tokenless provisioning is allowed only when the
// caller explicitly enables the server's open mode.
func (s *txProvider) ProvisionUser(hash, username, personalTokenHash string, createdAt time.Time, allowTokenless bool) (*model.User, error) {
	role := model.RoleUser
	var channelScope int64
	if hash == "" {
		if !allowTokenless {
			return nil, fmt.Errorf("datastore: token required")
		}
	} else {
		token, err := s.ValidateToken(hash)
		if err != nil {
			return nil, err
		}
		role = token.Role
		channelScope = token.ChannelScope
	}

	user, err := s.CreateUserWithChannelScope(username, role, channelScope)
	if err != nil {
		return nil, err
	}
	if err := s.UpdateUserPersonalToken(user.ID, personalTokenHash, createdAt); err != nil {
		return nil, err
	}
	user.PersonalTokenHash = personalTokenHash
	user.PersonalTokenCreatedAt = createdAt
	return user, nil
}

// ProvisionBootstrapUser atomically binds the internal bootstrap credential to
// one administrator and a deterministic personal token. Repeating the same
// provisioning request returns the existing user; a different username is
// rejected.
func (s *txProvider) ProvisionBootstrapUser(hash, username, personalTokenHash string, createdAt time.Time) (*model.User, error) {
	ctx := context.Background()
	var roleInt, kind, maxUses, useCount int
	var channelScope, createdBy int64
	if err := s.QueryRowContext(ctx,
		"SELECT role, channel_scope, created_by, kind, max_uses, use_count FROM tokens WHERE hash = ?", hash,
	).Scan(&roleInt, &channelScope, &createdBy, &kind, &maxUses, &useCount); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("datastore: invalid bootstrap credential")
		}
		return nil, fmt.Errorf("datastore: read bootstrap credential: %w", err)
	}
	if model.Role(roleInt) != model.RoleAdmin || channelScope != 0 || kind != tokenKindBootstrap || maxUses != -1 {
		return nil, fmt.Errorf("datastore: invalid bootstrap credential")
	}
	if useCount == 0 && createdBy != 0 {
		return nil, fmt.Errorf("datastore: invalid bootstrap credential state")
	}
	if useCount != 0 {
		if useCount != 1 || createdBy <= 0 {
			return nil, fmt.Errorf("datastore: invalid bootstrap credential state")
		}
		user, err := s.GetUserByID(createdBy)
		if err != nil {
			return nil, err
		}
		if user == nil || user.Username != username || user.PersonalTokenHash != personalTokenHash {
			return nil, ErrBootstrapAlreadyProvisioned
		}
		return user, nil
	}

	result, err := s.ExecContext(ctx,
		"UPDATE tokens SET use_count = 1 WHERE hash = ? AND kind = ? AND max_uses = -1 AND use_count = 0",
		hash, tokenKindBootstrap,
	)
	if err != nil {
		return nil, fmt.Errorf("datastore: claim bootstrap credential: %w", err)
	}
	claimed, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("datastore: inspect bootstrap claim: %w", err)
	}
	if claimed != 1 {
		return nil, fmt.Errorf("datastore: bootstrap credential changed concurrently")
	}
	user, err := s.CreateUserWithChannelScope(username, model.RoleAdmin, 0)
	if err != nil {
		return nil, err
	}
	if err := s.UpdateUserPersonalToken(user.ID, personalTokenHash, createdAt); err != nil {
		return nil, err
	}
	result, err = s.ExecContext(ctx,
		"UPDATE tokens SET created_by = ? WHERE hash = ? AND kind = ? AND max_uses = -1 AND use_count = 1 AND created_by = 0",
		user.ID, hash, tokenKindBootstrap,
	)
	if err != nil {
		return nil, fmt.Errorf("datastore: bind bootstrap credential: %w", err)
	}
	bound, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("datastore: inspect bootstrap binding: %w", err)
	}
	if bound != 1 {
		return nil, fmt.Errorf("datastore: bootstrap credential changed concurrently")
	}
	user.PersonalTokenHash = personalTokenHash
	user.PersonalTokenCreatedAt = createdAt
	return user, nil
}

// ---- Bans ----

// CreateUserBan adds an account-only ban record.
func (s *baseProvider) CreateUserBan(userID, bannedBy int64, expiresAt time.Time) error {
	if userID <= 0 {
		return fmt.Errorf("datastore: create user ban: invalid user ID")
	}
	return s.createBan(userID, "", bannedBy, expiresAt)
}

// CreateIPBan adds one canonical exact-address ban record.
func (s *baseProvider) CreateIPBan(ip string, bannedBy int64, expiresAt time.Time) error {
	canonical, err := model.CanonicalIPAddress(ip)
	if err != nil {
		return fmt.Errorf("datastore: create IP ban: %w", err)
	}
	return s.createBan(0, canonical, bannedBy, expiresAt)
}

func (s *baseProvider) createBan(userID int64, ip string, bannedBy int64, expiresAt time.Time) error {
	var expStr *string
	if !expiresAt.IsZero() {
		es := formatDBTime(expiresAt)
		expStr = &es
	}
	_, err := s.ExecContext(context.Background(),
		"INSERT INTO bans (user_id, ip, banned_by, expires_at) VALUES (?, ?, ?, ?)",
		userID, ip, bannedBy, expStr)
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

// IsIPBanned checks one exact canonical address against active bans.
func (s *baseProvider) IsIPBanned(ip string) (bool, error) {
	canonical, err := model.CanonicalIPAddress(ip)
	if err != nil {
		return false, fmt.Errorf("datastore: check IP ban: %w", err)
	}
	var count int
	if err := s.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM bans WHERE user_id = 0 AND ip = ? AND (expires_at IS NULL OR expires_at > datetime('now'))",
		canonical,
	).Scan(&count); err != nil {
		return false, fmt.Errorf("datastore: check IP ban: %w", err)
	}
	return count > 0, nil
}

// ListActiveBans returns one bounded page without user-controlled reason text.
func (s *baseProvider) ListActiveBans(afterID int64, limit int) ([]model.Ban, bool, error) {
	if afterID < 0 || limit <= 0 || limit > MaxBanPageSize {
		return nil, false, fmt.Errorf("datastore: invalid ban page")
	}
	rows, err := s.QueryContext(context.Background(), `
		SELECT b.id, b.user_id, COALESCE(u.username, ''), b.ip, b.banned_by, b.expires_at, b.created_at
		FROM bans b
		LEFT JOIN users u ON u.id = b.user_id
		WHERE b.id > ? AND (b.expires_at IS NULL OR b.expires_at > datetime('now'))
		ORDER BY b.id
		LIMIT ?
	`, afterID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("datastore: list active bans: %w", err)
	}
	defer rows.Close()

	bans := make([]model.Ban, 0)
	for rows.Next() {
		var ban model.Ban
		var expiresAt *string
		var createdAt string
		if err := rows.Scan(&ban.ID, &ban.UserID, &ban.Username, &ban.IP, &ban.BannedBy, &expiresAt, &createdAt); err != nil {
			return nil, false, fmt.Errorf("datastore: scan active ban: %w", err)
		}
		ban.CreatedAt, err = parseDBTime(createdAt)
		if err != nil {
			return nil, false, fmt.Errorf("datastore: parse ban creation time: %w", err)
		}
		if expiresAt != nil {
			ban.ExpiresAt, err = parseDBTime(*expiresAt)
			if err != nil {
				return nil, false, fmt.Errorf("datastore: parse ban expiration: %w", err)
			}
		}
		bans = append(bans, ban)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("datastore: iterate active bans: %w", err)
	}
	hasMore := len(bans) > limit
	if hasMore {
		bans = bans[:limit]
	}
	return bans, hasMore, nil
}

// DeleteBan removes one immutable ban record by ID.
func (s *baseProvider) DeleteBan(id int64) (bool, error) {
	if id <= 0 {
		return false, nil
	}
	result, err := s.ExecContext(context.Background(), "DELETE FROM bans WHERE id = ?", id)
	if err != nil {
		return false, fmt.Errorf("datastore: delete ban: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("datastore: delete ban rows affected: %w", err)
	}
	return rows == 1, nil
}

// ---- Messages ----

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
