package repository

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Get exports a file from the repository to the local filesystem.
func (r *Repository) Get(ctx context.Context, repoPath string, localPath string) error {
	// 1. Retrieve the metadata entry
	entry, err := r.metaStore.GetEntry(ctx, repoPath)
	if err != nil {
		return err
	}

	if entry.IsDir {
		return fmt.Errorf("repository path %q is a directory, directories cannot be exported", repoPath)
	}

	// 2. Open the object from the store
	rc, err := r.objectStore.Open(ctx, entry.Hash)
	if err != nil {
		return fmt.Errorf("failed to open object for metadata entry: %w", err)
	}
	defer rc.Close()

	// 3. Ensure the parent directories of the destination local file exist
	dir := filepath.Dir(localPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create local destination directory %q: %w", dir, err)
	}

	// 4. Create and write to the local file using the preserved mode
	destFile, err := os.OpenFile(localPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(entry.Mode))
	if err != nil {
		return fmt.Errorf("failed to create local file: %w", err)
	}
	defer destFile.Close()

	if _, err := io.Copy(destFile, rc); err != nil {
		return fmt.Errorf("failed to write object content to local file: %w", err)
	}

	if err := destFile.Close(); err != nil {
		return fmt.Errorf("failed to close local file: %w", err)
	}

	// 5. Restore the original modification time (mtime)
	if err := os.Chtimes(localPath, timeNow(), entry.Mtime); err != nil {
		// Non-fatal, but we should log or return it if it is critical. We'll return it wrapped.
		return fmt.Errorf("failed to restore file timestamps: %w", err)
	}

	return nil
}

// timeNow is a helper for testing/mocking time in os.Chtimes.
var timeNow = func() time.Time {
	return timeNowInternal()
}

func timeNowInternal() time.Time {
	return time.Now()
}
