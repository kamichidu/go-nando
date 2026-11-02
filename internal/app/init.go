package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	coordinatorsqlite "github.com/kamichidu/go-nando/internal/coordinator/sqlite"
	"github.com/kamichidu/go-nando/internal/metadata/sqlite"
	"github.com/kamichidu/go-nando/internal/repository"
	repositoryformat "github.com/kamichidu/go-nando/internal/repository/format"
)

// InitRepository initializes the directory structure and schema for a new nando repository.
func InitRepository(ctx context.Context, repoDir string) error {
	return InitRepositoryWithSettings(ctx, repoDir, repository.DefaultSettings())
}

// InitRepositoryWithSettings initializes a repository with its delivery settings.
// Existing repositories are never silently reconfigured.
func InitRepositoryWithSettings(ctx context.Context, repoDir string, settings repository.Settings) error {
	if err := settings.Validate(); err != nil {
		return fmt.Errorf("invalid repository settings: %w", err)
	}
	settings = settings.Canonical()
	// 1. Create root directory
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
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
	settingsPath := filepath.Join(repoDir, "repository.json")
	if dbExists {
		persisted, err := LoadRepositorySettings(repoDir)
		if errors.Is(err, os.ErrNotExist) {
			if settings != repository.DefaultSettings() {
				return fmt.Errorf("existing repository without settings is treated as Local; mode migration is not supported")
			}
		} else if err != nil {
			return fmt.Errorf("read repository settings: %w", err)
		} else {
			if settings != repository.DefaultSettings() && settings != persisted {
				return fmt.Errorf("existing repository settings cannot be changed by init")
			}
			settings = persisted
		}
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
	if err := os.MkdirAll(objectsDir, 0o755); err != nil {
		return fmt.Errorf("failed to create objects directory: %w", err)
	}
	tempDir := filepath.Join(repoDir, "temp")
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
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

	if !dbExists {
		file, err := os.OpenFile(settingsPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("create repository settings: %w", err)
		}
		encodeErr := json.NewEncoder(file).Encode(settings)
		closeErr := file.Close()
		if err := errors.Join(encodeErr, closeErr); err != nil {
			return fmt.Errorf("write repository settings: %w", err)
		}
	}
	return nil
}

// LoadRepositorySettings reads repository-owned delivery settings. Repositories
// predating repository.json retain Local behavior for compatibility.
func LoadRepositorySettings(repoDir string) (repository.Settings, error) {
	file, err := os.Open(filepath.Join(repoDir, "repository.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return repository.DefaultSettings(), nil
		}
		return repository.Settings{}, err
	}
	defer file.Close()
	var settings repository.Settings
	if err := json.NewDecoder(file).Decode(&settings); err != nil {
		return repository.Settings{}, err
	}
	if err := settings.Validate(); err != nil {
		return repository.Settings{}, err
	}
	return settings, nil
}
