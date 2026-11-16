-- Source-derived v0.3.0 fixture, not a historical server execution.
-- Tag commit: d216660f5cffa2dd6dbe34c879411aee368bafcd; pkg/datastore/sql.go migrate schema (only trailing whitespace trimmed).
-- Migrations 2 and 3 are already present in schema; migration 4 adds the index.

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
CREATE INDEX IF NOT EXISTS idx_users_personal_token_hash ON users(personal_token_hash);
CREATE TABLE schema_migrations (version INTEGER NOT NULL);
INSERT INTO schema_migrations VALUES (4);
-- ensureAdminToken in pkg/server/run.go calls CreateToken(hash, RoleAdmin, 0, 0, 0, ZeroTime()).
-- Synthetic hash only; CreateToken binds nil for zero expiry, use_count defaults to zero.
INSERT INTO tokens (hash, role, channel_scope, created_by, max_uses, expires_at) VALUES ('7651877d522e13f5bd506fc1d2981b329c075a0b5610f2be6155aa171b1726ad', 2, 0, 0, 0, NULL);
