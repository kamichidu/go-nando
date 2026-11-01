package app

import (
	"context"
	"io"

	"github.com/kamichidu/go-nando/internal/metadata"
)

// List returns immediate children of the given logical repository path.
func (a *App) List(ctx context.Context, repoPath string) ([]metadata.Entry, error) {
	return a.repo.List(ctx, repoPath)
}

// Stat returns details of a single logical path in the repository.
func (a *App) Stat(ctx context.Context, repoPath string) (metadata.Entry, error) {
	return a.repo.Stat(ctx, repoPath)
}

func (a *App) ResolveEntry(ctx context.Context, id metadata.EntryID) (metadata.Entry, string, error) {
	return a.repo.ResolveEntry(ctx, id)
}

// Cat streams content of the given logical repository path to the writer.
func (a *App) Cat(ctx context.Context, repoPath string, w io.Writer) error {
	return a.repo.Cat(ctx, repoPath, w)
}

// BeginReadResource returns a local immutable object path and a release
// function that holds its Repository content lease until called.
func (a *App) BeginReadResource(ctx context.Context, repoPath string) (metadata.Entry, string, func() error, error) {
	return a.repo.BeginReadResource(ctx, repoPath)
}

func (a *App) BeginReadEntryResource(ctx context.Context, id metadata.EntryID) (metadata.Entry, string, func() error, error) {
	return a.repo.BeginReadEntryResource(ctx, id)
}
