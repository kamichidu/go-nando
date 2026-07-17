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
	if err := runEntryValidators(entry,
		mustHaveEntryIDInEntry,
		mustNotBeRootInEntry,
		mustHaveValidKind,
		mustHaveValidRevision,
		mustHaveValidParentID,
		mustHaveNonEmptyName,
		mustHaveValidObjectIDForKind,
	); err != nil {
		return Entry{}, err
	}

	// State-dependent validation: Parent existence and parent kind validation
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
	if err := runEntryValidators(entry,
		mustHaveEntryIDInEntry,
		mustNotBeRootInEntry,
		mustHaveValidKind,
		mustHaveValidRevision,
		mustHaveValidParentID,
		mustHaveNonEmptyName,
		mustHaveValidObjectIDForKind,
	); err != nil {
		return Entry{}, err
	}

	return s.next.Update(ctx, entry)
}

// Move validates the move operation before delegating to the next store.
func (s *ValidatingStore) Move(ctx context.Context, entry Entry) (Entry, error) {
	if entry.ParentID == nil {
		return Entry{}, fmt.Errorf("%w: parent ID cannot be empty", ErrEntryConflict)
	}

	if err := runMoveValidators(moveParams{ID: entry.ID, NewParentID: *entry.ParentID, NewName: entry.Name},
		mustHaveMoveEntryID,
		mustNotMoveRoot,
		mustHaveMoveDestination,
		mustHaveMoveName,
		mustNotMoveToItself,
	); err != nil {
		return Entry{}, err
	}

	return s.next.Move(ctx, entry)
}

// Delete validates the deletion before delegating to the next store.
func (s *ValidatingStore) Delete(ctx context.Context, id EntryID) error {
	if err := runEntryIDValidators(id,
		mustHaveEntryID,
		mustNotBeRootID,
	); err != nil {
		return err
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

// EntryID Validator definitions and helpers.

type entryIDValidator func(EntryID) error

func runEntryIDValidators(id EntryID, validators ...entryIDValidator) error {
	for _, validate := range validators {
		if err := validate(id); err != nil {
			return err
		}
	}
	return nil
}

func mustHaveEntryID(id EntryID) error {
	if id == "" {
		return fmt.Errorf("%w: entry ID cannot be empty", ErrEntryConflict)
	}
	return nil
}

func mustNotBeRootID(id EntryID) error {
	if id == "root" {
		return fmt.Errorf("%w: operation on root entry is forbidden", ErrEntryConflict)
	}
	return nil
}

// Entry Validator definitions and helpers

type entryValidator func(Entry) error

func runEntryValidators(entry Entry, validators ...entryValidator) error {
	for _, validate := range validators {
		if err := validate(entry); err != nil {
			return err
		}
	}
	return nil
}

func mustHaveEntryIDInEntry(entry Entry) error {
	return mustHaveEntryID(entry.ID)
}

func mustNotBeRootInEntry(entry Entry) error {
	return mustNotBeRootID(entry.ID)
}

func mustHaveValidKind(entry Entry) error {
	if entry.Kind != EntryKindFile && entry.Kind != EntryKindDirectory {
		return fmt.Errorf("%w: invalid entry kind %q", ErrEntryConflict, entry.Kind)
	}
	return nil
}

func mustHaveValidRevision(entry Entry) error {
	if entry.Revision < 1 {
		return fmt.Errorf("%w: invalid revision %d (must be >= 1)", ErrEntryConflict, entry.Revision)
	}
	return nil
}

func mustHaveValidParentID(entry Entry) error {
	if entry.ParentID == nil {
		return fmt.Errorf("%w: non-root entry must have a parent ID", ErrEntryConflict)
	}
	if *entry.ParentID == "" {
		return fmt.Errorf("%w: parent ID cannot be empty", ErrEntryConflict)
	}
	return nil
}

func mustHaveNonEmptyName(entry Entry) error {
	if entry.Name == "" {
		return fmt.Errorf("%w: non-root entry must have a non-empty name", ErrEntryConflict)
	}
	return nil
}

func mustHaveValidObjectIDForKind(entry Entry) error {
	switch entry.Kind {
	case EntryKindFile:
		if entry.ObjectID == nil {
			return fmt.Errorf("%w: file entry must have an associated object ID", ErrEntryConflict)
		}
	case EntryKindDirectory:
		if entry.ObjectID != nil {
			return fmt.Errorf("%w: directory entry must not have an associated object ID", ErrEntryConflict)
		}
	}
	return nil
}

// Move Validator definitions and helpers

type moveParams struct {
	ID          EntryID
	NewParentID EntryID
	NewName     string
}

type moveValidator func(moveParams) error

func runMoveValidators(params moveParams, validators ...moveValidator) error {
	for _, validate := range validators {
		if err := validate(params); err != nil {
			return err
		}
	}
	return nil
}

func mustHaveMoveEntryID(params moveParams) error {
	return mustHaveEntryID(params.ID)
}

func mustNotMoveRoot(params moveParams) error {
	return mustNotBeRootID(params.ID)
}

func mustHaveMoveDestination(params moveParams) error {
	if params.NewParentID == "" {
		return fmt.Errorf("%w: parent ID cannot be empty", ErrEntryConflict)
	}
	return nil
}

func mustHaveMoveName(params moveParams) error {
	if params.NewName == "" {
		return fmt.Errorf("%w: local name cannot be empty", ErrEntryConflict)
	}
	return nil
}

func mustNotMoveToItself(params moveParams) error {
	if params.ID == params.NewParentID {
		return fmt.Errorf("%w: entry cannot be moved to itself", ErrEntryConflict)
	}
	return nil
}
