//go:build !linux

package daemon

import (
	"errors"
	"fmt"
)

// AcquireRepositoryLock is available only on the Phase A Linux runtime.
func AcquireRepositoryLock(string) (func() error, error) {
	return nil, fmt.Errorf("daemon repository locking is not implemented on this platform: %w", errors.ErrUnsupported)
}
