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

// RunScheduler runs the daemon-owned transfer and GC subtasks independently.
// Transfer checks are periodic; a new write schedules an idle check, while the
// maximum delay bounds how long a continuously busy repository can defer work.
func (a *App) RunScheduler(ctx context.Context, idleInterval, periodicInterval, maxDeliveryDelay time.Duration, parallelism int, report func(string, error)) error {
	if idleInterval <= 0 || periodicInterval <= 0 || maxDeliveryDelay <= 0 || parallelism <= 0 {
		return fmt.Errorf("scheduler durations and parallelism must be positive")
	}
	periodic := time.NewTicker(periodicInterval)
	gcTicker := time.NewTicker(periodicInterval)
	defer periodic.Stop()
	defer gcTicker.Stop()
	maximum := time.NewTimer(time.Hour)
	defer maximum.Stop()
	idle := time.NewTimer(idleInterval)
	defer idle.Stop()
	idleC := idle.C
	resetIdle := func(d time.Duration) {
		if !idle.Stop() {
			select {
			case <-idle.C:
			default:
			}
		}
		idle.Reset(d)
		idleC = idle.C
	}
	var transferRunning, capacityRunning, orphanRunning bool
	var transferRetryAt time.Time
	transferDone := make(chan error, 1)
	capacityDone, orphanDone := make(chan error, 1), make(chan error, 1)
	startTransfer := func() {
		if transferRunning || (!transferRetryAt.IsZero() && time.Now().Before(transferRetryAt)) {
			return
		}
		transferRunning = true
		go func() { transferDone <- a.repo.ProcessTransferQueueParallel(ctx, parallelism) }()
	}
	startCapacityGC := func() {
		if capacityRunning {
			return
		}
		capacityRunning = true
		go func() { _, err := a.repo.CapacityGC(ctx); capacityDone <- err }()
	}
	startOrphanGC := func() {
		if orphanRunning {
			return
		}
		orphanRunning = true
		go func() { _, err := a.repo.GC(ctx); orphanDone <- err }()
	}
	resetMaximum := func() {
		oldest, found, err := a.repo.OldestPendingTransfer(ctx)
		if err != nil {
			if report != nil {
				report("transfer queue age", err)
			}
			return
		}
		delay := maxDeliveryDelay
		if found {
			delay = time.Until(oldest.Add(maxDeliveryDelay))
			if !transferRetryAt.IsZero() && transferRetryAt.After(time.Now()) && time.Until(transferRetryAt) > delay {
				delay = time.Until(transferRetryAt)
			}
			if delay <= 0 {
				delay = time.Nanosecond
			}
		}
		if !maximum.Stop() {
			select {
			case <-maximum.C:
			default:
			}
		}
		maximum.Reset(delay)
	}
	resetMaximum()
	for {
		select {
		case <-ctx.Done():
			if transferRunning {
				if err := <-transferDone; err != nil && report != nil {
					report("transfer queue shutdown", err)
				}
			}
			if capacityRunning {
				if err := <-capacityDone; err != nil && report != nil {
					report("capacity GC shutdown", err)
				}
			}
			if orphanRunning {
				if err := <-orphanDone; err != nil && report != nil {
					report("orphan/invalid GC shutdown", err)
				}
			}
			return nil
		case <-periodic.C:
			startTransfer()
		case <-maximum.C:
			startTransfer()
		case <-gcTicker.C:
			startCapacityGC()
			startOrphanGC()
		case <-idleC:
			startTransfer()
			resetIdle(idleInterval)
		case <-a.activity:
			resetIdle(idleInterval)
		case err := <-transferDone:
			transferRunning = false
			if err != nil && report != nil {
				report("transfer queue", err)
			}
			if err != nil {
				transferRetryAt = time.Now().Add(periodicInterval)
			} else {
				transferRetryAt = time.Time{}
			}
			resetMaximum()
		case err := <-capacityDone:
			capacityRunning = false
			if err != nil && report != nil {
				report("capacity GC", err)
			}
		case err := <-orphanDone:
			orphanRunning = false
			if err != nil && report != nil {
				report("orphan/invalid GC", err)
			}
		}
	}
}

// Doctor verifies repository integrity and checks for corruption.
func (a *App) Doctor(ctx context.Context) error {
	return a.repo.Doctor(ctx)
}
