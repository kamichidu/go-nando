package repository

import (
	"context"
	"fmt"

	"github.com/kamichidu/go-nando/internal/metadata"
)

// Remove atomically detaches a file or collection from the public namespace.
// Detached metadata is reclaimed asynchronously in resumable per-entry steps.
func (r *Repository) Remove(ctx context.Context, repoPath string) error {
	// 1. Resolve logical entry
	entry, err := r.ResolvePath(ctx, repoPath)
	if err != nil {
		return err
	}

	if entry.ID == "root" {
		return fmt.Errorf("cannot remove the root directory")
	}
	if entry.ParentID == nil {
		return fmt.Errorf("cannot remove an entry without a public parent")
	}
	return r.detachAndReclaim(ctx, entry, entry.Revision, *entry.ParentID)
}

// removeTreeEntry detaches the current stable Entry ID, used by ID-based adapter
// operations such as MOVE replacement cleanup.
func (r *Repository) removeTreeEntry(ctx context.Context, entry metadata.Entry) error {
	if r.bufferStore == nil {
		return fmt.Errorf("metadata store does not support internal deletion buffers")
	}
	current, err := r.metaStore.GetByID(ctx, entry.ID)
	if err != nil {
		return err
	}
	if current.ParentID == nil {
		return fmt.Errorf("cannot detach root entry %q", entry.ID)
	}
	return r.detachAndReclaim(ctx, current, current.Revision, *current.ParentID)
}

func (r *Repository) detachAndReclaim(ctx context.Context, current metadata.Entry, expectedRevision uint64, expectedParent metadata.EntryID) error {
	if r.bufferStore == nil {
		return fmt.Errorf("metadata store does not support internal deletion buffers")
	}
	_, err := r.bufferStore.DetachTree(ctx, current.ID, expectedRevision, expectedParent)
	if err != nil {
		return fmt.Errorf("detach entry %q from the public namespace: %w", current.Name, err)
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
	return r.removeTreeEntry(ctx, entry)
}
