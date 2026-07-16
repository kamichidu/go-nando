package app

import "context"

// MoveInput represents the application-level input for moving a repository path.
type MoveInput struct {
	SourcePath      string
	DestinationPath string
}

// Move renames or moves a logical path (file or directory) in the repository.
func (a *App) Move(ctx context.Context, input MoveInput) error {
	return a.repo.Move(ctx, input.SourcePath, input.DestinationPath)
}
