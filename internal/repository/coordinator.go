package repository

import "context"

// Coordinator provides cross-process exclusion for Repository operations.
// Implementations must honor context cancellation while waiting to enter.
type Coordinator interface {
	Exclusive(ctx context.Context, operation func() error) error
}
