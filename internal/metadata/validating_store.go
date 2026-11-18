package metadata

import (
	"context"
	"fmt"
	"time"

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

func (s *ValidatingStore) CreateDetachedRoot(ctx context.Context, entry Entry, ttl time.Duration) (DetachedBuffer, error) {
	bufferStore, ok := s.next.(InternalBufferStore)
	if !ok {
		return DetachedBuffer{}, fmt.Errorf("metadata store does not support internal buffers")
	}
	return bufferStore.CreateDetachedRoot(ctx, entry, ttl)
}
func (s *ValidatingStore) RenewDetachedBuffer(ctx context.Context, id BufferID, fence uint64, ttl time.Duration) (DetachedBuffer, error) {
	bufferStore, ok := s.next.(InternalBufferStore)
	if !ok {
		return DetachedBuffer{}, fmt.Errorf("metadata store does not support internal buffers")
	}
	return bufferStore.RenewDetachedBuffer(ctx, id, fence, ttl)
}
func (s *ValidatingStore) ClaimExpiredDetachedBuffer(ctx context.Context, id BufferID, now Timestamp) (DetachedBuffer, error) {
	bufferStore, ok := s.next.(InternalBufferStore)
	if !ok {
		return DetachedBuffer{}, fmt.Errorf("metadata store does not support internal buffers")
	}
	return bufferStore.ClaimExpiredDetachedBuffer(ctx, id, now)
}
func (s *ValidatingStore) ExpireDetachedBuffersAfterRestart(ctx context.Context, now Timestamp) error {
	bufferStore, ok := s.next.(InternalBufferStore)
	if !ok {
		return fmt.Errorf("metadata store does not support internal buffers")
	}
	return bufferStore.ExpireDetachedBuffersAfterRestart(ctx, now)
}
func (s *ValidatingStore) CreateDetachedEntry(ctx context.Context, buffer BufferID, fence uint64, entry Entry) (Entry, error) {
	bufferStore, ok := s.next.(InternalBufferStore)
	if !ok {
		return Entry{}, fmt.Errorf("metadata store does not support internal buffers")
	}
	return bufferStore.CreateDetachedEntry(ctx, buffer, fence, entry)
}
func (s *ValidatingStore) SetDetachedDeadProperties(ctx context.Context, buffer BufferID, fence uint64, id EntryID, changes []DeadPropertyChange) error {
	bufferStore, ok := s.next.(InternalBufferStore)
	if !ok {
		return fmt.Errorf("metadata store does not support internal buffers")
	}
	return bufferStore.SetDetachedDeadProperties(ctx, buffer, fence, id, changes)
}
func (s *ValidatingStore) PublishDetachedRoot(ctx context.Context, buffer BufferID, fence uint64, parent EntryID, name string) (Entry, error) {
	bufferStore, ok := s.next.(InternalBufferStore)
	if !ok {
		return Entry{}, fmt.Errorf("metadata store does not support internal buffers")
	}
	return bufferStore.PublishDetachedRoot(ctx, buffer, fence, parent, name)
}

func (s *ValidatingStore) DetachTree(ctx context.Context, id EntryID, revision uint64, expectedParent EntryID) (DetachedBuffer, error) {
	bufferStore, ok := s.next.(InternalBufferStore)
	if !ok {
		return DetachedBuffer{}, fmt.Errorf("metadata store does not support internal buffers")
	}
	return bufferStore.DetachTree(ctx, id, revision, expectedParent)
}
func (s *ValidatingStore) DetachedBuffers(ctx context.Context) ([]DetachedBuffer, error) {
	bufferStore, ok := s.next.(InternalBufferStore)
	if !ok {
		return nil, fmt.Errorf("metadata store does not support internal buffers")
	}
	return bufferStore.DetachedBuffers(ctx)
}
func (s *ValidatingStore) ListDetachedChildren(ctx context.Context, buffer BufferID, fence uint64, parent EntryID) ([]Entry, error) {
	bufferStore, ok := s.next.(InternalBufferStore)
	if !ok {
		return nil, fmt.Errorf("metadata store does not support internal buffers")
	}
	return bufferStore.ListDetachedChildren(ctx, buffer, fence, parent)
}
func (s *ValidatingStore) DeleteDetachedEntry(ctx context.Context, buffer BufferID, fence uint64, id EntryID) error {
	bufferStore, ok := s.next.(InternalBufferStore)
	if !ok {
		return fmt.Errorf("metadata store does not support internal buffers")
	}
	return bufferStore.DeleteDetachedEntry(ctx, buffer, fence, id)
}

func (s *ValidatingStore) GetDeadProperties(ctx context.Context, id EntryID) ([]DeadProperty, error) {
	return s.next.GetDeadProperties(ctx, id)
}
func (s *ValidatingStore) UpdateDeadProperties(ctx context.Context, id EntryID, changes []DeadPropertyChange) error {
	return s.next.UpdateDeadProperties(ctx, id, changes)
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
	if err := mustHaveValidRevision(entry); err != nil {
		return Entry{}, err
	}

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

// CommitPut validates a Put Entry and delegates its atomic persistence.
func (s *ValidatingStore) CommitPut(ctx context.Context, entry Entry, enqueueTransfer bool, requireExisting bool) (Entry, error) {
	if err := runEntryValidators(entry, mustHaveEntryIDInEntry, mustNotBeRootInEntry, mustHaveValidKind, mustHaveValidRevision, mustHaveValidParentID, mustHaveNonEmptyName, mustHaveValidObjectIDForKind); err != nil {
		return Entry{}, err
	}
	if entry.Kind != EntryKindFile {
		return Entry{}, fmt.Errorf("%w: Put commit requires a file Entry", ErrEntryConflict)
	}
	return s.next.CommitPut(ctx, entry, enqueueTransfer, requireExisting)
}

// PendingTransfers delegates transfer queue listing.
func (s *ValidatingStore) PendingTransfers(ctx context.Context) ([]content.ID, error) {
	return s.next.PendingTransfers(ctx)
}

func (s *ValidatingStore) OldestPendingTransfer(ctx context.Context) (time.Time, bool, error) {
	return s.next.OldestPendingTransfer(ctx)
}

// CompleteTransfer delegates transfer queue completion.
func (s *ValidatingStore) CompleteTransfer(ctx context.Context, id content.ID) error {
	if err := id.Validate(); err != nil {
		return err
	}
	return s.next.CompleteTransfer(ctx, id)
}

// ReferencedObjectIDs delegates to the next store.
func (s *ValidatingStore) ReferencedObjectIDs(ctx context.Context) ([]content.ID, error) {
	return s.next.ReferencedObjectIDs(ctx)
}

// ContentMD5 delegates immutable content integrity metadata lookup.
func (s *ValidatingStore) ContentMD5(ctx context.Context, id content.ID) ([]byte, error) {
	return s.next.ContentMD5(ctx, id)
}

// ListAll delegates to the next store.
func (s *ValidatingStore) ListAll(ctx context.Context) ([]Entry, error) {
	return s.next.ListAll(ctx)
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
		if err := entry.ObjectID.Validate(); err != nil {
			return fmt.Errorf("%w: invalid object ID: %w", ErrEntryConflict, err)
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
