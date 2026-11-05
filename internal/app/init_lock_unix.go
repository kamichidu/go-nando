//go:build linux || darwin

package app

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockInitFile(file *os.File) error   { return unix.Flock(int(file.Fd()), unix.LOCK_EX) }
func unlockInitFile(file *os.File) error { return unix.Flock(int(file.Fd()), unix.LOCK_UN) }
