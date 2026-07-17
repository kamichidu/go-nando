//go:build !windows

package local

import (
	"os"
)

func publishNoReplace(tempPath, finalPath string) (bool, error) {
	err := os.Link(tempPath, finalPath)
	if err == nil {
		return true, nil
	}
	if os.IsExist(err) {
		return false, nil
	}
	return false, err
}
