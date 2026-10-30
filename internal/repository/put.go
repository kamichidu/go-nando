package repository

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/objectstore"
	"github.com/kamichidu/go-nando/internal/paths"
)

const (
	putLeaseTTL       = 30 * time.Second
	putLeaseRenewal   = 10 * time.Second
	putCleanupTimeout = 5 * time.Second
)

// ErrPutCommitted indicates the Metadata Commit completed but post-commit Lease
// or staging cleanup failed. Callers must not assume retrying is side-effect free.
var ErrPutCommitted = errors.New("Put metadata committed but staging cleanup failed")

// Put imports a local file into the repository under the specified logical repository path.
func (r *Repository) Put(ctx context.Context, localPath string, repoPath string) (retErr error) {
	normalized, err := paths.NormalizeLogicalPath(repoPath)
	if err != nil {
		return fmt.Errorf("invalid logical destination path: %w", err)
	}
	if normalized == "/" {
		return fmt.Errorf("cannot put a file directly over the root directory")
	}

	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open local file: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("close local input: %w", closeErr))
		}
	}()
	localStat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat local file: %w", err)
	}
	if localStat.IsDir() {
		return fmt.Errorf("local path %q is a directory, directories cannot be put", localPath)
	}

	if err := r.cleanupExpiredStaging(ctx); err != nil {
		return fmt.Errorf("clean expired Put staging: %w", err)
	}
	operationID := uuid.NewString()
	session, err := r.objectStore.BeginStage(ctx, operationID)
	if err != nil {
		return fmt.Errorf("begin Object Store Stage: %w", err)
	}
	sessionOwned := true
	defer func() {
		if sessionOwned {
			retErr = errors.Join(retErr, session.Close())
		}
	}()
	if err := r.coordinator.AcquireLease(ctx, operationID, putLeaseTTL); err != nil {
		cleanupErr := r.objectStore.CleanupStaging(context.Background(), operationID)
		closeErr := session.Close()
		sessionOwned = false
		return errors.Join(fmt.Errorf("acquire Put staging Lease: %w", err), closeErr, cleanupErr)
	}
	metadataCommitted := false
	defer func() {
		cleanupErr := r.finishPutLease(operationID)
		if cleanupErr != nil {
			if metadataCommitted {
				retErr = errors.Join(retErr, fmt.Errorf("%w: %w", ErrPutCommitted, cleanupErr))
			} else {
				retErr = errors.Join(retErr, fmt.Errorf("clean failed Put staging: %w", cleanupErr))
			}
		}
	}()

	staged, err := r.stagePutInput(ctx, operationID, session, file)
	sessionOwned = false
	if err != nil {
		return err
	}

	contentType := mime.TypeByExtension(filepath.Ext(localPath))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	baseName := path.Base(normalized)
	now := metadata.TimestampFromTime(time.Now().UTC())

	err = r.coordinator.ExclusiveWithLease(ctx, operationID, func() error {
		// Parent resolution and every persistent or externally visible mutation
		// belong to the exclusive Commit half of Put.
		parentID, err := r.resolveOrCreateParentDirectories(ctx, normalized)
		if err != nil {
			return err
		}
		res, err := r.objectStore.Publish(ctx, staged)
		if err != nil {
			return fmt.Errorf("publish staged object: %w", err)
		}
		if err := res.ID.Validate(); err != nil {
			return fmt.Errorf("object store returned invalid content ID: %w", err)
		}
		if res.ID != staged.ID() || res.Size != staged.Size() {
			return fmt.Errorf("object store published unexpected staged content: got %s/%d, want %s/%d", res.ID, res.Size, staged.ID(), staged.Size())
		}

		existing, err := r.metaStore.GetChild(ctx, parentID, baseName)
		if err == nil {
			if existing.Kind == metadata.EntryKindDirectory {
				return fmt.Errorf("cannot replace directory %s with a file: %w", repoPath, metadata.ErrEntryConflict)
			}
			existing.ObjectID = &res.ID
			existing.Size = res.Size
			existing.Mtime = metadata.TimestampFromTime(localStat.ModTime())
			existing.Mode = uint32(localStat.Mode())
			existing.ContentType = contentType
			if _, err := r.metaStore.Update(ctx, existing); err != nil {
				return fmt.Errorf("failed to update entry metadata: %w", err)
			}
		} else if errors.Is(err, metadata.ErrEntryNotFound) {
			created := metadata.Entry{
				ID:          metadata.EntryID(uuid.New().String()),
				ParentID:    &parentID,
				Name:        baseName,
				Kind:        metadata.EntryKindFile,
				ObjectID:    &res.ID,
				Size:        res.Size,
				Mtime:       metadata.TimestampFromTime(localStat.ModTime()),
				Mode:        uint32(localStat.Mode()),
				ContentType: contentType,
				Revision:    1,
				CreatedAt:   now,
				UpdatedAt:   now,
			}
			if _, err := r.metaStore.Create(ctx, created); err != nil {
				return fmt.Errorf("failed to create entry metadata: %w", err)
			}
		} else {
			return fmt.Errorf("failed to check existing entry: %w", err)
		}
		metadataCommitted = true
		return nil
	})
	return err
}

