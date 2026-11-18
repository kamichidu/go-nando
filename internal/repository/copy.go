package repository

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kamichidu/go-nando/internal/coordinator"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/paths"
)

type copySnapshot struct {
	entry    metadata.Entry
	children []copySnapshot
	shallow  bool
}

// CopyEntry clones a metadata subtree while sharing immutable content objects.
// Staging remains in a private registered buffer until one atomic publish.
func (r *Repository) CopyEntry(ctx context.Context, id metadata.EntryID, sourcePath string, revision uint64, destination, depth string, overwrite bool) (bool, error) {
	source, err := r.metaStore.GetByID(ctx, id)
	if err != nil {
		return false, err
	}
	if source.ID == "root" {
		return false, fmt.Errorf("cannot copy root: %w", metadata.ErrEntryConflict)
	}
	if source.Kind != metadata.EntryKindDirectory {
		return false, fmt.Errorf("collection COPY requires a collection: %w", metadata.ErrEntryConflict)
	}
	canonical, err := r.DeriveCanonicalPath(ctx, source)
	if err != nil {
		return false, err
	}
	if canonical != sourcePath || source.Revision != revision {
		return false, fmt.Errorf("COPY source changed: %w", metadata.ErrEntryConflict)
	}
	snapshot, err := r.captureCopySnapshot(ctx, source, depth)
	if err != nil {
		return false, err
	}
	return r.withCopySnapshotLeases(ctx, snapshot, func(ctx context.Context) (bool, error) {
		if depth == "" {
			depth = "infinity"
		}
		if depth != "0" && depth != "infinity" {
			return false, fmt.Errorf("invalid COPY depth: %w", metadata.ErrEntryConflict)
		}
		dst, err := paths.NormalizeLogicalPath(destination)
		if err != nil || dst == "/" {
			return false, fmt.Errorf("invalid COPY destination: %w", metadata.ErrEntryConflict)
		}
		if dst == canonical || strings.HasPrefix(dst, canonical+"/") || strings.HasPrefix(canonical, dst+"/") {
			return false, fmt.Errorf("COPY paths overlap: %w", metadata.ErrEntryConflict)
		}
		parent, err := r.ResolvePath(ctx, path.Dir(dst))
		if err != nil {
			return false, err
		}
		if parent.Kind != metadata.EntryKindDirectory {
			return false, fmt.Errorf("destination parent is not a collection: %w", metadata.ErrEntryConflict)
		}
		name := path.Base(dst)
		existing, err := r.metaStore.GetChild(ctx, parent.ID, name)
		exists := err == nil
		if err != nil && !errors.Is(err, metadata.ErrEntryNotFound) {
			return false, err
		}
		if exists && !overwrite {
			return false, fmt.Errorf("destination exists: %w", metadata.ErrEntryConflict)
		}
		now := metadata.TimestampFromTime(time.Now().UTC())
		tmp, err := r.bufferStore.CreateDetachedRoot(ctx, metadata.Entry{ID: metadata.EntryID(uuid.NewString()), Kind: metadata.EntryKindDirectory, Mtime: snapshot.entry.Mtime, Mode: snapshot.entry.Mode, Revision: 1, CreatedAt: now, UpdatedAt: now}, internalBufferLeaseTTL)
		if err != nil {
			return false, err
		}
		published := false
		return r.withBufferedCopyCleanup(tmp, &published, func() (bool, error) {
			leaseCtx, stopLease := r.renewDetachedBuffer(ctx, tmp)
			defer stopLease()
			if err := r.cloneDeadProperties(leaseCtx, tmp, snapshot.entry.ID, tmp.RootID); err != nil {
				return false, err
			}
			if depth == "infinity" {
				if err := r.copySnapshotChildren(leaseCtx, tmp, snapshot, metadata.Entry{ID: tmp.RootID}); err != nil {
					return false, err
				}
			}
			if err := r.verifyCopySnapshot(leaseCtx, snapshot); err != nil {
				return false, err
			}
			if _, err := r.bufferStore.RenewDetachedBuffer(leaseCtx, tmp.ID, tmp.Fencing, internalBufferLeaseTTL); err != nil {
				return false, err
			}
			currentParent, err := r.ResolvePath(leaseCtx, path.Dir(dst))
			if err != nil || currentParent.ID != parent.ID || currentParent.Kind != metadata.EntryKindDirectory {
				return false, fmt.Errorf("COPY destination parent changed: %w", metadata.ErrEntryConflict)
			}
			backupPath := ""
			if exists {
				backupPath = "/.nando-copy-old-" + uuid.NewString()
				if err := r.MoveEntry(leaseCtx, existing.ID, backupPath); err != nil {
					return false, err
				}
			}
			if _, err := r.bufferStore.PublishDetachedRoot(leaseCtx, tmp.ID, tmp.Fencing, parent.ID, name); err != nil {
				if backupPath != "" {
					return false, errors.Join(err, fmt.Errorf("displaced destination remains at %s", backupPath))
				}
				return false, err
			}
			published = true
			if exists {
				if err := r.removeTreeEntry(ctx, existing); err != nil {
					return true, fmt.Errorf("COPY committed; old destination cleanup failed at %s: %w", backupPath, err)
				}
				return true, nil
			}
			return false, nil
		})
	})
}

