package metadata

import (
	"context"
	"errors"
	"fmt"

	"github.com/kamichidu/go-nando/internal/content"
)

// ValidatingStore wraps a MetadataStore and enforces validation constraints.
type ValidatingStore struct {
	next MetadataStore
}

// NewValidatingStore wraps the given MetadataStore with input validation.
func NewValidatingStore(next MetadataStore) *ValidatingStore {
	return &ValidatingStore{
		next: next,
	}
}

// Ensure ValidatingStore implements MetadataStore
var _ MetadataStore = (*ValidatingStore)(nil)

// Initialize delegates to the next store.
func (s *ValidatingStore) Initialize(ctx context.Context) error {
	return s.next.Initialize(ctx)
}

// Root delegates to the next store.
func (s *ValidatingStore) Root(ctx context.Context) (Entry, error) {
	return s.next.Root(ctx)
}

// GetByID delegates to the next store.
func (s *ValidatingStore) GetByID(ctx context.Context, id EntryID) (Entry, error) {
	return s.next.GetByID(ctx, id)
}

// GetChild delegates to the next store.
func (s *ValidatingStore) GetChild(ctx context.Context, parentID EntryID, name string) (Entry, error) {
	return s.next.GetChild(ctx, parentID, name)
}

// ListChildren delegates to the next store.
func (s *ValidatingStore) ListChildren(ctx context.Context, parentID EntryID) ([]Entry, error) {
	return s.next.ListChildren(ctx, parentID)
}

// Create validates the entry before inserting it.
func (s *ValidatingStore) Create(ctx context.Context, entry Entry) (Entry, error) {
	if entry.ID == "" {
		return Entry{}, fmt.Errorf("%w: entry ID cannot be empty", ErrEntryConflict)
	}

	// Forbid creating the root entry via normal Create
	if entry.ID == "root" {
		return Entry{}, fmt.Errorf("%w: operation on root entry is forbidden", ErrEntryConflict)
	}

	// 1. Kind validation: Kind must be "file" or "directory"
	if entry.Kind != EntryKindFile && entry.Kind != EntryKindDirectory {
		return Entry{}, fmt.Errorf("%w: invalid entry kind %q", ErrEntryConflict, entry.Kind)
	}

	// 2. Revision validation: Revision >= 1
	if entry.Revision < 1 {
		return Entry{}, fmt.Errorf("%w: invalid revision %d (must be >= 1)", ErrEntryConflict, entry.Revision)
	}

	// 3. ParentID and Name validation
	if entry.ParentID == nil {
		return Entry{}, fmt.Errorf("%w: non-root entry must have a parent ID", ErrEntryConflict)
	}
	if *entry.ParentID == "" {
		return Entry{}, fmt.Errorf("%w: parent ID cannot be empty", ErrEntryConflict)
	}
	if entry.Name == "" {
		return Entry{}, fmt.Errorf("%w: non-root entry must have a non-empty name", ErrEntryConflict)
	}

	// 4. File-specific & Directory-specific ObjectID validation
	if entry.Kind == EntryKindFile {
		if entry.ObjectID == nil {
			return Entry{}, fmt.Errorf("%w: file entry must have an associated object ID", ErrEntryConflict)
		}
	} else {
		if entry.ObjectID != nil {
			return Entry{}, fmt.Errorf("%w: directory entry must not have an associated object ID", ErrEntryConflict)
		}
	}

	// 5. Parent existence and parent kind validation
	parent, err := s.next.GetByID(ctx, *entry.ParentID)
	if err != nil {
		if errors.Is(err, ErrEntryNotFound) {
			return Entry{}, fmt.Errorf("%w: parent entry %s does not exist", ErrEntryConflict, *entry.ParentID)
		}
		return Entry{}, fmt.Errorf("failed to verify parent existence: %w", err)
	}
	if parent.Kind != EntryKindDirectory {
		return Entry{}, fmt.Errorf("%w: parent entry %s must be a directory, but got %s", ErrEntryConflict, *entry.ParentID, parent.Kind)
	}

	return s.next.Create(ctx, entry)
}

