package repository

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kamichidu/go-nando/internal/coordinator"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/objectstore"
	"github.com/kamichidu/go-nando/internal/paths"
)

const (
	resourceLeaseTTL       = 30 * time.Second
	resourceLeaseHeartbeat = 5 * time.Second
	putCleanupTimeout      = 5 * time.Second
)

// ErrPutCommitted indicates the Metadata Commit completed but post-commit
// staging cleanup or Resource Lease release failed. Retrying may duplicate work.
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
	if err := r.coordinator.AcquireResourceLock(ctx, temporaryResourceID(operationID), operationID, coordinator.RLock, resourceLeaseTTL); err != nil {
		return fmt.Errorf("acquire temp RLock before Stage: %w", err)
	}
	metadataCommitted := false
	defer func() {
		cleanupErr := r.finishPutResources(operationID)
		if cleanupErr != nil {
			if metadataCommitted {
				retErr = errors.Join(retErr, fmt.Errorf("%w: %w", ErrPutCommitted, cleanupErr))
			} else {
				retErr = errors.Join(retErr, fmt.Errorf("clean failed Put staging: %w", cleanupErr))
			}
		}
	}()
	leaseCtx, stopHeartbeat, err := r.startLeaseHeartbeat(ctx, operationID)
	if err != nil {
		return fmt.Errorf("start Resource Lease heartbeat: %w", err)
	}
	defer func() {
		heartbeatErr := stopHeartbeat()
		if heartbeatErr != nil {
			if metadataCommitted {
				retErr = errors.Join(retErr, fmt.Errorf("%w: %w", ErrPutCommitted, heartbeatErr))
			} else {
				retErr = errors.Join(retErr, heartbeatErr)
			}
		}
	}()
	session, err := r.objectStore.BeginStage(leaseCtx, operationID)
	if err != nil {
		return fmt.Errorf("begin Object Store Stage: %w", err)
	}
	sessionOwned := true
	defer func() {
		if sessionOwned {
			retErr = errors.Join(retErr, session.Close())
		}
	}()

	staged, err := r.stagePutInput(leaseCtx, session, file)
	sessionOwned = false
	if err != nil {
		return err
	}
	if err := r.coordinator.AcquireResourceLock(leaseCtx, contentResourceID(staged.ID().String()), operationID, coordinator.RLock, resourceLeaseTTL); err != nil {
		return fmt.Errorf("acquire content ID RLock after hash: %w", err)
	}

	contentType := mime.TypeByExtension(filepath.Ext(localPath))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	baseName := path.Base(normalized)
	now := metadata.TimestampFromTime(time.Now().UTC())

	err = func() error {
		// Parent resolution, Object publication, and Metadata mutation happen
		// while both resource RLocks remain active, without a long SQLite writer lock.
		parentID, err := r.resolveOrCreateParentDirectories(leaseCtx, normalized)
		if err != nil {
			return err
		}
		if err := r.validateLease(leaseCtx, operationID); err != nil {
			return err
		}
		res, err := r.objectStore.Publish(leaseCtx, staged)
		if err != nil {
			return fmt.Errorf("publish staged object: %w", err)
		}
		if err := res.ID.Validate(); err != nil {
			return fmt.Errorf("object store returned invalid content ID: %w", err)
		}
		if res.ID != staged.ID() || res.Size != staged.Size() {
			return fmt.Errorf("object store published unexpected staged content: got %s/%d, want %s/%d", res.ID, res.Size, staged.ID(), staged.Size())
		}

		if err := r.validateLease(leaseCtx, operationID); err != nil {
			return err
		}
		existing, err := r.metaStore.GetChild(leaseCtx, parentID, baseName)
		if err == nil {
			if existing.Kind == metadata.EntryKindDirectory {
				return fmt.Errorf("cannot replace directory %s with a file: %w", repoPath, metadata.ErrEntryConflict)
			}
			existing.ObjectID = &res.ID
			existing.Size = res.Size
			existing.Mtime = metadata.TimestampFromTime(localStat.ModTime())
			existing.Mode = uint32(localStat.Mode())
			existing.ContentType = contentType
			if _, err := r.metaStore.Update(leaseCtx, existing); err != nil {
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
			if _, err := r.metaStore.Create(leaseCtx, created); err != nil {
				return fmt.Errorf("failed to create entry metadata: %w", err)
			}
		} else {
			return fmt.Errorf("failed to check existing entry: %w", err)
		}
		metadataCommitted = true
		return nil
	}()
	return err
}

func (r *Repository) stagePutInput(ctx context.Context, session objectstore.StageSession, input *os.File) (objectstore.StagedObject, error) {
	staged, stageErr := session.Stage(ctx, input)
	return staged, errors.Join(stageErr, session.Close())
}

