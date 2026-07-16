package repository

import (
	"context"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"time"

	"github.com/kamichidu/go-nando/internal/metadata"
)

// Put imports a local file into the repository under the specified logical repository path.
func (r *Repository) Put(ctx context.Context, localPath string, repoPath string) error {
	// 1. Open the input file
	file1, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open local file for hashing: %w", err)
	}
	defer file1.Close()

	// 2. Obtain size, mtime, mode, etc.
	stat, err := file1.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat local file: %w", err)
	}

	if stat.IsDir() {
		return fmt.Errorf("local path %q is a directory, directories cannot be put", localPath)
	}

	// 3. Calculate Content Hash
	digest, err := r.hasher.Sum(ctx, file1)
	if err != nil {
		return fmt.Errorf("failed to calculate hash of local file: %w", err)
	}
	file1.Close() // Close early

	hashStr := digest.String()

	// 4. Check if same hash exists in Object Store
	exists, err := r.objectStore.Exists(ctx, hashStr)
	if err != nil {
		return fmt.Errorf("failed to check if object exists: %w", err)
	}

	if !exists {
		// 5. Save Object (re-open file)
		file2, err := os.Open(localPath)
		if err != nil {
			return fmt.Errorf("failed to reopen local file for storing: %w", err)
		}
		defer file2.Close()

		err = r.objectStore.Put(ctx, hashStr, file2)
		if err != nil {
			return fmt.Errorf("failed to store object: %w", err)
		}
		file2.Close() // Close early

		// 6. Verify Object
		err = r.objectStore.Verify(ctx, hashStr)
		if err != nil {
			return fmt.Errorf("failed to verify stored object: %w", err)
		}
	}

	// 7. Update Metadata
	contentType := mime.TypeByExtension(filepath.Ext(localPath))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	now := time.Now().UTC()
	entry := metadata.Entry{
		Path:        repoPath,
		Hash:        hashStr,
		Size:        stat.Size(),
		Mtime:       stat.ModTime().UTC(),
		Mode:        uint32(stat.Mode()),
		ContentType: contentType,
		State:       metadata.StateReady,
		IsDir:       false,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	err = r.metaStore.PutEntry(ctx, entry)
	if err != nil {
		return fmt.Errorf("failed to save entry metadata: %w", err)
	}

	return nil
}
