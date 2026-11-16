package repository

import (
	"context"
	"fmt"

	"github.com/kamichidu/go-nando/internal/metadata"
)

// Remove removes a file or collection from the logical repository. Collections
// are removed bottom-up so their descendants cannot remain reachable after a
// successful return.
func (r *Repository) Remove(ctx context.Context, repoPath string) error {
	// 1. Resolve logical entry
	entry, err := r.ResolvePath(ctx, repoPath)
	if err != nil {
		return err
	}

	if entry.ID == "root" {
		return fmt.Errorf("cannot remove the root directory")
	}

	return r.removeTreeEntry(ctx, entry)
}

// removeTreeEntry removes descendants before the entry. MetadataStore.Delete
// is atomic for one entry and rejects non-empty directories, but the store
// contract does not expose a multi-entry transaction; an error can therefore
// leave already deleted descendants removed while the collection remains.
func (r *Repository) removeTreeEntry(ctx context.Context, entry metadata.Entry) error {
	if entry.Kind == metadata.EntryKindDirectory {
		children, err := r.metaStore.ListChildren(ctx, entry.ID)
		if err != nil {
			return fmt.Errorf("failed to list children of %q: %w", entry.Name, err)
		}
		for _, child := range children {
			if err := r.removeTreeEntry(ctx, child); err != nil {
				return fmt.Errorf("failed to remove descendant %q: %w", child.Name, err)
			}
		}
	}
	if err := r.metaStore.Delete(ctx, entry.ID); err != nil {
		return fmt.Errorf("failed to delete entry %q: %w", entry.Name, err)
	}
	return nil
}

// RemoveEntry removes exactly the Entry identified by its stable ID.
func (r *Repository) RemoveEntry(ctx context.Context, id metadata.EntryID) error {
	entry, err := r.metaStore.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if entry.ID == "root" {
		return fmt.Errorf("cannot remove the root directory")
	}
	if entry.Kind == metadata.EntryKindDirectory {
		children, err := r.metaStore.ListChildren(ctx, entry.ID)
		if err != nil {
			return fmt.Errorf("failed to check child entries: %w", err)
		}
		if len(children) > 0 {
			return fmt.Errorf("cannot remove non-empty directory: %w", metadata.ErrEntryConflict)
		}
	}
	if err := r.metaStore.Delete(ctx, entry.ID); err != nil {
		return fmt.Errorf("failed to delete entry %s: %w", id, err)
	}
	return nil
}
