package repository

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/coordinator"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/objectstore"
	"github.com/kamichidu/go-nando/internal/paths"
)

// Repository coordinates the interactions between MetadataStore and ObjectStore.
type Repository struct {
	metaStore   metadata.MetadataStore
	objectStore objectstore.ObjectStore
	replica     objectstore.ReplicaStore
	hasher      content.Hasher
	coordinator Coordinator
	settings    Settings
}

// NewRepository creates a Repository whose protected operations use the
// repository-wide coordinator shared by every process and instance.
func NewRepository(metaStore metadata.MetadataStore, objStore objectstore.ObjectStore, hasher content.Hasher, coordinator Coordinator) *Repository {
	return NewRepositoryWithSettings(metaStore, objStore, hasher, coordinator, DefaultSettings())
}

// NewRepositoryWithSettings creates a Repository with persisted delivery settings.
func NewRepositoryWithSettings(metaStore metadata.MetadataStore, objStore objectstore.ObjectStore, hasher content.Hasher, coordinator Coordinator, settings Settings) *Repository {
	return NewRepositoryWithReplica(metaStore, objStore, nil, hasher, coordinator, settings)
}

// NewRepositoryWithReplica installs the remote replica used by GCS delivery.
func NewRepositoryWithReplica(metaStore metadata.MetadataStore, objStore objectstore.ObjectStore, replica objectstore.ReplicaStore, hasher content.Hasher, coordinator Coordinator, settings Settings) *Repository {
	return &Repository{
		metaStore:   metaStore,
		objectStore: objStore,
		replica:     replica,
		hasher:      hasher,
		coordinator: coordinator,
		settings:    settings,
	}
}

// ResolvePath traverses the parent-child relationships in MetadataStore to find the entry at repoPath.
func (r *Repository) ResolvePath(ctx context.Context, repoPath string) (metadata.Entry, error) {
	normalized, err := paths.NormalizeLogicalPath(repoPath)
	if err != nil {
		return metadata.Entry{}, err
	}

	root, err := r.metaStore.Root(ctx)
	if err != nil {
		return metadata.Entry{}, err
	}

	if normalized == "/" {
		return root, nil
	}

	parts := strings.Split(strings.TrimPrefix(normalized, "/"), "/")
	current := root
	for _, part := range parts {
		if part == "" {
			continue
		}
		child, err := r.metaStore.GetChild(ctx, current.ID, part)
		if err != nil {
			return metadata.Entry{}, err
		}
		current = child
	}

	return current, nil
}

// DeriveCanonicalPath reconstructs the logical path of an entry by walking up its ancestry.
func (r *Repository) DeriveCanonicalPath(ctx context.Context, entry metadata.Entry) (string, error) {
	if entry.ID == "root" {
		return "/", nil
	}

	var parts []string
	current := entry
	for current.ID != "root" {
		parts = append([]string{current.Name}, parts...)
		if current.ParentID == nil {
			return "", fmt.Errorf("non-root entry has no parent ID: %s", current.ID)
		}
		parent, err := r.metaStore.GetByID(ctx, *current.ParentID)
		if err != nil {
			return "", fmt.Errorf("failed to get parent entry %s: %w", *current.ParentID, err)
		}
		current = parent
	}

	return "/" + strings.Join(parts, "/"), nil
}

// resolveOrCreateParentDirectories takes a normalized logical path,
// traverses the ancestry from root, and implicitly creates any missing parent directories.
// It returns the ID of the parent directory of the final component, or an error.
func (r *Repository) resolveOrCreateParentDirectories(ctx context.Context, repoPath string) (metadata.EntryID, error) {
	normalized, err := paths.NormalizeLogicalPath(repoPath)
	if err != nil {
		return "", err
	}

	parentPath := path.Dir(normalized)
	if parentPath == "/" {
		root, err := r.metaStore.Root(ctx)
		if err != nil {
			return "", err
		}
		return root.ID, nil
	}

	parts := strings.Split(strings.TrimPrefix(parentPath, "/"), "/")
	root, err := r.metaStore.Root(ctx)
	if err != nil {
		return "", err
	}

	current := root
	now := metadata.TimestampFromTime(time.Now().UTC())
	for _, part := range parts {
		if part == "" {
			continue
		}
		child, err := r.metaStore.GetChild(ctx, current.ID, part)
		if err != nil {
			if errors.Is(err, metadata.ErrEntryNotFound) {
				// Create missing directory entry
				dirID := metadata.EntryID(uuid.New().String())
				newDir := metadata.Entry{
					ID:          dirID,
					ParentID:    &current.ID,
					Name:        part,
					Kind:        metadata.EntryKindDirectory,
					ObjectID:    nil,
					Size:        0,
					Mtime:       now,
					Mode:        0755,
					ContentType: "",
					Revision:    1,
					CreatedAt:   now,
					UpdatedAt:   now,
				}
				created, err := r.metaStore.Create(ctx, newDir)
				if err != nil {
					return "", fmt.Errorf("failed to create directory component %q: %w", part, err)
				}
				current = created
			} else {
				return "", fmt.Errorf("failed to traverse path component %q: %w", part, err)
			}
		} else {
			// Exists. Verify it is a directory.
			if child.Kind != metadata.EntryKindDirectory {
				return "", fmt.Errorf("path component %q is a file, not a directory: %w", part, metadata.ErrEntryConflict)
			}
			current = child
		}
	}

	return current.ID, nil
}