func (r *Repository) withBufferedCopyCleanup(buffer metadata.DetachedBuffer, published *bool, operation func() (bool, error)) (replaced bool, retErr error) {
	defer func() {
		if *published {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), putCleanupTimeout)
		defer cancel()
		if _, err := r.bufferStore.RenewDetachedBuffer(cleanupCtx, buffer.ID, buffer.Fencing, -time.Second); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("expire COPY lease for cleanup: %w", err))
			return
		}
		claimed, err := r.bufferStore.ClaimExpiredDetachedBuffer(cleanupCtx, buffer.ID, metadata.TimestampFromTime(time.Now()), internalBufferLeaseTTL)
		if err == nil {
			err = r.reclaimDetachedBuffer(cleanupCtx, claimed)
		}
		if err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("COPY staging remains in internal buffer %s for retry: %w", buffer.ID, err))
		}
	}()
	return operation()
}

func (r *Repository) withCopySnapshotLeases(ctx context.Context, snapshot copySnapshot, operation func(context.Context) (bool, error)) (bool, error) {
	ids := make(map[string]struct{})
	var collect func(copySnapshot)
	collect = func(node copySnapshot) {
		if node.entry.ObjectID != nil {
			ids[node.entry.ObjectID.String()] = struct{}{}
		}
		for _, child := range node.children {
			collect(child)
		}
	}
	collect(snapshot)
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	var replaced bool
	var acquire func(int, context.Context) error
	acquire = func(i int, lockCtx context.Context) error {
		if i == len(ordered) {
			var err error
			replaced, err = operation(lockCtx)
			return err
		}
		id := ordered[i]
		return r.withResourceLease(lockCtx, contentResourceID(id), coordinator.RLock, func(next context.Context, _ string) error { return acquire(i+1, next) })
	}
	err := acquire(0, ctx)
	return replaced, err
}

func (r *Repository) captureCopySnapshot(ctx context.Context, entry metadata.Entry, depth string) (copySnapshot, error) {
	snapshot := copySnapshot{entry: entry}
	if depth == "0" {
		snapshot.shallow = true
		return snapshot, nil
	}
	if entry.Kind != metadata.EntryKindDirectory {
		return snapshot, nil
	}
	children, err := r.metaStore.ListChildren(ctx, entry.ID)
	if err != nil {
		return copySnapshot{}, err
	}
	for _, child := range children {
		captured, err := r.captureCopySnapshot(ctx, child, "infinity")
		if err != nil {
			return copySnapshot{}, err
		}
		snapshot.children = append(snapshot.children, captured)
	}
	return snapshot, nil
}

func (r *Repository) copySnapshotChildren(ctx context.Context, buffer metadata.DetachedBuffer, snapshot copySnapshot, target metadata.Entry) error {
	for _, childSnapshot := range snapshot.children {
		child := childSnapshot.entry
		copyEntry := child
		copyEntry.ID = metadata.EntryID(uuid.NewString())
		copyEntry.ParentID = &target.ID
		copyEntry.Revision = 1
		copyEntry.CreatedAt = metadata.TimestampFromTime(time.Now().UTC())
		copyEntry.UpdatedAt = copyEntry.CreatedAt
		if _, err := r.bufferStore.RenewDetachedBuffer(ctx, buffer.ID, buffer.Fencing, internalBufferLeaseTTL); err != nil {
			return err
		}
		created, err := r.bufferStore.CreateDetachedEntry(ctx, buffer.ID, buffer.Fencing, copyEntry)
		if err != nil {
			return err
		}
		if err := r.cloneDeadProperties(ctx, buffer, child.ID, created.ID); err != nil {
			return err
		}
		if child.Kind == metadata.EntryKindDirectory {
			if err := r.copySnapshotChildren(ctx, buffer, childSnapshot, created); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Repository) cloneDeadProperties(ctx context.Context, buffer metadata.DetachedBuffer, source, target metadata.EntryID) error {
	properties, err := r.metaStore.GetDeadProperties(ctx, source)
	if err != nil {
		return err
	}
	for i := range properties {
		properties[i].EntryID = target
	}
	changes := make([]metadata.DeadPropertyChange, 0, len(properties))
	for _, p := range properties {
		changes = append(changes, metadata.DeadPropertyChange{Property: p})
	}
	if _, err := r.bufferStore.RenewDetachedBuffer(ctx, buffer.ID, buffer.Fencing, internalBufferLeaseTTL); err != nil {
		return err
	}
	return r.bufferStore.SetDetachedDeadProperties(ctx, buffer.ID, buffer.Fencing, target, changes)
}

// verifyCopySnapshot rechecks membership and revisions after staging, catching
// source edits during the copy before the destination is switched.
func (r *Repository) verifyCopySnapshot(ctx context.Context, snapshot copySnapshot) error {
	current, err := r.metaStore.GetByID(ctx, snapshot.entry.ID)
	if err != nil || current.Revision != snapshot.entry.Revision {
		return fmt.Errorf("COPY source changed: %w", metadata.ErrEntryConflict)
	}
	if snapshot.shallow {
		return nil
	}
	children, err := r.metaStore.ListChildren(ctx, snapshot.entry.ID)
	if err != nil {
		return err
	}
	if len(children) != len(snapshot.children) {
		return fmt.Errorf("COPY source changed: %w", metadata.ErrEntryConflict)
	}
	want := make(map[metadata.EntryID]copySnapshot, len(snapshot.children))
	for _, child := range snapshot.children {
		want[child.entry.ID] = child
	}
	for _, child := range children {
		expected, ok := want[child.ID]
		if !ok || child.Revision != expected.entry.Revision {
			return fmt.Errorf("COPY source changed: %w", metadata.ErrEntryConflict)
		}
		if err := r.verifyCopySnapshot(ctx, expected); err != nil {
			return err
		}
	}
	return nil
}
