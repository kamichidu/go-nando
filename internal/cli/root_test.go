package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewAppAtRepositoryRequiresAbsolutePath(t *testing.T) {
	t.Run("absolute path is accepted", func(t *testing.T) {
		repoDir := filepath.Join(t.TempDir(), "not-initialized")
		_, _, _, err := NewAppAtRepository(context.Background(), repoDir)
		if err == nil || !strings.Contains(err.Error(), "repository is not initialized") {
			t.Fatalf("NewAppAtRepository(%q) error = %v, want uninitialized-repository error", repoDir, err)
		}
	})

	t.Run("relative path is rejected", func(t *testing.T) {
		_, _, _, err := NewAppAtRepository(context.Background(), "relative/repository")
		if err == nil || !strings.Contains(err.Error(), "repository path must be absolute") {
			t.Fatalf("NewAppAtRepository(relative path) error = %v, want absolute-path error", err)
		}
	})
}
