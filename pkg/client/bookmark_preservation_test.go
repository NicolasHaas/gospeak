package client

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBookmarkFailedLoadBlocksSave(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "legacy"}[legacy], func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "servers.yaml")
			original := []byte("bookmarks: [\n# credential-bearing corrupt file\n")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			store := &BookmarkStore{path: path}
			if legacy {
				store.path = filepath.Join(dir, "missing.yaml")
				store.legacyPath = path
			}
			if err := store.Load(); err == nil {
				t.Fatal("malformed load succeeded")
			}
			store.Add(Bookmark{ControlAddr: "example.test:9600", Token: "fixture-token"})
			store.TrustServer("example.test:9600", "fixture-pin")
			if err := store.Save(); err == nil {
				t.Error("save after failed load succeeded")
			}
			assertContentsAndMode(t, path, original, 0o600)
			if legacy {
				if _, err := os.Stat(store.path); err == nil && store.path != path {
					t.Fatal("save silently migrated legacy file")
				}
			}
			if err := os.WriteFile(path, []byte("bookmarks: []\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := store.Load(); err != nil {
				t.Fatal(err)
			}
			if err := store.Save(); err != nil {
				t.Fatalf("successful reload did not unblock save: %v", err)
			}
		})
	}
}

func TestBookmarkMissingLoadAllowsFirstSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.yaml")
	store := &BookmarkStore{path: path, legacyPath: filepath.Join(t.TempDir(), "servers.yaml")}
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if store.path != path {
		t.Fatal("first launch redirected saves to legacy storage")
	}
	store.Add(Bookmark{Token: "fixture-token"})
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestBookmarkReadFailureBlocksSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.yaml")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	store := &BookmarkStore{path: path}
	if err := store.Load(); err == nil {
		t.Fatal("non-file load succeeded")
	}
	// A readable file appearing later must not silently clear the load failure.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	original := []byte("bookmarks: []\n# appeared after failed read\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	store.TrustServer("example.test:9600", "fixture-pin")
	if err := store.Save(); err == nil {
		t.Fatal("read failure did not block pin save")
	}
	assertContentsAndMode(t, path, original, 0o600)
}
