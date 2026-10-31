package app

import (
	"context"

	"github.com/kamichidu/go-nando/internal/repository"
)

// PutInput represents the application-level input for putting a file.
type PutInput struct {
	LocalPath      string
	RepositoryPath string
}

// Put imports a local file into the nando repository.
func (a *App) Put(ctx context.Context, input PutInput) error {
	return a.repo.Put(ctx, input.LocalPath, input.RepositoryPath)
}

// PutExpected commits the write only while its observed target Entry state is
// still current. A zero-value revision is valid only for an observed absence.
func (a *App) PutExpected(ctx context.Context, input PutInput, expected repository.PutExpectation) error {
	return a.repo.PutExpected(ctx, input.LocalPath, input.RepositoryPath, expected)
}
