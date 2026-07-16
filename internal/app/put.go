package app

import "context"

// PutInput represents the application-level input for putting a file.
type PutInput struct {
	LocalPath      string
	RepositoryPath string
}

// Put imports a local file into the nando repository.
func (a *App) Put(ctx context.Context, input PutInput) error {
	return a.repo.Put(ctx, input.LocalPath, input.RepositoryPath)
}
