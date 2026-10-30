package repository

import (
	"context"
	"time"
)

// Coordinator provides cross-process exclusion for Repository operations.
// Implementations must honor context cancellation while waiting to enter.
type Coordinator interface {
	Exclusive(ctx context.Context, operation func() error) error
	ExclusiveWithLease(ctx context.Context, operationID string, operation func() error) error
	AcquireLease(ctx context.Context, operationID string, ttl time.Duration) error
	RenewLease(ctx context.Context, operationID string, ttl time.Duration) (bool, error)
	InvalidateLease(ctx context.Context, operationID string) error
	InvalidateExpiredLeases(ctx context.Context) ([]string, error)
	ActiveLeaseIDs(ctx context.Context) ([]string, error)
	ForgetExpiredLease(ctx context.Context, operationID string) error
}
