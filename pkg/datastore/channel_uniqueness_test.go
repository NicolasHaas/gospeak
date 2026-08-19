package datastore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/model"
)

func TestCreateChannelRejectsDuplicateSiblingName(t *testing.T) {
	st := newChannelUniquenessStore(t)
	first := &model.Channel{Name: "duplicate", ParentID: 7}
	if err := st.NonTx().CreateChannel(first); err != nil {
		t.Fatalf("create first channel: %v", err)
	}

	second := &model.Channel{Name: "duplicate", ParentID: 7}
	if err := st.NonTx().CreateChannel(second); !errors.Is(err, ErrChannelNameTaken) {
		t.Fatalf("create duplicate error = %v, want %v", err, ErrChannelNameTaken)
	}
}

func TestCreateChannelAllowsSameNameUnderDifferentParents(t *testing.T) {
	st := newChannelUniquenessStore(t)
	for _, parentID := range []int64{1, 2} {
		channel := &model.Channel{Name: "shared", ParentID: parentID}
		if err := st.NonTx().CreateChannel(channel); err != nil {
			t.Fatalf("create channel under parent %d: %v", parentID, err)
		}
	}
}

func TestSiblingUniquenessMigrationRejectsExistingDuplicates(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "duplicates.db")
	st, err := NewProviderFactory(path)
	if err != nil {
		t.Fatalf("open datastore: %v", err)
	}
	if _, err := st.DB.ExecContext(ctx, "DROP INDEX IF EXISTS idx_channels_parent_name"); err != nil {
		t.Fatalf("drop sibling index: %v", err)
	}
	if _, err := st.DB.ExecContext(ctx, "UPDATE schema_migrations SET version = 5"); err != nil {
		t.Fatalf("rewind schema version: %v", err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO channels (name, parent_id) VALUES ('duplicate', 3), ('duplicate', 3)"); err != nil {
		t.Fatalf("seed duplicate channels: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close seeded datastore: %v", err)
	}

	reopened, err := NewProviderFactory(path)
	if err == nil {
		_ = reopened.Close()
		t.Fatal("NewProviderFactory() error = nil, want duplicate migration failure")
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open failed-migration datastore: %v", err)
	}
	var version, duplicates, indexes int
	if err := raw.QueryRowContext(ctx, "SELECT version FROM schema_migrations").Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if err := raw.QueryRowContext(ctx, "SELECT COUNT(*) FROM channels WHERE name = 'duplicate' AND parent_id = 3").Scan(&duplicates); err != nil {
		t.Fatalf("count preserved duplicates: %v", err)
	}
	if err := raw.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_channels_parent_name'").Scan(&indexes); err != nil {
		t.Fatalf("check failed index: %v", err)
	}
	if version != 5 || duplicates != 2 || indexes != 0 {
		t.Fatalf("failed migration state = version %d, duplicates %d, indexes %d; want 5, 2, 0", version, duplicates, indexes)
	}
	if _, err := raw.ExecContext(ctx, "DELETE FROM channels WHERE id = (SELECT MAX(id) FROM channels WHERE name = 'duplicate' AND parent_id = 3)"); err != nil {
		t.Fatalf("resolve duplicate channel: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close repaired datastore: %v", err)
	}

	reopened, err = NewProviderFactory(path)
	if err != nil {
		t.Fatalf("reopen repaired datastore: %v", err)
	}
	if err := reopened.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_channels_parent_name'").Scan(&indexes); err != nil {
		t.Fatalf("check migrated index: %v", err)
	}
	if indexes != 1 {
		t.Fatalf("migrated index count = %d, want 1", indexes)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close migrated datastore: %v", err)
	}
}

func newChannelUniquenessStore(t *testing.T) *ProviderFactory {
	t.Helper()
	st, err := NewProviderFactory(filepath.Join(t.TempDir(), "channels.db"))
	if err != nil {
		t.Fatalf("open datastore: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close datastore: %v", err)
		}
	})
	return st
}
