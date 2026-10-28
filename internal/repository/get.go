package repository

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/kamichidu/go-nando/internal/metadata"
)

// ErrDestinationPublished identifies a cleanup failure after the new export
// has already replaced the destination.
var ErrDestinationPublished = errors.New("export destination was published")

// Get exports a file from the repository to the local filesystem.
func (r *Repository) Get(ctx context.Context, repoPath string, localPath string) error {
	return r.coordinator.Exclusive(ctx, func() error {
		return r.get(ctx, repoPath, localPath)
	})
}

func (r *Repository) get(ctx context.Context, repoPath string, localPath string) (retErr error) {
	entry, err := r.ResolvePath(ctx, repoPath)
	if err != nil {
		return err
	}
	if entry.Kind == metadata.EntryKindDirectory {
		return fmt.Errorf("repository path %q is a directory, directories cannot be exported", repoPath)
	}
	if entry.ObjectID == nil {
		return fmt.Errorf("file entry %s has no associated object ID", entry.ID)
	}

	parentDir := filepath.Dir(localPath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return fmt.Errorf("failed to create local destination directory %q: %w", parentDir, err)
	}
	destinationExists, err := validateDestination(localPath)
	if err != nil {
		return err
	}

	reader, err := r.objectStore.Open(ctx, *entry.ObjectID)
	if err != nil {
		return fmt.Errorf("failed to open object for metadata entry: %w", err)
	}
	readerClosed := false
	defer func() {
		if !readerClosed {
			if err := reader.Close(); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("failed to close object reader: %w", err))
			}
		}
	}()

	tempFile, err := os.CreateTemp(parentDir, ".nando-get-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary destination in %q: %w", parentDir, err)
	}
	tempPath := tempFile.Name()
	tempClosed := false
	published := false
	defer func() {
		if !tempClosed {
			if err := tempFile.Close(); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("failed to close temporary destination: %w", err))
			}
		}
		if !published {
			if err := os.Remove(tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				retErr = errors.Join(retErr, fmt.Errorf("failed to remove temporary destination: %w", err))
			}
		}
	}()

	if _, err := io.Copy(tempFile, reader); err != nil {
		return fmt.Errorf("failed to write object content to temporary destination: %w", err)
	}
	if err := tempFile.Chmod(os.FileMode(entry.Mode).Perm()); err != nil {
		return fmt.Errorf("failed to set exported file mode: %w", err)
	}
	if err := os.Chtimes(tempPath, timeNow(), entry.Mtime.Time()); err != nil {
		return fmt.Errorf("failed to restore file timestamps: %w", err)
	}
	if err := tempFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync temporary destination: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("failed to close temporary destination: %w", err)
	}
	tempClosed = true
	if err := reader.Close(); err != nil {
		readerClosed = true
		return fmt.Errorf("failed to close object reader: %w", err)
	}
	readerClosed = true

	if err := replaceDestination(tempPath, localPath, destinationExists); err != nil {
		return fmt.Errorf("failed to atomically publish exported file: %w", err)
	}
	published = true
	return nil
}

// validateDestination rejects paths that Get is not allowed to replace. Lstat
// intentionally examines the final path itself so a symlink is never followed.
func validateDestination(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to inspect export destination %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("export destination %q is a symbolic link", path)
	}
	if info.IsDir() {
		return false, fmt.Errorf("export destination %q is a directory", path)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("export destination %q is not a regular file", path)
	}
	return true, nil
}

// timeNow is a helper for testing/mocking time in os.Chtimes.
var timeNow = func() time.Time {
	return timeNowInternal()
}

func timeNowInternal() time.Time {
	return time.Now()
}
