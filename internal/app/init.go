package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	coordinatorsqlite "github.com/kamichidu/go-nando/internal/coordinator/sqlite"
	"github.com/kamichidu/go-nando/internal/metadata/sqlite"
	repositoryformat "github.com/kamichidu/go-nando/internal/repository/format"
)

// InitRepository initializes the directory structure and schema for a new nando repository.
func InitRepository(ctx context.Context, repoDir string) error {
	// 1. Create root directory
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		return fmt.Errorf("failed to create repository root directory: %w", err)
	}
	dbPath := filepath.Join(repoDir, "metadata.sqlite")
	dbExists := true
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		dbExists = false
		entries, readErr := os.ReadDir(repoDir)
		if readErr != nil {
			return fmt.Errorf("failed to inspect repository directory: %w", readErr)
		}
		if len(entries) != 0 {
			return fmt.Errorf("%w: non-empty directory has no repository format metadata", repositoryformat.ErrUnsupported)
		}
	} else if err != nil {
		return fmt.Errorf("failed to inspect metadata database path: %w", err)
	}

	// 2. Validate any existing database before adding repository directories.
	store, err := sqlite.NewStore(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open metadata store: %w", err)
	}
	defer store.Close()

	repoFormat := repositoryformat.Current()
	if err := repoFormat.Validate(); err != nil {
		return err
	}
	if dbExists {
		if err := store.CheckMigrations(ctx); err != nil {
			return fmt.Errorf("failed schema migration validation: %w", err)
		}
		persistedFormat, found, err := store.RepositoryFormat(ctx)
		if err != nil {
			return err
		}
		if !found {
			return repositoryformat.ErrUnsupported
		}
		if err := persistedFormat.Validate(); err != nil {
			return err
		}
	}

	if err := store.Initialize(ctx, repoFormat); err != nil {
		return fmt.Errorf("failed to initialize metadata schema: %w", err)
	}

	// 3. Create object and staging directories after format acceptance.
	objectsDir := filepath.Join(repoDir, "objects")
	if err := os.MkdirAll(objectsDir, 0755); err != nil {
		return fmt.Errorf("failed to create objects directory: %w", err)
	}
	tempDir := filepath.Join(repoDir, "temp")
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}

	coordinator, err := coordinatorsqlite.Open(filepath.Join(repoDir, "coordination.db"))
	if err != nil {
		return fmt.Errorf("failed to open repository coordinator: %w", err)
	}
	if err := coordinator.Exclusive(ctx, func() error { return nil }); err != nil {
		return errors.Join(fmt.Errorf("initialize repository coordinator: %w", err), coordinator.Close())
	}
	if err := coordinator.Close(); err != nil {
		return fmt.Errorf("close repository coordinator: %w", err)
	}

	return nil
}
