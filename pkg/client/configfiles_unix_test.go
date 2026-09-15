//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package client

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateFileSnapshotDoesNotRemoveReplacementPath(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "settings.yaml")
	movedPath := filepath.Join(dir, "moved-original.yaml")
	original := []byte("audio_input: Original Secret\n")
	replacement := []byte("audio_input: Replacement\n")
	if err := os.WriteFile(legacyPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := openPrivateFileSnapshot(legacyPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snapshot.Close() }()
	if err := os.Rename(legacyPath, movedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, replacement, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := snapshot.Remove(); err == nil {
		t.Fatal("removed a pathname replacement")
	}
	moved, err := os.ReadFile(movedPath) //nolint:gosec // temporary test path
	if err != nil {
		t.Fatal(err)
	}
	if string(moved) != string(original) {
		t.Fatalf("moved original changed: got %q, want %q", moved, original)
	}
	gotReplacement, err := os.ReadFile(legacyPath) //nolint:gosec // temporary test path
	if err != nil {
		t.Fatal(err)
	}
	if string(gotReplacement) != string(replacement) {
		t.Fatalf("replacement changed: got %q, want %q", gotReplacement, replacement)
	}
}

func TestPrivateFileSnapshotRejectsMutationBeforeRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.yaml")
	original := []byte("audio_input: Original\n")
	mutated := []byte("audio_input: Mutated!\n")
	if len(original) != len(mutated) {
		t.Fatal("fixture sizes differ")
	}
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := openPrivateFileSnapshot(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snapshot.Close() }()
	if err := os.WriteFile(path, mutated, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Remove(); err == nil {
		t.Fatal("removed a legacy file mutated after its snapshot")
	}
	assertContentsAndMode(t, path, mutated, 0o600)
}

func TestPrivateFileSnapshotRejectsLateHardLinkBeforeRemoval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.yaml")
	linkedPath := filepath.Join(dir, "late-link.yaml")
	data := []byte("bookmarks:\n  - token: secret\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := openPrivateFileSnapshot(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snapshot.Close() }()
	if err := os.Link(path, linkedPath); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if err := snapshot.Remove(); err == nil {
		t.Fatal("removed a legacy file after a hard link appeared")
	}
	assertContentsAndMode(t, path, data, 0o600)
	assertContentsAndMode(t, linkedPath, data, 0o600)
}
