//go:build windows

package client

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const configFileFullControl windows.ACCESS_MASK = 0x001F01FF

type configDirectory struct {
	handle  windows.Handle
	handles []windows.Handle
}

func (directory *configDirectory) Close() {
	for i := len(directory.handles) - 1; i >= 0; i-- {
		_ = windows.CloseHandle(directory.handles[i])
	}
}

func currentConfigUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read process user SID: %w", err)
	}
	return user.User.Sid, nil
}

func privateConfigSecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	owner, err := currentConfigUserSID()
	if err != nil {
		return nil, err
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: configFileFullControl,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(owner),
		},
	}}, nil)
	if err != nil {
		return nil, err
	}
	descriptor, err := windows.NewSecurityDescriptor()
	if err != nil {
		return nil, err
	}
	if err := descriptor.SetOwner(owner, false); err != nil {
		return nil, err
	}
	if err := descriptor.SetDACL(acl, true, false); err != nil {
		return nil, err
	}
	if err := descriptor.SetControl(windows.SE_DACL_PROTECTED, windows.SE_DACL_PROTECTED); err != nil {
		return nil, err
	}
	return descriptor.ToSelfRelative()
}

func openConfigHandle(path string, directory bool, access, share uint32) (windows.Handle, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	flags := uint32(windows.FILE_ATTRIBUTE_NORMAL | windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(pointer, access, share, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return windows.InvalidHandle, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return handle, nil
}

func inspectConfigType(handle windows.Handle, path string, directory bool) (*windows.ByHandleFileInformation, error) {
	info := new(windows.ByHandleFileInformation)
	if err := windows.GetFileInformationByHandle(handle, info); err != nil {
		return nil, fmt.Errorf("inspect config path %q: %w", path, err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return nil, fmt.Errorf("config path %q is a reparse point", path)
	}
	if (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory {
		return nil, fmt.Errorf("config path %q has the wrong file type", path)
	}
	return info, nil
}

func verifyConfigOwner(handle windows.Handle, path string) error {
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("inspect config owner %q: %w", path, err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return fmt.Errorf("inspect config owner %q: %w", path, err)
	}
	current, err := currentConfigUserSID()
	if err != nil {
		return err
	}
	if !owner.Equals(current) {
		return fmt.Errorf("config path %q is not owned by the current user", path)
	}
	return nil
}

func protectConfigHandle(handle windows.Handle) error {
	descriptor, err := privateConfigSecurityDescriptor()
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
}

func openConfigDirectory(path string, create, protect bool) (*configDirectory, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	volume := filepath.VolumeName(absolute)
	current := volume + string(filepath.Separator)
	remainder := strings.TrimPrefix(absolute, current)
	parts := strings.FieldsFunc(remainder, func(r rune) bool { return r == '\\' || r == '/' })
	directory := &configDirectory{}
	for index, part := range parts {
		current = filepath.Join(current, part)
		access := uint32(windows.FILE_READ_ATTRIBUTES)
		if index == len(parts)-1 && (create || protect) {
			access |= windows.READ_CONTROL | windows.WRITE_DAC
		}
		handle, openErr := openConfigHandle(current, true, access, windows.FILE_SHARE_READ)
		if (errors.Is(openErr, windows.ERROR_FILE_NOT_FOUND) || errors.Is(openErr, windows.ERROR_PATH_NOT_FOUND)) && create {
			if mkdirErr := os.Mkdir(current, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				directory.Close()
				return nil, mkdirErr
			}
			handle, openErr = openConfigHandle(current, true, access, windows.FILE_SHARE_READ)
		}
		if openErr != nil {
			directory.Close()
			return nil, openErr
		}
		if _, err := inspectConfigType(handle, current, true); err != nil {
			_ = windows.CloseHandle(handle)
			directory.Close()
			return nil, err
		}
		directory.handles = append(directory.handles, handle)
	}
	if len(directory.handles) == 0 {
		return nil, fmt.Errorf("config directory %q resolves to a volume root", path)
	}

	directory.handle = directory.handles[len(directory.handles)-1]
	if create || protect {
		if err := verifyConfigOwner(directory.handle, current); err != nil {
			directory.Close()
			return nil, err
		}
	}
	if protect {
		if err := protectConfigHandle(directory.handle); err != nil {
			directory.Close()
			return nil, fmt.Errorf("secure config directory %q: %w", path, err)
		}
	}
	return directory, nil
}

func inspectPrivateConfigHandle(handle windows.Handle, path string) (*windows.ByHandleFileInformation, error) {
	info, err := inspectConfigType(handle, path, false)
	if err != nil {
		return nil, err
	}
	if info.NumberOfLinks != 1 {
		return nil, fmt.Errorf("config file %q has %d hard links, want 1", path, info.NumberOfLinks)
	}
	if err := verifyConfigOwner(handle, path); err != nil {
		return nil, err
	}
	return info, nil
}

func openPrivateFileSnapshot(path string, protectParent bool) (*privateFileSnapshot, error) {
	directory, err := openConfigDirectory(filepath.Dir(path), false, protectParent)
	if err != nil {
		return nil, err
	}

	initialHandle, err := openConfigHandle(path, false,
		windows.GENERIC_READ|windows.READ_CONTROL|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE)
	if err != nil {
		directory.Close()
		return nil, err
	}
	initial, err := inspectPrivateConfigHandle(initialHandle, path)
	if err != nil {
		_ = windows.CloseHandle(initialHandle)
		directory.Close()
		return nil, err
	}
	if err := protectConfigHandle(initialHandle); err != nil {
		_ = windows.CloseHandle(initialHandle)
		directory.Close()
		return nil, fmt.Errorf("secure config file %q: %w", path, err)
	}
	handle, err := openConfigHandle(path, false,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL|windows.DELETE,
		windows.FILE_SHARE_READ)
	if err != nil {
		_ = windows.CloseHandle(initialHandle)
		directory.Close()
		return nil, err
	}
	before, err := inspectPrivateConfigHandle(handle, path)
	_ = windows.CloseHandle(initialHandle)
	if err != nil {
		_ = windows.CloseHandle(handle)
		directory.Close()
		return nil, err
	}
	if !sameWindowsFileIdentity(initial, before) {
		_ = windows.CloseHandle(handle)
		directory.Close()
		return nil, fmt.Errorf("config file %q changed while being secured", path)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		directory.Close()
		return nil, fmt.Errorf("open config file %q: invalid handle", path)
	}
	first, err := io.ReadAll(file)
	if err != nil {
		_ = file.Close()
		directory.Close()
		return nil, fmt.Errorf("read config file %q: %w", path, err)
	}
	middle, err := inspectPrivateConfigHandle(handle, path)
	if err != nil {
		_ = file.Close()
		directory.Close()
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		directory.Close()
		return nil, fmt.Errorf("rewind config file %q: %w", path, err)
	}
	second, err := io.ReadAll(file)
	if err != nil {
		_ = file.Close()
		directory.Close()
		return nil, fmt.Errorf("reread config file %q: %w", path, err)
	}
	after, err := inspectPrivateConfigHandle(handle, path)
	if err != nil {
		_ = file.Close()
		directory.Close()
		return nil, err
	}
	if !sameWindowsFileState(before, middle) || !sameWindowsFileState(middle, after) || !bytes.Equal(first, second) {
		_ = file.Close()
		directory.Close()
		return nil, fmt.Errorf("config file %q changed while being read", path)
	}

	snapshot := &privateFileSnapshot{data: first}
	snapshot.closeFn = func() error {
		fileErr := file.Close()
		directory.Close()
		return fileErr
	}
	scrub := func() error {
		if err := file.Truncate(0); err != nil {
			return &os.PathError{Op: "scrub", Path: path, Err: err}
		}
		if err := file.Sync(); err != nil {
			return &os.PathError{Op: "sync scrubbed", Path: path, Err: err}
		}
		return nil
	}
	verify := func() error {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("rewind config file %q before verification: %w", path, err)
		}
		current, err := io.ReadAll(file)
		if err != nil {
			return fmt.Errorf("revalidate config file %q: %w", path, err)
		}
		state, err := inspectPrivateConfigHandle(handle, path)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, first) || !sameWindowsFileState(after, state) {
			return fmt.Errorf("config file %q changed after it was read", path)
		}
		return nil
	}
	snapshot.verifyFn = verify
	snapshot.removeFn = func() error {
		if err := verify(); err != nil {
			return err
		}
		if err := scrub(); err != nil {
			return err
		}
		deleteFile := byte(1)
		if err := windows.SetFileInformationByHandle(handle, windows.FileDispositionInfo,
			&deleteFile, uint32(unsafe.Sizeof(deleteFile))); err != nil {
			return &os.PathError{Op: "remove", Path: path, Err: err}
		}
		return nil
	}
	return snapshot, nil
}

func sameWindowsFileIdentity(left, right *windows.ByHandleFileInformation) bool {
	return left.VolumeSerialNumber == right.VolumeSerialNumber && left.FileIndexHigh == right.FileIndexHigh &&
		left.FileIndexLow == right.FileIndexLow
}

func sameWindowsFileState(left, right *windows.ByHandleFileInformation) bool {
	return left.VolumeSerialNumber == right.VolumeSerialNumber && left.FileIndexHigh == right.FileIndexHigh &&
		left.FileIndexLow == right.FileIndexLow && left.FileSizeHigh == right.FileSizeHigh && left.FileSizeLow == right.FileSizeLow &&
		left.NumberOfLinks == right.NumberOfLinks && left.LastWriteTime == right.LastWriteTime
}

func writePrivateFile(path string, data []byte) error {
	return writePrivateFileMode(path, data, false)
}

func writePrivateFileIfAbsent(path string, data []byte) error {
	return writePrivateFileMode(path, data, true)
}

func writePrivateFileMode(path string, data []byte, exclusive bool) error {
	directory, err := openConfigDirectory(filepath.Dir(path), true, true)
	if err != nil {
		return err
	}
	defer directory.Close()

	if existing, err := openConfigHandle(path, false, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, windows.FILE_SHARE_READ); err == nil {
		_, inspectErr := inspectPrivateConfigHandle(existing, path)
		_ = windows.CloseHandle(existing)
		if inspectErr != nil {
			return inspectErr
		}
		if exclusive {
			return &os.PathError{Op: "create", Path: path, Err: os.ErrExist}
		}
	} else if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return err
	}

	temporary, temporaryPath, err := createPrivateConfigTemp(filepath.Dir(path), data)
	if err != nil {
		return err
	}
	err = renameFileHandle(windows.Handle(temporary.Fd()), path, !exclusive)
	if err != nil {
		cleanupErr := scrubWindowsPrivateTemp(temporary)
		if exclusive && (errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS)) {
			if cleanupErr != nil {
				return fmt.Errorf("destination exists but temporary config cleanup failed: %w", cleanupErr)
			}
			return &os.PathError{Op: "create", Path: path, Err: os.ErrExist}
		}
		return errors.Join(fmt.Errorf("publish config: %w", err), cleanupErr)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close published config %q: %w", temporaryPath, err)
	}
	return nil
}

func createPrivateConfigTemp(dir string, data []byte) (*os.File, string, error) {
	descriptor, err := privateConfigSecurityDescriptor()
	if err != nil {
		return nil, "", err
	}
	attrs := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	for range 100 {
		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return nil, "", err
		}
		path := filepath.Join(dir, ".gospeak-"+hex.EncodeToString(random))
		pointer, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return nil, "", err
		}
		handle, err := windows.CreateFile(pointer,
			windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL|windows.DELETE,
			0, attrs, windows.CREATE_NEW,
			windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		file := os.NewFile(uintptr(handle), path)
		if file == nil {
			_ = windows.CloseHandle(handle)
			return nil, "", fmt.Errorf("create temporary config: invalid handle")
		}
		if _, err := file.Write(data); err != nil {
			return nil, "", errors.Join(err, scrubWindowsPrivateTemp(file))
		}
		if err := file.Sync(); err != nil {
			return nil, "", errors.Join(err, scrubWindowsPrivateTemp(file))
		}
		return file, path, nil
	}
	return nil, "", fmt.Errorf("too many temporary-name collisions")
}

func scrubWindowsPrivateTemp(file *os.File) error {
	truncateErr := file.Truncate(0)
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(truncateErr, syncErr, closeErr)
}