var errResourceLeaseLost = errors.New("Resource Lease expired or was invalidated")

func temporaryResourceID(operationID string) string { return "temp:" + operationID }
func contentResourceID(contentID string) string     { return "content:" + contentID }

func (r *Repository) validateLease(ctx context.Context, operationID string) error {
	active, err := r.coordinator.ValidateResourceLocks(ctx, operationID)
	if err != nil {
		return err
	}
	if !active {
		return fmt.Errorf("%w: %s", errResourceLeaseLost, operationID)
	}
	return ctx.Err()
}

func (r *Repository) withResourceLease(ctx context.Context, resourceID string, mode coordinator.LockMode, operation func(context.Context) error) (retErr error) {
	operationID := uuid.NewString()
	if err := r.coordinator.AcquireResourceLock(ctx, resourceID, operationID, mode, resourceLeaseTTL); err != nil {
		return err
	}
	leaseCtx, stopHeartbeat, err := r.startLeaseHeartbeat(ctx, operationID)
	if err != nil {
		return errors.Join(err, r.releaseResourceLocks(operationID))
	}
	defer func() {
		retErr = errors.Join(retErr, stopHeartbeat())
		releaseCtx, cancel := context.WithTimeout(context.Background(), putCleanupTimeout)
		defer cancel()
		retErr = errors.Join(retErr, r.coordinator.ReleaseResourceLocks(releaseCtx, operationID))
	}()
	if err := r.validateLease(leaseCtx, operationID); err != nil {
		return err
	}
	return operation(leaseCtx)
}

func (r *Repository) startLeaseHeartbeat(ctx context.Context, operationID string) (context.Context, func() error, error) {
	active, err := r.coordinator.RenewResourceLocks(ctx, operationID, resourceLeaseTTL)
	if err != nil {
		return nil, nil, err
	}
	if !active {
		return nil, nil, fmt.Errorf("%w: %s", errResourceLeaseLost, operationID)
	}
	operationCtx, cancel := context.WithCancel(ctx)
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(resourceLeaseHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-operationCtx.Done():
				done <- operationCtx.Err()
				return
			case <-stop:
				done <- nil
				return
			case <-ticker.C:
				ok, renewErr := r.coordinator.RenewResourceLocks(operationCtx, operationID, resourceLeaseTTL)
				if renewErr != nil {
					cancel()
					done <- fmt.Errorf("renew Resource Lease: %w", renewErr)
					return
				}
				if !ok {
					cancel()
					done <- fmt.Errorf("%w: %s", errResourceLeaseLost, operationID)
					return
				}
			}
		}
	}()
	var once sync.Once
	stopFn := func() error {
		once.Do(func() { close(stop) })
		err := <-done
		cancel()
		if errors.Is(err, context.Canceled) && ctx.Err() == nil {
			return nil
		}
		return err
	}
	return operationCtx, stopFn, nil
}

func (r *Repository) cleanupExpiredStaging(ctx context.Context) error {
	operationIDs, err := r.coordinator.InvalidateExpiredResourceLocks(ctx)
	if err != nil {
		return err
	}
	for _, operationID := range operationIDs {
		if err := r.objectStore.CleanupStaging(ctx, operationID); err != nil {
			return fmt.Errorf("cleanup staging for operation %s: %w", operationID, err)
		}
		if err := r.coordinator.ForgetExpiredResourceLocks(ctx, operationID); err != nil {
			return fmt.Errorf("forget cleaned Resource Leases for %s: %w", operationID, err)
		}
	}
	activeIDs, err := r.coordinator.ActiveResourceOperationIDs(ctx)
	if err != nil {
		return fmt.Errorf("list active Resource Lease operations: %w", err)
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
		if err := r.coordinator.InvalidateResourceLocks(ctx, operationID); err != nil {
			return fmt.Errorf("invalidate orphan Resource Leases for %s: %w", operationID, err)
		}
		if err := r.objectStore.CleanupStaging(ctx, operationID); err != nil {
			return fmt.Errorf("cleanup orphan Put staging %s: %w", operationID, err)
		}
		if err := r.coordinator.ForgetExpiredResourceLocks(ctx, operationID); err != nil {
			return fmt.Errorf("forget orphan Resource Leases for %s: %w", operationID, err)
		}
	}
	return nil
}

func (r *Repository) finishPutResources(operationID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), putCleanupTimeout)
	defer cancel()
	cleanupErr := r.objectStore.CleanupStaging(ctx, operationID)
	releaseErr := r.coordinator.ReleaseResourceLocks(ctx, operationID)
	return errors.Join(cleanupErr, releaseErr)
}

func (r *Repository) releaseResourceLocks(operationID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), putCleanupTimeout)
	defer cancel()
	return r.coordinator.ReleaseResourceLocks(ctx, operationID)
}
