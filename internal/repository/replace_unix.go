//go:build !windows

package repository

import "os"

// replaceDestination uses the same-directory rename operation. On Unix this
// atomically replaces an existing non-directory path or creates a new path.
func replaceDestination(tempPath, destinationPath string, _ bool) error {
	return os.Rename(tempPath, destinationPath)
}
