package app

import (
	"context"

	"github.com/kamichidu/go-nando/internal/metadata"
)

// Remove deletes (soft delete) a logical path from the repository.
func (a *App) Remove(ctx context.Context, repoPath string) error {
	return a.repo.Remove(ctx, repoPath)
}

func (a *App) RemoveEntry(ctx context.Context, id metadata.EntryID) error {
	return a.repo.RemoveEntry(ctx, id)
}

// ReclaimDetached resumes cleanup of private metadata buffers after failures
// or daemon restart.
func (a *App) ReclaimDetached(ctx context.Context) error {
	return a.repo.ReclaimDetached(ctx)
}
