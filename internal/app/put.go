package app

import (
	"context"

	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/repository"
)

// PutInput represents the application-level input for putting a file.
type PutInput struct {
	LocalPath      string
	RepositoryPath string
}

// Put imports a local file into the nando repository.
func (a *App) Put(ctx context.Context, input PutInput) error {
	err := a.repo.Put(ctx, input.LocalPath, input.RepositoryPath)
	if err == nil {
		a.noteRepositoryWrite()
	}
	return err
}

// PutExpected commits the write only while its observed target Entry state is
// still current. A zero-value revision is valid only for an observed absence.
func (a *App) PutExpected(ctx context.Context, input PutInput, expected repository.PutExpectation) error {
	err := a.repo.PutExpected(ctx, input.LocalPath, input.RepositoryPath, expected)
	if err == nil {
		a.noteRepositoryWrite()
	}
	return err
}

func (a *App) PutExpectedWithMode(ctx context.Context, input PutInput, expected repository.PutExpectation, mode *uint32) error {
	err := a.repo.PutExpectedWithMode(ctx, input.LocalPath, input.RepositoryPath, expected, mode)
	if err == nil {
		a.noteRepositoryWrite()
	}
	return err
}

func (a *App) PutExpectedEntryIDWithMode(ctx context.Context, input PutInput, observed metadata.Entry, mode *uint32) error {
	err := a.repo.PutExpectedEntryIDWithMode(ctx, input.LocalPath, observed, mode)
	if err == nil {
		a.noteRepositoryWrite()
	}
	return err
}

// PutExpectedEntryID commits to the current path of an Entry using its original
// identity and revision as the compare-and-swap expectation.
func (a *App) PutExpectedEntryID(ctx context.Context, input PutInput, observed metadata.Entry) error {
	err := a.repo.PutExpectedEntryID(ctx, input.LocalPath, observed)
	if err == nil {
		a.noteRepositoryWrite()
	}
	return err
}
