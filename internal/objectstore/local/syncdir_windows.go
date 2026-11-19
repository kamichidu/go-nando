//go:build windows

package local

import (
	"fmt"
	"os"
)

func syncDirectory(path string) error {
	// Go does not expose a portable directory flush on Windows. Publication
	// uses MOVEFILE_WRITE_THROUGH, which waits for the move to complete.
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("stat directory for write-through publication: %w", err)
	}
	return nil
}
