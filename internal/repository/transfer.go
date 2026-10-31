package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/coordinator"
)

// ProcessTransferQueue attempts every currently pending content ID once. Failed
// rows remain durable for the next Maintenance Job invocation.
func (r *Repository) ProcessTransferQueue(ctx context.Context) error {
	if r.settings.DeliveryMode != DeliveryModeGCS {
		return nil
	}
	if r.replica == nil {
		return errors.New("GCS delivery is configured without a replica store")
	}
	ids, err := r.metaStore.PendingTransfers(ctx)
	if err != nil {
		return fmt.Errorf("list GCS transfer obligations: %w", err)
	}
	var failures []error
	for _, id := range ids {
		if err := id.Validate(); err != nil {
			failures = append(failures, fmt.Errorf("invalid transfer queue content ID %q: %w", id, err))
			continue
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
			reader, err := r.objectStore.Open(transferCtx, id)
			if err != nil {
				return fmt.Errorf("open Local source %s for transfer: %w", id, err)
			}
			info, uploadErr := r.replica.Upload(transferCtx, id, size, reader)
			closeErr := reader.Close()
			if uploadErr != nil || closeErr != nil {
				if uploadErr != nil {
					uploadErr = fmt.Errorf("upload content %s: %w", id, uploadErr)
				}
				return errors.Join(uploadErr, closeErr)
			}
			if info.ID != id || info.Size != size || info.Generation <= 0 {
				return fmt.Errorf("GCS upload returned mismatched API attributes for %s: %+v", id, info)
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
			failures = append(failures, fmt.Errorf("process transfer %s: %w", id, err))
		}
	}
	return errors.Join(failures...)
}
