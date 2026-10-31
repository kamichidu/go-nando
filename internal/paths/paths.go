package paths

import (
	"errors"
	"os"
	"path"
	"runtime"
	"strings"

	"github.com/kamichidu/go-nando/internal/config"
)

// ErrInvalidPath is returned when a logical path is invalid or attempts to escape root.
var ErrInvalidPath = errors.New("invalid logical repository path")

// DefaultRepositoryDir returns the XDG-style data directory, separate from
// per-user configuration on every supported platform.
func DefaultRepositoryDir() (string, error) {
	env := map[string]string{
		"HOME": os.Getenv("HOME"), "USERPROFILE": os.Getenv("USERPROFILE"),
		"XDG_CONFIG_HOME": os.Getenv("XDG_CONFIG_HOME"), "XDG_DATA_HOME": os.Getenv("XDG_DATA_HOME"),
	}
	resolved, err := config.ResolvePaths(runtime.GOOS, env)
	if err != nil {
		return "", err
	}
	return resolved.DataDir, nil
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

	// Reject any attempt to escape above repository root
	parts := strings.Split(normalized, "/")
	depth := 0
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			depth--
			if depth < 0 {
				return "", ErrInvalidPath
			}
		} else {
			depth++
		}
	}

	// 4. Resolve dot-notation . and .. recursively
	cleaned := path.Clean(normalized)

	// 5. Verify it did not escape or resolve to relative
	if cleaned == "" || cleaned == "." || !strings.HasPrefix(cleaned, "/") {
		return "", ErrInvalidPath
	}

	return cleaned, nil
}
