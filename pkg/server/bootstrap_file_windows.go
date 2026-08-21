//go:build windows

package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

const bootstrapFileFullControl windows.ACCESS_MASK = 0x001F01FF

func currentProcessSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read process user SID: %w", err)
	}
	return user.User.Sid, nil
}

func bootstrapSecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	owner, err := currentProcessSID()
	if err != nil {
		return nil, err
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: bootstrapFileFullControl,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(owner),
		},
	}}, nil)
	if err != nil {
		return nil, fmt.Errorf("build credential ACL: %w", err)
	}
	descriptor, err := windows.NewSecurityDescriptor()
	if err != nil {
		return nil, fmt.Errorf("create credential security descriptor: %w", err)
	}
	if err := descriptor.SetOwner(owner, false); err != nil {
		return nil, fmt.Errorf("set credential owner: %w", err)
	}
	if err := descriptor.SetDACL(acl, true, false); err != nil {
		return nil, fmt.Errorf("set credential DACL: %w", err)
	}
	if err := descriptor.SetControl(windows.SE_DACL_PROTECTED, windows.SE_DACL_PROTECTED); err != nil {
		return nil, fmt.Errorf("protect credential DACL: %w", err)
	}
	selfRelative, err := descriptor.ToSelfRelative()
	if err != nil {
		return nil, fmt.Errorf("encode credential security descriptor: %w", err)
	}
	return selfRelative, nil
}

func prepareBootstrapCredentialFile(path string, data []byte) (string, error) {
	descriptor, err := bootstrapSecurityDescriptor()
	if err != nil {
		return "", err
	}
	attrs := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	for range 100 {
		randomSuffix := make([]byte, 16)
		if _, err := rand.Read(randomSuffix); err != nil {
			return "", fmt.Errorf("generate temporary credential name: %w", err)
		}
		tempPath := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp-"+hex.EncodeToString(randomSuffix))
		pathPtr, err := windows.UTF16PtrFromString(tempPath)
		if err != nil {
			return "", err
		}
		handle, err := windows.CreateFile(
			pathPtr,
			windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL,
			0,
			attrs,
			windows.CREATE_NEW,
			windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
			0,
		)
		if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			continue
		}
		if err != nil {
			return "", &os.PathError{Op: "create", Path: tempPath, Err: err}
		}
		file := os.NewFile(uintptr(handle), tempPath)
		if file == nil {
			_ = windows.CloseHandle(handle)
			_ = os.Remove(tempPath)
			return "", fmt.Errorf("create credential file %q: invalid handle", tempPath)
		}
		if _, err := file.Write(data); err != nil {
			_ = file.Close()
			_ = os.Remove(tempPath)
			return "", fmt.Errorf("write credential file %q: %w", tempPath, err)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			_ = os.Remove(tempPath)
			return "", fmt.Errorf("sync credential file %q: %w", tempPath, err)
		}
		if err := file.Close(); err != nil {
			_ = os.Remove(tempPath)
			return "", fmt.Errorf("close credential file %q: %w", tempPath, err)
		}
		return tempPath, nil
	}
	return "", fmt.Errorf("create temporary credential file: too many name collisions")
}

// Windows does not provide a portable directory fsync equivalent. The file is
// flushed before its hard-link is published.
func syncBootstrapCredentialDirectory(string) error {
	return nil
}

func openBootstrapCredentialHandle(path string, access uint32) (windows.Handle, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	handle, err := windows.CreateFile(
		pathPtr,
		access,
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return windows.InvalidHandle, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return handle, nil
}

func readBootstrapCredentialFile(path string) ([]byte, error) {
	handle, err := openBootstrapCredentialHandle(path, windows.GENERIC_READ|windows.READ_CONTROL)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("open credential file %q: invalid handle", path)
	}
	defer func() { _ = file.Close() }()

	if fileType, err := windows.GetFileType(handle); err != nil || fileType != windows.FILE_TYPE_DISK {
		return nil, fmt.Errorf("credential path %q is not a disk file", path)
	}
	info := new(windows.ByHandleFileInformation)
	if err := windows.GetFileInformationByHandle(handle, info); err != nil {
		return nil, fmt.Errorf("inspect credential file %q: %w", path, err)
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return nil, fmt.Errorf("credential path %q is not a regular file", path)
	}
	size := uint64(info.FileSizeHigh)<<32 | uint64(info.FileSizeLow)
	if size != bootstrapTokenHexLength+1 {
		return nil, fmt.Errorf("credential file %q has an invalid size", path)
	}
	if err := verifyBootstrapCredentialACL(handle, path); err != nil {
		return nil, err
	}

	data, err := io.ReadAll(io.LimitReader(file, bootstrapTokenHexLength+2))
	if err != nil {
		return nil, fmt.Errorf("read credential file %q: %w", path, err)
	}
	if len(data) != bootstrapTokenHexLength+1 {
		return nil, fmt.Errorf("credential file %q has an invalid size", path)
	}
	return data, nil
}

func verifyBootstrapCredentialACL(handle windows.Handle, path string) error {
	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("inspect credential ACL %q: %w", path, err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return fmt.Errorf("inspect credential ACL %q: %w", path, err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("credential file %q ACL inherits permissions", path)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return fmt.Errorf("inspect credential owner %q: %w", path, err)
	}
	processSID, err := currentProcessSID()
	if err != nil {
		return err
	}
	if !owner.Equals(processSID) {
		return fmt.Errorf("credential file %q is not owned by the server process user", path)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 {
		return fmt.Errorf("credential file %q does not have an owner-only ACL", path)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		return fmt.Errorf("inspect credential ACL %q: %w", path, err)
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&(windows.INHERITED_ACE|windows.INHERIT_ONLY_ACE) != 0 {
		return fmt.Errorf("credential file %q ACL contains an unsupported entry", path)
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !sid.Equals(owner) || ace.Mask != bootstrapFileFullControl {
		return fmt.Errorf("credential file %q ACL does not grant only its owner full access", path)
	}
	return nil
}
