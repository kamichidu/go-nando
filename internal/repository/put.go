package repository

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kamichidu/go-nando/internal/content"
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
// PutExpectation captures the target Entry state observed when a daemon write
// Resource session begins. MustBeAbsent represents an observed missing path.
type PutExpectation struct {
	MustBeAbsent bool
	EntryID      metadata.EntryID
	Revision     uint64
}

// Put imports a complete file and applies the current path-based create or
// replace behavior.
func (r *Repository) Put(ctx context.Context, localPath string, repoPath string) error {
	return r.put(ctx, localPath, repoPath, nil)
}

// PutExpected imports a complete file only if the target still has the state
// observed when the caller opened its write Resource session.
func (r *Repository) PutExpected(ctx context.Context, localPath, repoPath string, expected PutExpectation) error {
	if expected.MustBeAbsent {
		if expected.EntryID != "" || expected.Revision != 0 {
			return fmt.Errorf("invalid absent-entry expectation: %w", metadata.ErrEntryConflict)
		}
	} else if expected.EntryID == "" || expected.Revision == 0 {
		return fmt.Errorf("existing-entry expectation requires ID and revision: %w", metadata.ErrEntryConflict)
	}
	return r.put(ctx, localPath, repoPath, &expected)
}

func (r *Repository) put(ctx context.Context, localPath string, repoPath string, expected *PutExpectation) (retErr error) {
	if err := r.settings.Validate(); err != nil {
		return fmt.Errorf("invalid Repository settings: %w", err)
	}
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
		var putEntry metadata.Entry
		createdEntry := false
		if err == nil {
			if expected != nil && (expected.MustBeAbsent || existing.ID != expected.EntryID || existing.Revision != expected.Revision) {
				return fmt.Errorf("Put target changed since Resource session began: %w", metadata.ErrEntryConflict)
			}
			if existing.Kind == metadata.EntryKindDirectory {
				return fmt.Errorf("cannot replace directory %s with a file: %w", repoPath, metadata.ErrEntryConflict)
			}
			existing.ObjectID = &res.ID
			existing.Size = res.Size
			existing.Mtime = metadata.TimestampFromTime(localStat.ModTime())
			existing.Mode = uint32(localStat.Mode())
			existing.ContentType = contentType
			putEntry = existing
		} else if errors.Is(err, metadata.ErrEntryNotFound) {
			if expected != nil && !expected.MustBeAbsent {
				return fmt.Errorf("Put target disappeared since Resource session began: %w", metadata.ErrEntryConflict)
			}
			createdEntry = true
			putEntry = metadata.Entry{
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
		} else {
			return fmt.Errorf("failed to check existing entry: %w", err)
		}
		if err := r.validateLease(leaseCtx, operationID); err != nil {
			return err
		}
		if r.settings.DeliveryMode == DeliveryModeGCS {
			if _, err := r.metaStore.CommitPut(leaseCtx, putEntry, true); err != nil {
				return fmt.Errorf("failed to atomically commit GCS-mode Put metadata and transfer obligation: %w", err)
			}
		} else if createdEntry {
			if _, err := r.metaStore.Create(leaseCtx, putEntry); err != nil {
				return fmt.Errorf("failed to create entry metadata: %w", err)
			}
		} else if _, err := r.metaStore.Update(leaseCtx, putEntry); err != nil {
			return fmt.Errorf("failed to update entry metadata: %w", err)
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

var errResourceLeaseLost = errors.New("resource lease expired or was invalidated")

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

func (r *Repository) openLocalOrDownload(ctx context.Context, operationID string, id content.ID) (reader io.ReadCloser, retErr error) {
	reader, err := r.objectStore.Open(ctx, id)
	if err == nil || !errors.Is(err, objectstore.ErrObjectNotFound) || r.settings.DeliveryMode != DeliveryModeGCS {
		return reader, err
	}
	if r.replica == nil {
		return nil, errors.New("GCS read-through is configured without a replica store")
	}
	if err := r.coordinator.AcquireResourceLock(ctx, temporaryResourceID(operationID), operationID, coordinator.RLock, resourceLeaseTTL); err != nil {
		return nil, fmt.Errorf("acquire download temp RLock: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), putCleanupTimeout)
		defer cancel()
		if err := r.objectStore.CleanupStaging(cleanupCtx, operationID); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("cleanup GCS download staging: %w", err))
		}
	}()
	session, err := r.objectStore.BeginStage(ctx, operationID)
	if err != nil {
		return nil, fmt.Errorf("begin GCS download staging: %w", err)
	}
	sessionClosed := false
	defer func() {
		if !sessionClosed {
			retErr = errors.Join(retErr, session.Close())
		}
	}()
	remote, err := r.replica.OpenReplica(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("open GCS replica %s: %w", id, err)
	}
	staged, stageErr := session.Stage(ctx, remote)
	remoteCloseErr := remote.Close()
	sessionCloseErr := session.Close()
	sessionClosed = true
	if err := errors.Join(stageErr, remoteCloseErr, sessionCloseErr); err != nil {
		return nil, fmt.Errorf("stage GCS download %s: %w", id, err)
	}
	if staged.ID() != id {
		return nil, fmt.Errorf("downloaded GCS content has the wrong identity: got %s, want %s: %w", staged.ID(), id, objectstore.ErrHashMismatch)
	}
	if err := r.validateLease(ctx, operationID); err != nil {
		return nil, err
	}
	result, err := r.objectStore.Publish(ctx, staged)
	if err != nil {
		return nil, fmt.Errorf("publish verified GCS download: %w", err)
	}
	if result.ID != id || result.Size != staged.Size() {
		return nil, fmt.Errorf("object store published unexpected GCS download: got %s/%d want %s/%d", result.ID, result.Size, id, staged.Size())
	}
	if err := r.validateLease(ctx, operationID); err != nil {
		return nil, err
	}
	return r.objectStore.Open(ctx, id)
}

func (r *Repository) withResourceLease(ctx context.Context, resourceID string, mode coordinator.LockMode, operation func(context.Context, string) error) (retErr error) {
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
	return operation(leaseCtx, operationID)
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
