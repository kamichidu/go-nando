package app

import (
	"errors"

	"github.com/kamichidu/go-nando/internal/repository"
)

// ErrNotImplemented is returned for commands that are not implemented yet.
var ErrNotImplemented = errors.New("command not implemented yet")

// App acts as the application layer orchestrating use-cases.
type App struct {
	repo *repository.Repository
}

// NewApp creates a new App instance.
func NewApp(repo *repository.Repository) *App {
	return &App{repo: repo}
}
