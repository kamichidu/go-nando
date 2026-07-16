package app

import "context"

// GetInput represents the application-level input for exporting a file.
type GetInput struct {
	RepositoryPath string
	LocalPath      string
}

// Get exports a file from the logical repository to the local filesystem.
func (a *App) Get(ctx context.Context, input GetInput) error {
	return a.repo.Get(ctx, input.RepositoryPath, input.LocalPath)
}
