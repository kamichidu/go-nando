package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kamichidu/go-nando/internal/metadata/sqlite"
)

// InitRepository initializes the directory structure and schema for a new nando repository.
func InitRepository(ctx context.Context, repoDir string) error {
	// 1. Create root directory
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		return fmt.Errorf("failed to create repository root directory: %w", err)
	}

	// 2. Create objects directory
	objectsDir := filepath.Join(repoDir, "objects")
	if err := os.MkdirAll(objectsDir, 0755); err != nil {
		return fmt.Errorf("failed to create objects directory: %w", err)
	}

	// 3. Create temp directory
	tempDir := filepath.Join(repoDir, "temp")
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}

	// 4. Initialize metadata SQLite database
	dbPath := filepath.Join(repoDir, "metadata.sqlite")
	store, err := sqlite.NewStore(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open metadata store: %w", err)
	}
	defer store.Close()

	if err := store.Initialize(ctx); err != nil {
		return fmt.Errorf("failed to initialize metadata schema: %w", err)
	}

	return nil
}
