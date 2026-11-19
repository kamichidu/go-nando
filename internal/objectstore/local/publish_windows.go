//go:build windows

package local

import (
	"errors"
	"fmt"
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

	// WRITE_THROUGH requires the move to complete before the call returns.
	// No REPLACE_EXISTING flag preserves immutable object publication.
	err = windows.MoveFileEx(tempPtr, finalPtr, windows.MOVEFILE_WRITE_THROUGH)
	if err == nil {
		return true, nil
	}

	if errno, ok := err.(syscall.Errno); ok {
		if errno == windows.ERROR_ALREADY_EXISTS || errno == windows.ERROR_FILE_EXISTS {
			return false, nil
		}
	}
	if errors.Is(err, windows.ERROR_NOT_SAME_DEVICE) {
		return false, fmt.Errorf("%w: %w", ErrCrossFilesystem, err)
	}

	return false, err
}