// Update validates the entry before delegating to the next store.
func (s *ValidatingStore) Update(ctx context.Context, entry Entry) (Entry, error) {
	if entry.ID == "" {
		return Entry{}, fmt.Errorf("%w: entry ID cannot be empty", ErrEntryConflict)
	}

	// Forbid modifying the root entry via normal Update
	if entry.ID == "root" {
		return Entry{}, fmt.Errorf("%w: operation on root entry is forbidden", ErrEntryConflict)
	}

	// 1. Kind validation: Kind must be "file" or "directory"
	if entry.Kind != EntryKindFile && entry.Kind != EntryKindDirectory {
		return Entry{}, fmt.Errorf("%w: invalid entry kind %q", ErrEntryConflict, entry.Kind)
	}

	// 2. Revision validation: Revision >= 1
	if entry.Revision < 1 {
		return Entry{}, fmt.Errorf("%w: invalid revision %d (must be >= 1)", ErrEntryConflict, entry.Revision)
	}

	// 3. ParentID and Name validation
	if entry.ParentID == nil {
		return Entry{}, fmt.Errorf("%w: non-root entry must have a parent ID", ErrEntryConflict)
	}
	if *entry.ParentID == "" {
		return Entry{}, fmt.Errorf("%w: parent ID cannot be empty", ErrEntryConflict)
	}
	if entry.Name == "" {
		return Entry{}, fmt.Errorf("%w: non-root entry must have a non-empty name", ErrEntryConflict)
	}

	// 4. File-specific & Directory-specific ObjectID validation
	if entry.Kind == EntryKindFile {
		if entry.ObjectID == nil {
			return Entry{}, fmt.Errorf("%w: file entry must have an associated object ID", ErrEntryConflict)
		}
	} else {
		if entry.ObjectID != nil {
			return Entry{}, fmt.Errorf("%w: directory entry must not have an associated object ID", ErrEntryConflict)
		}
	}

	return s.next.Update(ctx, entry)
}

// Move validates the move operation before delegating to the next store.
func (s *ValidatingStore) Move(ctx context.Context, id EntryID, newParentID EntryID, newName string) (Entry, error) {
	if id == "" {
		return Entry{}, fmt.Errorf("%w: entry ID cannot be empty", ErrEntryConflict)
	}
	if id == "root" {
		return Entry{}, fmt.Errorf("%w: operation on root entry is forbidden", ErrEntryConflict)
	}
	if newParentID == "" {
		return Entry{}, fmt.Errorf("%w: parent ID cannot be empty", ErrEntryConflict)
	}
	if newName == "" {
		return Entry{}, fmt.Errorf("%w: local name cannot be empty", ErrEntryConflict)
	}

	if id == newParentID {
		return Entry{}, fmt.Errorf("%w: entry cannot be moved to itself", ErrEntryConflict)
	}

	entry, err := s.next.GetByID(ctx, id)
	if err != nil {
		return Entry{}, err
	}

	newParent, err := s.next.GetByID(ctx, newParentID)
	if err != nil {
		if errors.Is(err, ErrEntryNotFound) {
			return Entry{}, fmt.Errorf("%w: destination parent entry %s does not exist", ErrEntryConflict, newParentID)
		}
		return Entry{}, fmt.Errorf("failed to verify destination parent existence: %w", err)
	}
	if newParent.Kind != EntryKindDirectory {
		return Entry{}, fmt.Errorf("%w: destination parent %s must be a directory", ErrEntryConflict, newParentID)
	}

	if entry.Kind == EntryKindDirectory {
		current := newParentID
		visited := make(map[EntryID]bool)
		for current != "root" {
			if current == id {
				return Entry{}, fmt.Errorf("%w: directory cannot be moved under its own descendant (hierarchy cycle)", ErrEntryConflict)
			}
			if visited[current] {
				return Entry{}, fmt.Errorf("%w: cycle detected in parent ancestry during move", ErrEntryConflict)
			}
			visited[current] = true

			p, err := s.next.GetByID(ctx, current)
			if err != nil {
				return Entry{}, fmt.Errorf("failed to verify ancestry during move: %w", err)
			}
			if p.ParentID == nil {
				break
			}
			current = *p.ParentID
		}
	}

	return s.next.Move(ctx, id, newParentID, newName)
}

// Delete validates the deletion before delegating to the next store.
func (s *ValidatingStore) Delete(ctx context.Context, id EntryID) error {
	if id == "" {
		return fmt.Errorf("%w: entry ID cannot be empty", ErrEntryConflict)
	}
	if id == "root" {
		return fmt.Errorf("%w: operation on root entry is forbidden", ErrEntryConflict)
	}
	return s.next.Delete(ctx, id)
}

// ReferencedObjectIDs delegates to the next store.
func (s *ValidatingStore) ReferencedObjectIDs(ctx context.Context) ([]content.ID, error) {
	return s.next.ReferencedObjectIDs(ctx)
}

// ListAll delegates to the next store.
func (s *ValidatingStore) ListAll(ctx context.Context) ([]Entry, error) {
	return s.next.ListAll(ctx)
}

// CheckSchemaCompatibility delegates to the next store.
func (s *ValidatingStore) CheckSchemaCompatibility(ctx context.Context) error {
	return s.next.CheckSchemaCompatibility(ctx)
}
