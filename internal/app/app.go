package app

import (
	"context"
	"errors"

	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/repository"
)

// ErrNotImplemented is returned for commands that are not implemented yet.
var ErrNotImplemented = errors.New("command not implemented yet")

// App acts as the application layer orchestrating use-cases.
type App struct {
	repo     *repository.Repository
	activity chan struct{}
}

// NewApp creates a new App instance.
func NewApp(repo *repository.Repository) *App {
	return &App{repo: repo, activity: make(chan struct{}, 1)}
}

func (a *App) noteRepositoryWrite() {
	select {
	case a.activity <- struct{}{}:
	default:
	}
}

func (a *App) DeadProperties(ctx context.Context, id metadata.EntryID) ([]metadata.DeadProperty, error) {
	return a.repo.DeadProperties(ctx, id)
}
func (a *App) SetDeadProperties(ctx context.Context, id metadata.EntryID, changes []metadata.DeadPropertyChange) error {
	return a.repo.SetDeadProperties(ctx, id, changes)
}

// DeriveCanonicalPath reconstructs the logical path of an entry by walking up its ancestry.
func (a *App) DeriveCanonicalPath(ctx context.Context, entry metadata.Entry) (string, error) {
	return a.repo.DeriveCanonicalPath(ctx, entry)
}
