//go:build !windows

package local

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func publishNoReplace(tempPath, finalPath string) (bool, error) {
	err := os.Link(tempPath, finalPath)
	if err == nil {
		return true, nil
	}
	if os.IsExist(err) {
		return false, nil
	}
	if errors.Is(err, syscall.EXDEV) {
		return false, fmt.Errorf("%w: %w", ErrCrossFilesystem, err)
	}
	return false, err
}
