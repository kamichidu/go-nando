package app

import "context"

import (
	"errors"
	"fmt"
	"time"
)

// GC runs Garbage Collection on the repository, returning the count of deleted orphan objects.
func (a *App) GC(ctx context.Context) (int, error) {
	return a.repo.GC(ctx)
}

// ProcessTransferQueue attempts pending GCS transfers once; failures remain queued.
func (a *App) ProcessTransferQueue(ctx context.Context) error {
	return a.repo.ProcessTransferQueue(ctx)
}

// MaintenanceOnce runs each maintenance subtask independently and joins results.
func (a *App) MaintenanceOnce(ctx context.Context) error {
	var failures []error
	if err := a.ProcessTransferQueue(ctx); err != nil {
		failures = append(failures, fmt.Errorf("transfer queue: %w", err))
	}
	if _, err := a.repo.CapacityGC(ctx); err != nil {
		failures = append(failures, fmt.Errorf("capacity GC: %w", err))
	}
	if _, err := a.repo.GC(ctx); err != nil {
		failures = append(failures, fmt.Errorf("orphan/invalid GC: %w", err))
	}
	return errors.Join(failures...)
}

// RunMaintenance runs immediately and then at the caller-selected interval.
func (a *App) RunMaintenance(ctx context.Context, interval time.Duration, report func(error)) error {
	if interval <= 0 {
		return fmt.Errorf("maintenance interval must be positive")
	}
	if err := a.MaintenanceOnce(ctx); err != nil && report != nil {
		report(err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := a.MaintenanceOnce(ctx); err != nil && report != nil {
				report(err)
			}
		}
	}
}

// Doctor verifies repository integrity and checks for corruption.
func (a *App) Doctor(ctx context.Context) error {
	return a.repo.Doctor(ctx)
}
