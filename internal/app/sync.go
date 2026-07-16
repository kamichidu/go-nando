package app

import "context"

// Sync is not implemented yet. It returns ErrNotImplemented as required.
func (a *App) Sync(ctx context.Context) error {
	return ErrNotImplemented
}
