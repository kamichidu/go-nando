package app

import "context"

// GC runs Garbage Collection on the repository, returning the count of deleted orphan objects.
func (a *App) GC(ctx context.Context) (int, error) {
	return a.repo.GC(ctx)
}

// Doctor verifies repository integrity and checks for corruption.
func (a *App) Doctor(ctx context.Context) error {
	return a.repo.Doctor(ctx)
}
