package repository

import (
	"context"
	"errors"
	"fmt"
	"path"

	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/paths"
)

// Move renames or moves a logical path (file or directory) inside the repository.
func (r *Repository) Move(ctx context.Context, srcPath, dstPath string) error {
	// 1. Resolve source entry
	srcEntry, err := r.ResolvePath(ctx, srcPath)
	if err != nil {
		return err
	}

	if srcEntry.ID == "root" {
		return fmt.Errorf("cannot move the root directory")
	}

	// 2. Normalize destination
	dstNormalized, err := paths.NormalizeLogicalPath(dstPath)
	if err != nil {
		return fmt.Errorf("invalid destination path: %w", err)
	}

	if dstNormalized == "/" {
		return fmt.Errorf("cannot move over the root directory: %w", metadata.ErrEntryConflict)
	}

	dstParentPath := path.Dir(dstNormalized)
	dstBaseName := path.Base(dstNormalized)

	// 3. Resolve destination parent
	dstParentEntry, err := r.ResolvePath(ctx, dstParentPath)
	if err != nil {
		return fmt.Errorf("failed to resolve destination parent directory %q: %w", dstParentPath, err)
	}

	if dstParentEntry.Kind != metadata.EntryKindDirectory {
		return fmt.Errorf("destination parent %q is a file, not a directory: %w", dstParentPath, metadata.ErrEntryConflict)
	}

	// 4. Check for destination conflict
	_, err = r.metaStore.GetChild(ctx, dstParentEntry.ID, dstBaseName)
	if err == nil {
		return fmt.Errorf("destination component %q already exists: %w", dstBaseName, metadata.ErrEntryConflict)
	}
	if !errors.Is(err, metadata.ErrEntryNotFound) {
		return fmt.Errorf("failed to check destination component: %w", err)
	}

	// 5. Perform the move
	moveEntry := metadata.Entry{
		ID:       srcEntry.ID,
		ParentID: &dstParentEntry.ID,
		Name:     dstBaseName,
		Revision: srcEntry.Revision,
	}
	_, err = r.metaStore.Move(ctx, moveEntry)
	if err != nil {
		return fmt.Errorf("failed to move entry: %w", err)
	}

	return nil
}
