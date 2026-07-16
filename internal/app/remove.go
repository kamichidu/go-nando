package app

import "context"

// Remove deletes (soft delete) a logical path from the repository.
func (a *App) Remove(ctx context.Context, repoPath string) error {
	return a.repo.Remove(ctx, repoPath)
}
