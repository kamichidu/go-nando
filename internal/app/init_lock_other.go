//go:build !linux && !darwin && !windows

package app

import (
	"errors"
	"os"
)

func lockInitFile(*os.File) error {
	return errors.New("repository initialization locking is unsupported on this OS")
}
func unlockInitFile(*os.File) error {
	return errors.New("repository initialization locking is unsupported on this OS")
}