// List returns immediate children of the logical path.
func (r *Repository) List(ctx context.Context, repoPath string) ([]metadata.Entry, error) {
	entry, err := r.ResolvePath(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	return r.metaStore.ListChildren(ctx, entry.ID)
}

// Stat returns metadata details of the given logical path.
func (r *Repository) Stat(ctx context.Context, repoPath string) (metadata.Entry, error) {
	return r.ResolvePath(ctx, repoPath)
}

// Cat streams content of the given logical path to the specified writer.
func (r *Repository) Cat(ctx context.Context, repoPath string, w io.Writer) error {
	return r.cat(ctx, repoPath, w)
}

func (r *Repository) cat(ctx context.Context, repoPath string, w io.Writer) error {
	entry, err := r.ResolvePath(ctx, repoPath)
	if err != nil {
		return err
	}

	if entry.Kind == metadata.EntryKindDirectory {
		return fmt.Errorf("repository path %q is a directory, directories cannot be catted", repoPath)
	}

	if entry.ObjectID == nil {
		return fmt.Errorf("file entry %s has no associated object ID", entry.ID)
	}

	return r.withResourceLease(ctx, contentResourceID(entry.ObjectID.String()), coordinator.RLock, func(readCtx context.Context, operationID string) error {
		rc, err := r.openLocalOrDownload(readCtx, operationID, *entry.ObjectID)
		if err != nil {
			return fmt.Errorf("failed to open object from store: %w", err)
		}
		defer rc.Close()
		if _, err := io.Copy(w, rc); err != nil {
			return fmt.Errorf("failed to stream object data: %w", err)
		}
		return nil
	})
}

// GC deletes unreferenced objects from the object store.
func (r *Repository) GC(ctx context.Context) (int, error) {
	if err := r.cleanupExpiredStaging(ctx); err != nil {
		return 0, fmt.Errorf("clean expired Put staging before GC: %w", err)
	}
	deletedCount, err := r.gc(ctx)
	return deletedCount, err
}

func (r *Repository) gc(ctx context.Context) (int, error) {
	refIDs, err := r.metaStore.ReferencedObjectIDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to list referenced object IDs: %w", err)
	}

	refSet := make(map[string]bool)
	for _, id := range refIDs {
		if err := id.Validate(); err != nil {
			return 0, fmt.Errorf("metadata store returned invalid referenced object ID: %w", err)
		}
		refSet[id.String()] = true
	}

	allIDs, err := r.objectStore.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to list object IDs: %w", err)
	}

	deletedCount := 0
	for _, id := range allIDs {
		if err := id.Validate(); err != nil {
			return deletedCount, fmt.Errorf("object store returned invalid object ID: %w", err)
		}
		if !refSet[id.String()] {
			opID := uuid.NewString()
			if err := r.coordinator.AcquireResourceLock(ctx, contentResourceID(id.String()), opID, coordinator.WLock, resourceLeaseTTL); err != nil {
				return deletedCount, fmt.Errorf("acquire GC WLock for %s: %w", id, err)
			}
			deleted, err := r.deleteIfUnreferenced(ctx, opID, id)
			if deleted {
				deletedCount++
			}
			if err != nil {
				return deletedCount, err
			}
		}
	}

	if lister, ok := r.objectStore.(interface {
		ListRaw(context.Context) ([]string, error)
	}); ok {
		deleted, err := r.gcInvalidLocalObjects(ctx, lister)
		deletedCount += deleted
		if err != nil {
			return deletedCount, err
		}
	}

	if r.settings.DeliveryMode == DeliveryModeGCS {
		deleted, err := r.gcGCSOrphans(ctx, refSet)
		deletedCount += deleted
		if err != nil {
			return deletedCount, err
		}
	}

	return deletedCount, nil
}

