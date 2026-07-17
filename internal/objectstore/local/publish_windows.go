//go:build windows

package local

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

func publishNoReplace(tempPath, finalPath string) (bool, error) {
	tempPtr, err := syscall.UTF16PtrFromString(tempPath)
	if err != nil {
		return false, err
	}
	finalPtr, err := syscall.UTF16PtrFromString(finalPath)
	if err != nil {
		return false, err
	}

	// We specify no flags (0), which means no replace.
	err = windows.MoveFileEx(tempPtr, finalPtr, 0)
	if err == nil {
		return true, nil
	}

	if errno, ok := err.(syscall.Errno); ok {
		if errno == windows.ERROR_ALREADY_EXISTS || errno == windows.ERROR_FILE_EXISTS {
			return false, nil
		}
	}

	return false, err
}
