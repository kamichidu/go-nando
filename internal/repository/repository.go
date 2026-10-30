package repository

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/objectstore"
	"github.com/kamichidu/go-nando/internal/paths"
)

// Repository coordinates the interactions between MetadataStore and ObjectStore.
type Repository struct {
	metaStore   metadata.MetadataStore
	objectStore objectstore.ObjectStore
	hasher      content.Hasher
	coordinator Coordinator
}

// NewRepository creates a Repository whose protected operations use the
// repository-wide coordinator shared by every process and instance.
func NewRepository(metaStore metadata.MetadataStore, objStore objectstore.ObjectStore, hasher content.Hasher, coordinator Coordinator) *Repository {
	return &Repository{
		metaStore:   metaStore,
		objectStore: objStore,
		hasher:      hasher,
		coordinator: coordinator,
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
	return r.coordinator.Exclusive(ctx, func() error {
		return r.cat(ctx, repoPath, w)
	})
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

	rc, err := r.objectStore.Open(ctx, *entry.ObjectID)
	if err != nil {
		return fmt.Errorf("failed to open object from store: %w", err)
	}
	defer rc.Close()

	if _, err := io.Copy(w, rc); err != nil {
		return fmt.Errorf("failed to stream object data: %w", err)
	}
	return nil
}

// GC deletes unreferenced objects from the object store.
func (r *Repository) GC(ctx context.Context) (int, error) {
	if err := r.cleanupExpiredStaging(ctx); err != nil {
		return 0, fmt.Errorf("clean expired Put staging before GC: %w", err)
	}
	deletedCount := 0
	err := r.coordinator.Exclusive(ctx, func() error {
		var err error
		deletedCount, err = r.gc(ctx)
		return err
	})
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
			if err := r.objectStore.Delete(ctx, id); err != nil {
				return deletedCount, fmt.Errorf("failed to delete unreferenced object %s: %w", id.String(), err)
			}
			deletedCount++
		}
	}

	return deletedCount, nil
}

// Doctor verifies repository integrity (checking database reference matches physical object and is intact).
func (r *Repository) Doctor(ctx context.Context) error {
	return r.coordinator.Exclusive(ctx, func() error {
		return r.doctor(ctx)
	})
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
			exists, err := r.objectStore.Exists(ctx, id)
			if err != nil {
				return fmt.Errorf("doctor failed: failed to check object existence for ID %s: %w", id.String(), err)
			}
			if !exists {
				return fmt.Errorf("doctor failed: referenced object %s is missing from object store", id.String())
			}

			if err := r.objectStore.Verify(ctx, id); err != nil {
				return fmt.Errorf("doctor failed: verification failed for referenced object %s: %w", id.String(), err)
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
