//go:build windows

package repository

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var replaceFileW = syscall.NewLazyDLL("kernel32.dll").NewProc("ReplaceFileW")
var moveFileExW = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

const moveFileReplaceExisting = 0x1

// replaceDestination uses ReplaceFileW for an existing regular file. A backup
// protects the original from documented failure paths in ReplaceFileW. For an
// absent destination, MoveFileExW without REPLACE_EXISTING makes creation fail
// if another file appears after validation.
func replaceDestination(tempPath, destinationPath string, destinationExists bool) error {
	tempPtr, err := syscall.UTF16PtrFromString(tempPath)
	if err != nil {
		return err
	}
	destinationPtr, err := syscall.UTF16PtrFromString(destinationPath)
	if err != nil {
		return err
	}

	if !destinationExists {
		return moveFile(tempPtr, destinationPtr, 0)
	}

	backupFile, err := os.CreateTemp(filepath.Dir(destinationPath), ".nando-get-backup-*")
	if err != nil {
		return fmt.Errorf("create replacement backup name: %w", err)
	}
	backupPath := backupFile.Name()
	if err := backupFile.Close(); err != nil {
		return errors.Join(fmt.Errorf("close replacement backup placeholder: %w", err), os.Remove(backupPath))
	}
	if err := os.Remove(backupPath); err != nil {
		return fmt.Errorf("remove replacement backup placeholder: %w", err)
	}
	backupPtr, err := syscall.UTF16PtrFromString(backupPath)
	if err != nil {
		return err
	}

	result, _, callErr := replaceFileW.Call(
		uintptr(unsafe.Pointer(destinationPtr)),
		uintptr(unsafe.Pointer(tempPtr)),
		uintptr(unsafe.Pointer(backupPtr)),
		0,
		0,
		0,
	)
	if result == 0 {
		primaryErr := windowsCallError("ReplaceFileW", callErr)
		if _, statErr := os.Lstat(backupPath); statErr == nil {
			if restoreErr := moveFileByPath(backupPath, destinationPath, moveFileReplaceExisting); restoreErr != nil {
				return errors.Join(primaryErr, fmt.Errorf("restore original export destination: %w", restoreErr))
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return errors.Join(primaryErr, fmt.Errorf("inspect replacement backup after ReplaceFileW failure: %w", statErr))
		}
		return primaryErr
	}

	if err := os.Remove(backupPath); err != nil {
		return fmt.Errorf("%w: remove replaced destination backup: %w", ErrDestinationPublished, err)
	}
	return nil
}

func moveFileByPath(from, to string, flags uintptr) error {
	fromPtr, err := syscall.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	toPtr, err := syscall.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return moveFile(fromPtr, toPtr, flags)
}

func moveFile(from, to *uint16, flags uintptr) error {
	result, _, callErr := moveFileExW.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)), flags)
	if result == 0 {
		return windowsCallError("MoveFileExW", callErr)
	}
	return nil
}

func windowsCallError(operation string, err error) error {
	if err == nil || err == syscall.Errno(0) {
		return fmt.Errorf("%s failed without an OS error: %w", operation, syscall.EINVAL)
	}
	return fmt.Errorf("%s: %w", operation, err)
}
