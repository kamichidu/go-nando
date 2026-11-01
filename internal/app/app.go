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

// DeriveCanonicalPath reconstructs the logical path of an entry by walking up its ancestry.
func (a *App) DeriveCanonicalPath(ctx context.Context, entry metadata.Entry) (string, error) {
	return a.repo.DeriveCanonicalPath(ctx, entry)
}
