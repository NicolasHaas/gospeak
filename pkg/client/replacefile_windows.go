//go:build windows

package client

import (
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

type fileRenameInfoHeader struct {
	replaceIfExists uint32
	rootDirectory   windows.Handle
	fileNameLength  uint32
}

func renameFileHandle(handle windows.Handle, destination string, replace bool) error {
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	name, err := windows.UTF16FromString(absolute)
	if err != nil {
		return err
	}
	name = name[:len(name)-1]
	header := fileRenameInfoHeader{}
	if replace {
		header.replaceIfExists = 1
	}
	nameOffset := unsafe.Offsetof(header.fileNameLength) + unsafe.Sizeof(header.fileNameLength)
	buffer := make([]byte, nameOffset+uintptr(len(name))*unsafe.Sizeof(name[0]))
	stored := (*fileRenameInfoHeader)(unsafe.Pointer(&buffer[0]))
	*stored = header
	stored.fileNameLength = uint32(len(name) * 2)
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&buffer[nameOffset])), len(name)), name)
	return windows.SetFileInformationByHandle(handle, windows.FileRenameInfo, &buffer[0], uint32(len(buffer)))
}
