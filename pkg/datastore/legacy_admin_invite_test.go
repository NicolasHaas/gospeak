package datastore

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/model"
)

func TestMigrationLegacyLoggedAdminInvite(t *testing.T) {
	for _, alreadyUpgraded := range []bool{false, true} {
		name := "v0.3.0"
		if alreadyUpgraded {
			name = "schema-13"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "legacy.db")
			raw := openRawSQLite(t, path)
			fixture, err := os.ReadFile("testdata/v0.3.0-admin-invite.sql")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := raw.ExecContext(t.Context(), string(fixture)); err != nil {
				t.Fatal(err)
			}
			// v0.3.0 control.go generates an independent personal token after
			// redeeming the logged invite; it never installs the invite hash.
			if _, err := raw.ExecContext(t.Context(), `INSERT INTO users (username, role, personal_token_hash) VALUES ('legacy-admin', 2, 'independent-personal-hash'); UPDATE tokens SET use_count = 1`); err != nil {
				t.Fatal(err)
			}
			closeRawSQLite(t, raw)
			if alreadyUpgraded {
				// Restore the exact schema-13 legacy row after creating its schema;
				// this represents an installation already upgraded by pre-fix v1.
				f, err := NewProviderFactory(path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.DB.ExecContext(t.Context(), "UPDATE tokens SET expires_at = NULL; UPDATE schema_migrations SET version = 13"); err != nil {
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			f, err := NewProviderFactory(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			tx, err := f.Tx(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			user, provisionErr := tx.ProvisionUser(crypto.HashToken("synthetic-v0.3.0-logged-admin-invite"), "intruder", "intruder-personal", time.Now(), false)
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if provisionErr == nil {
				t.Errorf("logged legacy invite provisioned new user id=%d role=%s; must be rejected", user.ID, user.Role)
			}
			tx, err = f.Tx(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			_, validationErr := tx.ValidateToken(crypto.HashToken("synthetic-v0.3.0-logged-admin-invite"))
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if validationErr == nil {
				t.Error("logged legacy invite still validates")
			}
			personal, err := f.NonTx().GetUserByPersonalTokenHash("independent-personal-hash")
			if err != nil || personal == nil || personal.Username != "legacy-admin" || personal.Role != model.RoleAdmin {
				t.Fatalf("existing personal credential/account changed: %+v %v", personal, err)
			}
			before := legacyInviteRows(t, f)
			if err := f.migrate(); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			f, err = NewProviderFactory(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, legacyInviteRows(t, f)) {
				t.Fatal("repeat migration/reopen changed token rows")
			}
			var uses, maxUses int
			var expiry string
			if err := f.DB.QueryRowContext(t.Context(), "SELECT use_count, max_uses, COALESCE(expires_at, '') FROM tokens").Scan(&uses, &maxUses, &expiry); err != nil || uses != 1 || maxUses != 0 || expiry != "1970-01-01 00:00:01" {
				t.Fatalf("legacy lifecycle row: uses=%d max=%d expiry=%q err=%v", uses, maxUses, expiry, err)
			}
		})
	}
}

func TestMigrationLegacyAdminInvitePreservesOtherCredentials(t *testing.T) {
	f, err := NewProviderFactory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.DB.ExecContext(t.Context(), `
		INSERT INTO users (username, role, personal_token_hash) VALUES ('owner', 2, 'personal');
		INSERT INTO channels (name) VALUES ('permanent');
		INSERT INTO tokens (hash, role, channel_scope, created_by, kind, max_uses, use_count, expires_at, bootstrap_key) VALUES
		('user-created-admin', 2, 0, 1, 0, 0, 3, NULL, NULL),
		('user-created-user', 0, 0, 1, 0, 0, 0, NULL, NULL),
		('system-user', 0, 0, 0, 0, 0, 0, NULL, NULL),
		('system-moderator', 1, 0, 0, 0, 0, 0, NULL, NULL),
		('scoped-admin', 2, 1, 0, 0, 0, 0, NULL, NULL),
		('expiring-admin', 2, 0, 0, 0, 0, 0, '2099-01-01 00:00:00', NULL),
		('expired-admin', 2, 0, 0, 0, 0, 9, '2000-01-01 00:00:00', NULL),
		('limited-admin', 2, 0, 0, 0, 5, 2, NULL, NULL),
		('exhausted-admin', 2, 0, 0, 0, 5, 5, NULL, NULL),
		('marked-admin', 2, 0, 0, 0, 0, 0, NULL, ''),
		('legacy-bootstrap-owner', 2, 0, 1, 1, -1, 1, NULL, NULL);
		UPDATE schema_migrations SET version = 13;
	`); err != nil {
		t.Fatal(err)
	}
	before := legacyInviteRows(t, f)
	if err := f.migrate(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, legacyInviteRows(t, f)) {
		t.Fatal("migration changed an unrelated token field")
	}
	owner, err := f.NonTx().GetUserByPersonalTokenHash("personal")
	if err != nil || owner == nil || owner.Role != model.RoleAdmin {
		t.Fatalf("owner personal authentication changed: %+v %v", owner, err)
	}
	legacy, err := f.NonTx().HasLegacyBootstrapOwner()
	if err != nil || !legacy {
		t.Fatalf("legacy owner warning lost: %v %v", legacy, err)
	}
	state, err := f.NonTx().BootstrapTokenState()
	if err != nil || state != BootstrapTokenPending {
		t.Fatalf("bootstrap ownership/state changed: %v %v", state, err)
	}
	for _, hash := range []string{"user-created-admin", "user-created-user", "system-user", "system-moderator", "scoped-admin", "expiring-admin", "limited-admin"} {
		tx, err := f.Tx(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		_, validationErr := tx.ValidateToken(hash)
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if validationErr != nil {
			t.Errorf("legitimate invite %s rejected: %v", hash, validationErr)
		}
	}
}

func TestMigrationLegacyAdminInviteRollsBackOnVersionFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	f, err := NewProviderFactory(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.ExecContext(t.Context(), `INSERT INTO tokens (hash, role) VALUES ('legacy-logged-admin-hash', 2);
		UPDATE schema_migrations SET version = 13;
		CREATE TRIGGER reject_version BEFORE UPDATE ON schema_migrations BEGIN SELECT RAISE(ABORT, 'reject version'); END;`); err != nil {
		t.Fatal(err)
	}
	before := legacyInviteRows(t, f)
	if err := f.migrate(); err == nil {
		t.Fatal("migration succeeded despite version-update failure")
	}
	if !reflect.DeepEqual(before, legacyInviteRows(t, f)) {
		t.Fatal("failed migration partially invalidated a token")
	}
	var version int
	if err := f.DB.QueryRowContext(t.Context(), "SELECT version FROM schema_migrations").Scan(&version); err != nil || version != 13 {
		t.Fatalf("failed migration advanced schema version: %d %v", version, err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func legacyInviteRows(t *testing.T, f *ProviderFactory) [][]any {
	t.Helper()
	rows, err := f.DB.QueryContext(t.Context(), "SELECT * FROM tokens ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
