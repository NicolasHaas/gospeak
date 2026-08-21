//go:build windows

package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"golang.org/x/sys/windows"
)

func TestReadBootstrapCredentialRejectsPermissiveWindowsACL(t *testing.T) {
	rawToken, err := crypto.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}
	path, err := prepareBootstrapCredentialFile(filepath.Join(t.TempDir(), bootstrapAdminCredentialName), []byte(rawToken+"\n"))
	if err != nil {
		t.Fatalf("prepareBootstrapCredentialFile() error = %v", err)
	}
	defer func() { _ = os.Remove(path) }()
	if got, err := readBootstrapCredential(path); err != nil || got != rawToken {
		t.Fatalf("untouched Windows credential round trip = %q, %v", got, err)
	}

	handle, err := openBootstrapCredentialHandle(path, windows.READ_CONTROL|windows.WRITE_DAC)
	if err != nil {
		t.Fatalf("open credential handle: %v", err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	owner, err := currentProcessSID()
	if err != nil {
		t.Fatalf("currentProcessSID() error = %v", err)
	}
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatalf("CreateWellKnownSid() error = %v", err)
	}
	entries := []windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: bootstrapFileFullControl,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(owner),
			},
		},
		{
			AccessPermissions: windows.GENERIC_READ,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(everyone),
			},
		},
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		t.Fatalf("ACLFromEntries() error = %v", err)
	}
	if err := windows.SetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		t.Fatalf("SetSecurityInfo() error = %v", err)
	}
	if _, err := readBootstrapCredential(path); err == nil {
		t.Fatal("readBootstrapCredential() accepted an Everyone-readable ACL")
	}
}
