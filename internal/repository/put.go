package repository

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/paths"
)

// Put imports a local file into the repository under the specified logical repository path.
func (r *Repository) Put(ctx context.Context, localPath string, repoPath string) error {
	// 1. Normalize and validate destination
	normalized, err := paths.NormalizeLogicalPath(repoPath)
	if err != nil {
		return fmt.Errorf("invalid logical destination path: %w", err)
	}

	if normalized == "/" {
		return fmt.Errorf("cannot put a file directly over the root directory")
	}

	// 2. Open the local file once
	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open local file: %w", err)
	}
	defer file.Close()

	localStat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat local file: %w", err)
	}

	if localStat.IsDir() {
		return fmt.Errorf("local path %q is a directory, directories cannot be put", localPath)
	}

	// 3. Resolve or create the destination parent directory
	parentID, err := r.ResolveOrCreateParentDirectories(ctx, normalized)
	if err != nil {
		return err
	}

	baseName := path.Base(normalized)

	// 4. Commit the exact source byte stream through Object Store
	res, err := r.objectStore.Commit(ctx, file)
	if err != nil {
		return fmt.Errorf("failed to commit object: %w", err)
	}

	// 5. Publish or replace committed metadata
	contentType := mime.TypeByExtension(filepath.Ext(localPath))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	now := metadata.TimestampFromTime(time.Now().UTC())

	// Check if entry already exists
	existing, err := r.metaStore.GetChild(ctx, parentID, baseName)
	if err == nil {
		// Entry already exists!
		if existing.Kind == metadata.EntryKindDirectory {
			return fmt.Errorf("cannot replace directory %s with a file: %w", repoPath, metadata.ErrEntryConflict)
		}

		// Update existing file:
		// - retain its immutable EntryID
		// - increment its Revision is done inside metaStore.Update
		// - preserve CreatedAt, update UpdatedAt
		existing.ObjectID = &res.ID
		existing.Size = res.Size
		existing.Mtime = metadata.TimestampFromTime(localStat.ModTime())
		existing.Mode = uint32(localStat.Mode())
		existing.ContentType = contentType

		_, err = r.metaStore.Update(ctx, existing)
		if err != nil {
			return fmt.Errorf("failed to update entry metadata: %w", err)
		}
	} else if errors.Is(err, metadata.ErrEntryNotFound) {
		// Create new file entry
		newEntry := metadata.Entry{
			ID:          metadata.EntryID(uuid.New().String()),
			ParentID:    &parentID,
			Name:        baseName,
			Kind:        metadata.EntryKindFile,
			ObjectID:    &res.ID,
			Size:        res.Size,
			Mtime:       metadata.TimestampFromTime(localStat.ModTime()),
			Mode:        uint32(localStat.Mode()),
			ContentType: contentType,
			Revision:    1,
			CreatedAt:   now,
			UpdatedAt:   now,
		}

		_, err = r.metaStore.Create(ctx, newEntry)
		if err != nil {
			return fmt.Errorf("failed to create entry metadata: %w", err)
		}
	} else {
		return fmt.Errorf("failed to check existing entry: %w", err)
	}

	return nil
}
