package paths

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrInvalidPath is returned when a logical path is invalid or attempts to escape root.
var ErrInvalidPath = errors.New("invalid logical repository path")

// DefaultRepositoryDir returns the default directory path for storing the nando repository.
// Under macOS: ~/Library/Application Support/nando
// Under Windows: %APPDATA%\nando
// Under Linux: ~/.config/nando (or $XDG_CONFIG_HOME/nando)
func DefaultRepositoryDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "nando"), nil
}

// NormalizeLogicalPath cleans and converts any path to a standardized, absolute, case-sensitive logical path.
func NormalizeLogicalPath(p string) (string, error) {
	// 1. Convert Windows-style backslashes to forward slashes
	normalized := strings.ReplaceAll(p, "\\", "/")

	// 2. Prevent empty paths
	if normalized == "" {
		return "", ErrInvalidPath
	}

	// 3. Ensure absolute path representation
	if !strings.HasPrefix(normalized, "/") {
		normalized = "/" + normalized
	}

	// 4. Resolve dot-notation . and .. recursively
	cleaned := path.Clean(normalized)

	// 5. Verify it did not escape or resolve to relative
	if cleaned == "" || cleaned == "." || !strings.HasPrefix(cleaned, "/") {
		return "", ErrInvalidPath
	}

	return cleaned, nil
}
