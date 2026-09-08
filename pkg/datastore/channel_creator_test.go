package datastore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/model"
)

func TestChannelCreatorPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "channels.db")
	store, err := NewProviderFactory(path)
	if err != nil {
		t.Fatalf("NewProviderFactory(): %v", err)
	}
	channel := model.NewChannel()
	channel.Name = "temporary"
	channel.IsTemp = true
	channel.CreatedBy = 42
	if err := store.NonTx().CreateChannel(channel); err != nil {
		t.Fatalf("CreateChannel(): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	reopened, err := NewProviderFactory(path)
	if err != nil {
		t.Fatalf("reopen datastore: %v", err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("Close reopened datastore: %v", err)
		}
	})
	got, err := reopened.NonTx().GetChannel(channel.ID)
	if err != nil {
		t.Fatalf("GetChannel(): %v", err)
	}
	if got == nil || got.CreatedBy != channel.CreatedBy {
		t.Fatalf("reopened channel = %#v, want creator %d", got, channel.CreatedBy)
	}
}

func TestChannelCreatorMigrationPreservesExistingRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "channels.db")
	store, err := NewProviderFactory(path)
	if err != nil {
		t.Fatalf("NewProviderFactory(): %v", err)
	}
	if _, err := store.DB.ExecContext(ctx, "UPDATE schema_migrations SET version = 7"); err != nil {
		t.Fatalf("rewind schema version: %v", err)
	}
	if _, err := store.DB.ExecContext(ctx, "ALTER TABLE channels DROP COLUMN created_by"); err != nil {
		t.Fatalf("remove creator column: %v", err)
	}
	if _, err := store.DB.ExecContext(ctx, "INSERT INTO channels (name, is_temp) VALUES (?, ?)", "legacy", 1); err != nil {
		t.Fatalf("seed legacy channel: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	reopened, err := NewProviderFactory(path)
	if err != nil {
		t.Fatalf("migrate datastore: %v", err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("Close reopened datastore: %v", err)
		}
	})
	legacy, err := reopened.NonTx().GetChannelByNameAndParent("legacy", 0)
	if err != nil {
		t.Fatalf("GetChannelByNameAndParent(): %v", err)
	}
	if legacy == nil || legacy.CreatedBy != 0 {
		t.Fatalf("migrated legacy channel = %#v, want creator 0", legacy)
	}
}
