package repository

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"sort"

	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/coordinator"
	"github.com/kamichidu/go-nando/internal/objectstore"
)

// CapacityGC evicts oldest confirmed Local cache objects until usage is within
// the configured limit. It never evicts in Local mode or an undelivered remote
// object, and rechecks eligibility while holding the resource WLock.
func (r *Repository) CapacityGC(ctx context.Context) (int, error) {
	if r.settings.DeliveryMode != DeliveryModeRemote || r.settings.MaxLocalBytes <= 0 {
		return 0, nil
	}
	if r.remoteStore == nil {
		return 0, errors.New("capacity GC requires a remote object store")
	}
	lister, ok := r.objectStore.(interface {
		ListObjectInfo(context.Context) ([]objectstore.LocalObjectInfo, error)
	})
	if !ok {
		return 0, errors.New("object store does not support Local capacity accounting")
	}
	objects, err := lister.ListObjectInfo(ctx)
	if err != nil {
		return 0, err
	}
	var used int64
	for _, item := range objects {
		used += item.Size
	}
	if used <= r.settings.MaxLocalBytes {
		return 0, nil
	}
	sort.Slice(objects, func(i, j int) bool {
		if objects[i].ModTime.Equal(objects[j].ModTime) {
			return objects[i].ID.String() < objects[j].ID.String()
		}
		return objects[i].ModTime.Before(objects[j].ModTime)
	})
	deleted := 0
	var failures []error
	for _, candidate := range objects {
		if used <= r.settings.MaxLocalBytes {
			break
		}
		removed, err := r.evictCapacityCandidate(ctx, candidate.ID)
		if err != nil {
			failures = append(failures, fmt.Errorf("capacity GC %s: %w", candidate.ID, err))
			continue
		}
		if removed {
			deleted++
			used -= candidate.Size
		}
	}
	return deleted, errors.Join(failures...)
}

func (r *Repository) evictCapacityCandidate(ctx context.Context, id content.ID) (bool, error) {
	removed := false
	err := r.withResourceLease(ctx, contentResourceID(id.String()), coordinator.WLock, func(leaseCtx context.Context, operationID string) error {
		refs, err := r.metaStore.ReferencedObjectIDs(leaseCtx)
		if err != nil {
			return fmt.Errorf("recheck Metadata references: %w", err)
		}
		referenced := false
		for _, ref := range refs {
			if ref == id {
				referenced = true
				break
			}
		}
		if !referenced {
			return nil // Unreferenced objects belong to the independent Orphan GC task.
		}
		pending, err := r.metaStore.PendingTransfers(leaseCtx)
		if err != nil {
			return fmt.Errorf("recheck transfer queue: %w", err)
		}
		for _, queued := range pending {
			if queued == id {
				return nil
			}
		}
		expectedMD5, err := r.metaStore.ContentMD5(leaseCtx, id)
		if err != nil || len(expectedMD5) != md5.Size {
			return nil
		}
		remote, err := r.remoteStore.RemoteAttrs(leaseCtx, id)
		if err != nil {
			return fmt.Errorf("read remote object attributes for capacity candidate: %w", err)
		}
		if len(remote.MD5) != md5.Size || !bytes.Equal(remote.MD5, expectedMD5) {
			return nil
		}
		if err := r.validateLease(leaseCtx, operationID); err != nil {
			return err
		}
		if err := r.objectStore.Delete(leaseCtx, id); err != nil {
			return fmt.Errorf("delete confirmed Local cache: %w", err)
		}
		removed = true
		return nil
	})
	return removed, err
}