func (r *Repository) stagePutInput(ctx context.Context, operationID string, session objectstore.StageSession, input *os.File) (objectstore.StagedObject, error) {
	stageCtx, cancelStage := context.WithCancel(ctx)
	defer cancelStage()

	stopRenewal := make(chan struct{})
	renewalDone := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(putLeaseRenewal)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				cancelStage()
				renewalDone <- ctx.Err()
				return
			case <-stopRenewal:
				renewalDone <- nil
				return
			case <-ticker.C:
				active, err := r.coordinator.RenewLease(ctx, operationID, putLeaseTTL)
				if err != nil {
					cancelStage()
					renewalDone <- fmt.Errorf("renew Put staging Lease: %w", err)
					return
				}
				if !active {
					cancelStage()
					renewalDone <- fmt.Errorf("%w: %s", errPutLeaseLost, operationID)
					return
				}
			}
		}
	}()

	active, renewErr := r.coordinator.RenewLease(ctx, operationID, putLeaseTTL)
	if renewErr != nil || !active {
		close(stopRenewal)
		renewerErr := <-renewalDone
		closeErr := session.Close()
		if renewErr != nil {
			return nil, errors.Join(fmt.Errorf("validate Put Lease before Stage: %w", renewErr), renewerErr, closeErr)
		}
		return nil, errors.Join(fmt.Errorf("%w: %s", errPutLeaseLost, operationID), renewerErr, closeErr)
	}

	staged, stageErr := session.Stage(stageCtx, input)
	close(stopRenewal)
	renewerErr := <-renewalDone
	closeErr := session.Close()
	if stageErr != nil || renewerErr != nil || closeErr != nil {
		return nil, errors.Join(stageErr, renewerErr, closeErr)
	}
	return staged, nil
}

var errPutLeaseLost = errors.New("Put staging Lease expired or was invalidated")

func (r *Repository) cleanupExpiredStaging(ctx context.Context) error {
	operationIDs, err := r.coordinator.InvalidateExpiredLeases(ctx)
	if err != nil {
		return err
	}
	for _, operationID := range operationIDs {
		if err := r.objectStore.CleanupStaging(ctx, operationID); err != nil {
			return fmt.Errorf("cleanup staging for operation %s: %w", operationID, err)
		}
		if err := r.coordinator.ForgetExpiredLease(ctx, operationID); err != nil {
			return fmt.Errorf("forget cleaned Put Lease %s: %w", operationID, err)
		}
	}
	activeIDs, err := r.coordinator.ActiveLeaseIDs(ctx)
	if err != nil {
		return fmt.Errorf("list active Put Leases: %w", err)
	}
	active := make(map[string]struct{}, len(activeIDs))
	for _, operationID := range activeIDs {
		active[operationID] = struct{}{}
	}
	stagingIDs, err := r.objectStore.ListStaging(ctx)
	if err != nil {
		return fmt.Errorf("list Put staging: %w", err)
	}
	for _, operationID := range stagingIDs {
		if _, ok := active[operationID]; ok {
			continue
		}
		if err := r.coordinator.Exclusive(ctx, func() error {
			return r.coordinator.InvalidateLease(ctx, operationID)
		}); err != nil {
			return fmt.Errorf("invalidate orphan Put Lease %s: %w", operationID, err)
		}
		if err := r.objectStore.CleanupStaging(ctx, operationID); err != nil {
			return fmt.Errorf("cleanup orphan Put staging %s: %w", operationID, err)
		}
		if err := r.coordinator.ForgetExpiredLease(ctx, operationID); err != nil {
			return fmt.Errorf("forget orphan Put Lease %s: %w", operationID, err)
		}
	}
	return nil
}

func (r *Repository) finishPutLease(operationID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), putCleanupTimeout)
	defer cancel()
	if err := r.coordinator.Exclusive(ctx, func() error {
		return r.coordinator.InvalidateLease(ctx, operationID)
	}); err != nil {
		return err
	}
	if err := r.objectStore.CleanupStaging(ctx, operationID); err != nil {
		return err
	}
	return r.coordinator.ForgetExpiredLease(ctx, operationID)
}
