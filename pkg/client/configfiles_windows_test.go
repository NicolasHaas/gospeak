//go:build windows

package client

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestReadPrivateFileRepairsPermissiveWindowsACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gospeak", "servers.yaml")
	if err := writePrivateFile(path, []byte("bookmarks: []\n")); err != nil {
		t.Fatal(err)
	}
	handle, err := openConfigHandle(path, false, windows.READ_CONTROL|windows.WRITE_DAC, windows.FILE_SHARE_READ)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := currentConfigUserSID()
	if err != nil {
		_ = windows.CloseHandle(handle)
		t.Fatal(err)
	}
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		_ = windows.CloseHandle(handle)
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		windowsExplicitAccess(owner, windows.TRUSTEE_IS_USER, configFileFullControl),
		windowsExplicitAccess(everyone, windows.TRUSTEE_IS_WELL_KNOWN_GROUP, windows.GENERIC_READ),
	}, nil)
	if err != nil {
		_ = windows.CloseHandle(handle)
		t.Fatal(err)
	}
	if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		_ = windows.CloseHandle(handle)
		t.Fatal(err)
	}
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}

	if _, err := readPrivateFile(path, true); err != nil {
		t.Fatal(err)
	}
	assertOwnerOnlyConfigACL(t, path)
}

func windowsExplicitAccess(sid *windows.SID, trusteeType windows.TRUSTEE_TYPE, permissions windows.ACCESS_MASK) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: permissions,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  trusteeType,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}

func assertOwnerOnlyConfigACL(t *testing.T, path string) {
	t.Helper()
	handle, err := openConfigHandle(path, false, windows.READ_CONTROL, windows.FILE_SHARE_READ)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("config DACL still inherits permissions")
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		t.Fatalf("read config owner: %v", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 {
		t.Fatalf("config DACL does not contain exactly one ACE: %v", err)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !sid.Equals(owner) || ace.Mask != configFileFullControl {
		t.Fatal("config DACL is not owner-only full control")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateFileSnapshotRejectsPreexistingWindowsWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gospeak", "servers.yaml")
	data := []byte("bookmarks:\n  - token: secret\n")
	if err := writePrivateFile(path, data); err != nil {
		t.Fatal(err)
	}
	writer, err := os.OpenFile(path, os.O_RDWR, 0) //nolint:gosec // controlled temporary test path
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	snapshot, err := openPrivateFileSnapshot(path, true)
	if err == nil {
		_ = snapshot.Close()
		t.Fatal("snapshot opened while a writable handle was active")
	}
	got, readErr := os.ReadFile(path) //nolint:gosec // controlled temporary test path
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(data) {
		t.Fatalf("config changed while writer was active: got %q, want %q", got, data)
	}
}

func TestPrivateFileSnapshotRemovesWindowsIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gospeak", "servers.yaml")
	if err := writePrivateFile(path, []byte("bookmarks:\n  - token: secret\n")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := openPrivateFileSnapshot(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snapshot.Close() }()
	if err := snapshot.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("removed Windows config still exists: %v", err)
	}
}

func TestWindowsTemporaryHandlePreventsPathSubstitution(t *testing.T) {
	dir := t.TempDir()
	temporary, temporaryPath, err := createPrivateConfigTemp(dir, []byte("expected"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = scrubWindowsPrivateTemp(temporary) }()

	moved := temporaryPath + ".moved"
	if err := os.Rename(temporaryPath, moved); err == nil {
		t.Fatal("renamed an open private temporary file")
	}
	replacement := filepath.Join(dir, "replacement")
	if err := os.WriteFile(replacement, []byte("substituted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, temporaryPath); err == nil {
		t.Fatal("replaced an open private temporary file")
	}

	destination := filepath.Join(dir, "published")
	if err := renameFileHandle(windows.Handle(temporary.Fd()), destination, false); err != nil {
		t.Fatal(err)
	}
	if err := temporary.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination) //nolint:gosec // temporary test path
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "expected" {
		t.Fatalf("published substituted data: %q", data)
	}
}

func TestWindowsFailedExclusivePublishScrubsExactTemporary(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "published")
	if err := os.WriteFile(destination, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	temporary, temporaryPath, err := createPrivateConfigTemp(dir, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if err := renameFileHandle(windows.Handle(temporary.Fd()), destination, false); err == nil {
		t.Fatal("exclusive handle rename replaced the destination")
	}
	if err := scrubWindowsPrivateTemp(temporary); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(temporaryPath) //nolint:gosec // temporary test path
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("failed publication left temporary secret data: %q", data)
	}
	current, err := os.ReadFile(destination) //nolint:gosec // temporary test path
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != "current" {
		t.Fatalf("exclusive publication changed destination: %q", current)
	}
}
