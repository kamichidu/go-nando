package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/coordinator"
)

// ProcessTransferQueue attempts every currently pending content ID once. Failed
// rows remain durable for the next Maintenance Job invocation.
func (r *Repository) ProcessTransferQueue(ctx context.Context) error {
	return r.ProcessTransferQueueParallel(ctx, 1)
}

func (r *Repository) OldestPendingTransfer(ctx context.Context) (time.Time, bool, error) {
	return r.metaStore.OldestPendingTransfer(ctx)
}

// ProcessTransferQueueParallel processes independent content IDs concurrently.
func (r *Repository) ProcessTransferQueueParallel(ctx context.Context, parallelism int) error {
	if parallelism <= 0 {
		return fmt.Errorf("transfer parallelism must be positive")
	}
	if r.settings.DeliveryMode != DeliveryModeRemote {
		return nil
	}
	if r.remoteStore == nil {
		return errors.New("remote delivery is configured without a remote object store")
	}
	ids, err := r.metaStore.PendingTransfers(ctx)
	if err != nil {
		return fmt.Errorf("list remote delivery obligations: %w", err)
	}
	var failures []error
	var failureMu sync.Mutex
	semaphore := make(chan struct{}, parallelism)
	var workers sync.WaitGroup
	canceled := false
	for _, id := range ids {
		select {
		case semaphore <- struct{}{}:
		case <-ctx.Done():
			canceled = true
		}
		if canceled {
			break
		}
		workers.Add(1)
		go func(id content.ID) {
			defer workers.Done()
			defer func() { <-semaphore }()
			if err := id.Validate(); err != nil {
				failureMu.Lock()
				failures = append(failures, fmt.Errorf("invalid transfer queue content ID %q: %w", id, err))
				failureMu.Unlock()
				return
			}
			err := r.withResourceLease(ctx, contentResourceID(id.String()), coordinator.RLock, func(transferCtx context.Context, operationID string) (retErr error) {
				if err := r.objectStore.Verify(transferCtx, id); err != nil {
					return fmt.Errorf("verify Local source %s before transfer: %w", id, err)
				}
				sizer, ok := r.objectStore.(interface {
					Size(context.Context, content.ID) (int64, error)
				})
				if !ok {
					return errors.New("object store does not report committed object size")
				}
				size, err := sizer.Size(transferCtx, id)
				if err != nil {
					return fmt.Errorf("read Local source size %s: %w", id, err)
				}
				expectedMD5, err := r.metaStore.ContentMD5(transferCtx, id)
				if err != nil {
					return fmt.Errorf("read Content ID integrity metadata %s: %w", id, err)
				}
				if len(expectedMD5) != 16 {
					return fmt.Errorf("missing MD5 metadata for Content ID %s", id)
				}
				reader, err := r.objectStore.Open(transferCtx, id)
				if err != nil {
					return fmt.Errorf("open Local source %s for transfer: %w", id, err)
				}
				info, uploadErr := r.remoteStore.Upload(transferCtx, id, size, expectedMD5, reader)
				closeErr := reader.Close()
				if uploadErr != nil || closeErr != nil {
					if uploadErr != nil {
						uploadErr = fmt.Errorf("upload content %s: %w", id, uploadErr)
					}
					return errors.Join(uploadErr, closeErr)
				}
				if info.ID != id || info.Size != size || info.ContentID != id.String() || len(info.MD5) != 16 || !bytes.Equal(info.MD5, expectedMD5) {
					return fmt.Errorf("remote upload returned mismatched API attributes for %s: %+v", id, info)
				}
				if err := r.validateLease(transferCtx, operationID); err != nil {
					return err
				}
				if err := r.metaStore.CompleteTransfer(transferCtx, id); err != nil {
					return fmt.Errorf("remove completed transfer obligation for %s: %w", id, err)
				}
				return nil
			})
			if err != nil {
				failureMu.Lock()
				failures = append(failures, fmt.Errorf("process transfer %s: %w", id, err))
				failureMu.Unlock()
			}
		}(id)
	}
	workers.Wait()
	if canceled {
		failures = append(failures, ctx.Err())
	}
	return errors.Join(failures...)
}
