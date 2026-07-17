package repository

import (
	"context"
	"fmt"

	"github.com/kamichidu/go-nando/internal/metadata"
)

// Remove removes an entry from the logical repository.
func (r *Repository) Remove(ctx context.Context, repoPath string) error {
	// 1. Resolve logical entry
	entry, err := r.ResolvePath(ctx, repoPath)
	if err != nil {
		return err
	}

	if entry.ID == "root" {
		return fmt.Errorf("cannot remove the root directory")
	}

	// 2. If it is a directory, check if it's empty
	if entry.Kind == metadata.EntryKindDirectory {
		children, err := r.metaStore.ListChildren(ctx, entry.ID)
		if err != nil {
			return fmt.Errorf("failed to check child entries: %w", err)
		}
		if len(children) > 0 {
			return fmt.Errorf("cannot remove non-empty directory: %w", metadata.ErrEntryConflict)
		}
	}

	// 3. Delete the entry
	err = r.metaStore.Delete(ctx, entry.ID)
	if err != nil {
		return fmt.Errorf("failed to delete entry: %w", err)
	}

	return nil
}
