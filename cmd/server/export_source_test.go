package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/server"
)

func TestRunExportRejectsMissingDatabase(t *testing.T) {
	for _, flags := range []string{"users", "channels", "both"} {
		for _, source := range []string{"path", "nested", "symlink", "uri"} {
			t.Run(flags+"/"+source, func(t *testing.T) {
				dir := t.TempDir()
				target := filepath.Join(dir, "missing.db")
				cfg := server.DefaultConfig()
				cfg.ExportUsers = flags != "channels"
				cfg.ExportChannels = flags != "users"
				cfg.DBPath = target
				switch source {
				case "nested":
					target = filepath.Join(dir, "absent", "missing.db")
					cfg.DBPath = target
				case "symlink":
					cfg.DBPath = filepath.Join(dir, "link.db")
					exportSourceLink(t, target, cfg.DBPath)
				case "uri":
					cfg.DBPath = "file:" + filepath.ToSlash(target) + "?mode=rwc"
				}
				err := run(cfg)
				if err == nil || !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "export database source") {
					t.Errorf("run() = %v, want explicit missing export source", err)
				}
				for _, path := range []string{target, target + "-wal", target + "-shm"} {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Errorf("export created %s: %v", path, err)
					}
				}
				if source == "nested" {
					if _, err := os.Stat(filepath.Dir(target)); !os.IsNotExist(err) {
						t.Error("export created missing parent")
					}
				}
			})
		}
	}
}

func TestRunExportExistingSource(t *testing.T) {
	for _, source := range []string{"empty", "symlink", "uri", "invalid", "permission"} {
		t.Run(source, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "existing.db")
			cfg := server.DefaultConfig()
			cfg.ExportUsers = true
			cfg.ExportChannels = true
			cfg.DBPath = target
			if source == "invalid" {
				if err := os.WriteFile(target, []byte("not a SQLite database"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				st, err := datastore.NewProviderFactory(target)
				if err != nil {
					t.Fatal(err)
				}
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
			}
			switch source {
			case "symlink":
				cfg.DBPath = filepath.Join(dir, "link.db")
				exportSourceLink(t, target, cfg.DBPath)
			case "uri":
				cfg.DBPath = "file:" + filepath.ToSlash(target) + "?mode=rw"
			case "permission":
				if err := os.Chmod(target, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Chmod(target, 0o600); err != nil {
						t.Error(err)
					}
				})
				probe, err := os.OpenFile(target, os.O_RDWR, 0) //nolint:gosec // target is a test-owned TempDir fixture; probe the factory's open permissions.
				if err == nil {
					if err := probe.Close(); err != nil {
						t.Fatal(err)
					}
					t.Skip("process can open the permission-restricted fixture")
				}
				if !errors.Is(err, os.ErrPermission) {
					t.Fatal(err)
				}
			}
			err := run(cfg)
			switch source {
			case "invalid":
				if err == nil {
					t.Fatal("invalid database exported successfully")
				}
			case "permission":
				if !errors.Is(err, os.ErrPermission) {
					t.Fatalf("permission failure = %v", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestRunNormalStartupStillCreatesDatabase(t *testing.T) {
	cfg := server.DefaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "new", "server.db")
	cfg.DataDir = t.TempDir()
	cfg.MediaCipher = "unsupported" // stop in Run validation, without listeners or bootstrap credentials
	if err := run(cfg); err == nil {
		t.Fatal("invalid server configuration did not stop startup")
	}
	st, err := os.Stat(cfg.DBPath)
	if err != nil || st.Size() == 0 {
		t.Fatalf("normal startup did not create/migrate database: %v", err)
	}
}

func exportSourceLink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
}
