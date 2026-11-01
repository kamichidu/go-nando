package app

import (
	"context"

	"github.com/kamichidu/go-nando/internal/metadata"
)

// MoveInput represents the application-level input for moving a repository path.
type MoveInput struct {
	SourcePath      string
	DestinationPath string
}

// Move renames or moves a logical path (file or directory) in the repository.
func (a *App) Move(ctx context.Context, input MoveInput) error {
	return a.repo.Move(ctx, input.SourcePath, input.DestinationPath)
}

func (a *App) MoveEntry(ctx context.Context, id metadata.EntryID, destination string) error {
	return a.repo.MoveEntry(ctx, id, destination)
}

// Mkdir creates one explicit directory Entry.
func (a *App) Mkdir(ctx context.Context, repositoryPath string, mode uint32) error {
	return a.repo.Mkdir(ctx, repositoryPath, mode)
}
