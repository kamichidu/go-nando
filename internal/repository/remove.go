package repository

import (
	"context"
)

// Remove removes an entry (soft delete) from the logical repository.
func (r *Repository) Remove(ctx context.Context, repoPath string) error {
	return r.metaStore.DeleteEntry(ctx, repoPath)
}