func (r *Repository) gcInvalidLocalObjects(ctx context.Context, lister interface {
	ListRaw(context.Context) ([]string, error)
}) (int, error) {
	deleter, ok := r.objectStore.(interface {
		DeleteRaw(context.Context, string) error
	})
	if !ok {
		return 0, nil
	}
	names, err := lister.ListRaw(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to list raw Local objects: %w", err)
	}
	deleted := 0
	for _, name := range names {
		if _, err := content.ParseID(name); err == nil {
			continue
		}
		removed, err := r.deleteInvalidLocalObject(ctx, deleter, name)
		if removed {
			deleted++
		}
		if err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

func (r *Repository) deleteInvalidLocalObject(ctx context.Context, deleter interface {
	DeleteRaw(context.Context, string) error
}, name string) (bool, error) {
	removed := false
	err := r.withResourceLease(ctx, "raw:"+name, coordinator.WLock, func(leaseCtx context.Context, operationID string) error {
		if err := r.validateLease(leaseCtx, operationID); err != nil {
			return err
		}
		if err := deleter.DeleteRaw(leaseCtx, name); err != nil {
			if errors.Is(err, objectstore.ErrObjectNotFound) || errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("delete invalid Local object %q: %w", name, err)
		}
		removed = true
		return nil
	})
	return removed, err
}

func (r *Repository) gcGCSOrphans(ctx context.Context, refSet map[string]bool) (int, error) {
	if r.replica == nil {
		return 0, errors.New("GCS delivery is configured without a replica store")
	}
	replicas, err := r.replica.ListReplicas(ctx)
	if err != nil {
		return 0, fmt.Errorf("list GCS replicas for orphan GC: %w", err)
	}
	deleted := 0
	for _, replica := range replicas {
		id := replica.ID
		if err := id.Validate(); err != nil {
			return deleted, fmt.Errorf("GCS returned invalid replica ID: %w", err)
		}
		if replica.Generation <= 0 || refSet[id.String()] {
			continue
		}
		removed := false
		err := r.withResourceLease(ctx, contentResourceID(id.String()), coordinator.WLock, func(leaseCtx context.Context, operationID string) error {
			refs, err := r.metaStore.ReferencedObjectIDs(leaseCtx)
			if err != nil {
				return fmt.Errorf("recheck Metadata references before GCS orphan deletion: %w", err)
			}
			for _, ref := range refs {
				if ref == id {
					return nil
				}
			}
			pending, err := r.metaStore.PendingTransfers(leaseCtx)
			if err != nil {
				return fmt.Errorf("recheck transfer queue before GCS orphan deletion: %w", err)
			}
			for _, queued := range pending {
				if queued == id {
					return nil
				}
			}
			if err := r.validateLease(leaseCtx, operationID); err != nil {
				return err
			}
			if err := r.replica.DeleteReplica(leaseCtx, id, replica.Generation); err != nil {
				if errors.Is(err, objectstore.ErrObjectNotFound) {
					return nil
				}
				return fmt.Errorf("delete unreferenced GCS replica %s: %w", id, err)
			}
			removed = true
			return nil
		})
		if removed {
			deleted++
		}
		if err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

func (r *Repository) deleteIfUnreferenced(ctx context.Context, operationID string, id content.ID) (deleted bool, retErr error) {
	defer func() {
		retErr = errors.Join(retErr, r.releaseResourceLocks(operationID))
	}()
	leaseCtx, stopHeartbeat, err := r.startLeaseHeartbeat(ctx, operationID)
	if err != nil {
		return false, err
	}
	defer func() { retErr = errors.Join(retErr, stopHeartbeat()) }()
	refs, err := r.metaStore.ReferencedObjectIDs(leaseCtx)
	if err != nil {
		return false, fmt.Errorf("recheck Metadata references under GC WLock: %w", err)
	}
	for _, ref := range refs {
		if ref == id {
			return false, nil
		}
	}
	pendingTransfers, err := r.metaStore.PendingTransfers(leaseCtx)
	if err != nil {
		return false, fmt.Errorf("recheck pending transfer obligations under GC WLock: %w", err)
	}
	for _, pendingID := range pendingTransfers {
		if pendingID == id {
			return false, nil
		}
	}
	if err := r.validateLease(leaseCtx, operationID); err != nil {
		return false, err
	}
	if err := r.objectStore.Delete(leaseCtx, id); err != nil {
		return false, fmt.Errorf("failed to delete unreferenced object %s: %w", id, err)
	}
	return true, nil
}

// Doctor verifies repository integrity (checking database reference matches physical object and is intact).
func (r *Repository) Doctor(ctx context.Context) error {
	return r.doctor(ctx)
}

func (r *Repository) doctor(ctx context.Context) error {
	// 1. Check root entry validity
	root, err := r.metaStore.Root(ctx)
	if err != nil {
		return fmt.Errorf("doctor failed: root entry is invalid or missing: %w", err)
	}
	if root.ID != "root" {
		return fmt.Errorf("doctor failed: root entry ID must be 'root', got %q", root.ID)
	}
	if root.ParentID != nil {
		return fmt.Errorf("doctor failed: root entry parent ID must be NULL, got %q", *root.ParentID)
	}
	if root.Kind != metadata.EntryKindDirectory {
		return fmt.Errorf("doctor failed: root entry kind must be 'directory', got %q", root.Kind)
	}

	// 2. Load all entries from the Metadata Store
	allEntries, err := r.metaStore.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("doctor failed: failed to list all entries: %w", err)
	}

	entryMap := make(map[metadata.EntryID]metadata.Entry)
	for _, e := range allEntries {
		if e.Kind != metadata.EntryKindFile && e.Kind != metadata.EntryKindDirectory {
			return fmt.Errorf("doctor failed: entry %s has invalid kind %q", e.ID, e.Kind)
		}

		if e.ID == "" {
			return fmt.Errorf("doctor failed: found entry with empty ID")
		}

		if e.Kind == metadata.EntryKindFile {
			if e.ObjectID == nil {
				return fmt.Errorf("doctor failed: file entry %s has no object ID", e.ID)
			}
		} else {
			if e.ObjectID != nil {
				return fmt.Errorf("doctor failed: directory entry %s has an object ID: %s", e.ID, e.ObjectID.String())
			}
		}

		if e.Revision == 0 {
			return fmt.Errorf("doctor failed: entry %s has invalid revision 0", e.ID)
		}

		entryMap[e.ID] = e
	}

	// 3. Parent existence, kind, uniqueness, cycle checks
	seenParentsAndNames := make(map[string]bool)
	refIDs := make(map[string]bool)
	for _, e := range allEntries {
		if e.Kind == metadata.EntryKindFile && e.ObjectID != nil {
			refIDs[e.ObjectID.String()] = true
		}

		if e.ID == "root" {
			continue
		}

		if e.ParentID == nil {
			return fmt.Errorf("doctor failed: non-root entry %s has no parent ID", e.ID)
		}
		parent, ok := entryMap[*e.ParentID]
		if !ok {
			return fmt.Errorf("doctor failed: entry %s references non-existent parent %s", e.ID, *e.ParentID)
		}

		if parent.Kind != metadata.EntryKindDirectory {
			return fmt.Errorf("doctor failed: parent %s of entry %s is not a directory (kind: %q)", parent.ID, e.ID, parent.Kind)
		}

		key := fmt.Sprintf("%s/%s", *e.ParentID, e.Name)
		if seenParentsAndNames[key] {
			return fmt.Errorf("doctor failed: duplicate entry name %q under parent %s", e.Name, *e.ParentID)
		}
		seenParentsAndNames[key] = true

		current := e
		visited := make(map[metadata.EntryID]bool)
		for current.ID != "root" {
			if visited[current.ID] {
				return fmt.Errorf("doctor failed: hierarchy cycle detected at entry %s", current.ID)
			}
			visited[current.ID] = true
			if current.ParentID == nil {
				return fmt.Errorf("doctor failed: non-root entry %s has no parent ID in ancestry", current.ID)
			}
			p, ok := entryMap[*current.ParentID]
			if !ok {
				return fmt.Errorf("doctor failed: non-existent parent in cycle check")
			}
			current = p
		}
	}

	// 4. Check referenced object existence and verification
	for _, e := range allEntries {
		if e.Kind == metadata.EntryKindFile && e.ObjectID != nil {
			id := *e.ObjectID
			err := r.withResourceLease(ctx, contentResourceID(id.String()), coordinator.RLock, func(readCtx context.Context, _ string) error {
				exists, err := r.objectStore.Exists(readCtx, id)
				if err != nil {
					return fmt.Errorf("doctor failed: failed to check object existence for ID %s: %w", id.String(), err)
				}
				if !exists {
					return fmt.Errorf("doctor failed: referenced object %s is missing from object store", id.String())
				}

				if err := r.objectStore.Verify(readCtx, id); err != nil {
					return fmt.Errorf("doctor failed: verification failed for referenced object %s: %w", id.String(), err)
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
	}

	// 5. Check for invalid files in the object namespace
	rawNames, err := r.objectStore.ListRaw(ctx)
	if err != nil {
		return fmt.Errorf("doctor failed: failed to list raw object files: %w", err)
	}

	for _, name := range rawNames {
		_, err := content.ParseID(name)
		if err != nil {
			return fmt.Errorf("doctor failed: invalid filename %q found in object namespace: %w", name, err)
		}
	}

	return nil
}
