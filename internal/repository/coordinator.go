package repository

import (
	"context"
	"time"

	"github.com/kamichidu/go-nando/internal/coordinator"
)

// Coordinator provides cross-process exclusion for Repository operations.
// Implementations must honor context cancellation while waiting to enter.
type Coordinator interface {
	Exclusive(ctx context.Context, operation func() error) error
	AcquireResourceLock(ctx context.Context, resourceID, operationID string, mode coordinator.LockMode, ttl time.Duration) error
	RenewResourceLocks(ctx context.Context, operationID string, ttl time.Duration) (bool, error)
	ValidateResourceLocks(ctx context.Context, operationID string) (bool, error)
	ReleaseResourceLocks(ctx context.Context, operationID string) error
	InvalidateResourceLocks(ctx context.Context, operationID string) error
	InvalidateExpiredResourceLocks(ctx context.Context) ([]string, error)
	ActiveResourceOperationIDs(ctx context.Context) ([]string, error)
	ForgetExpiredResourceLocks(ctx context.Context, operationID string) error
}
