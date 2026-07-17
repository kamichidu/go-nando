package metadata_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/metadata"
)

type fakeMetadataStore struct {
	metadata.MetadataStore
	createCalled bool
	getByIDFunc  func(ctx context.Context, id metadata.EntryID) (metadata.Entry, error)
	createFunc   func(ctx context.Context, entry metadata.Entry) (metadata.Entry, error)
}

func (f *fakeMetadataStore) GetByID(ctx context.Context, id metadata.EntryID) (metadata.Entry, error) {
	if f.getByIDFunc != nil {
		return f.getByIDFunc(ctx, id)
	}
	// Default mock behavior: "root" is a valid parent directory
	if id == "root" {
		return metadata.Entry{
			ID:   "root",
			Kind: metadata.EntryKindDirectory,
		}, nil
	}
	return metadata.Entry{}, metadata.ErrEntryNotFound
}

func (f *fakeMetadataStore) Create(ctx context.Context, entry metadata.Entry) (metadata.Entry, error) {
	f.createCalled = true
	if f.createFunc != nil {
		return f.createFunc(ctx, entry)
	}
	return entry, nil
}

func TestValidatingStore(t *testing.T) {
	ctx := context.Background()
	dummyObjectID, err := content.ParseID("1234567890abcdef1234567890abcdef12345678")
	if err != nil {
		t.Fatalf("failed to parse dummy object ID: %v", err)
	}

	parentRoot := metadata.EntryID("root")

	t.Run("Create normal file successfully", func(t *testing.T) {
		fake := &fakeMetadataStore{}
		store := metadata.NewValidatingStore(fake)

		entry := metadata.Entry{
			ID:       "file-1",
			ParentID: &parentRoot,
			Name:     "doc.txt",
			Kind:     metadata.EntryKindFile,
			ObjectID: &dummyObjectID,
			Revision: 1,
		}

		res, err := store.Create(ctx, entry)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !fake.createCalled {
			t.Errorf("expected next.Create to be called")
		}
		if res.ID != entry.ID {
			t.Errorf("expected returned entry ID %s, got %s", entry.ID, res.ID)
		}
	})

	t.Run("Create normal directory successfully", func(t *testing.T) {
		fake := &fakeMetadataStore{}
		store := metadata.NewValidatingStore(fake)

		entry := metadata.Entry{
			ID:       "dir-1",
			ParentID: &parentRoot,
			Name:     "documents",
			Kind:     metadata.EntryKindDirectory,
			ObjectID: nil,
			Revision: 1,
		}

		res, err := store.Create(ctx, entry)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !fake.createCalled {
			t.Errorf("expected next.Create to be called")
		}
		if res.ID != entry.ID {
			t.Errorf("expected returned entry ID %s, got %s", entry.ID, res.ID)
		}
	})

	t.Run("Reject empty EntryID", func(t *testing.T) {
		fake := &fakeMetadataStore{}
		store := metadata.NewValidatingStore(fake)

		entry := metadata.Entry{
			ID:       "",
			ParentID: &parentRoot,
			Name:     "doc.txt",
			Kind:     metadata.EntryKindFile,
			ObjectID: &dummyObjectID,
			Revision: 1,
		}

		_, err := store.Create(ctx, entry)
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.createCalled {
			t.Errorf("next.Create should not be called on validation failure")
		}
	})

	t.Run("Reject root creation via Create", func(t *testing.T) {
		fake := &fakeMetadataStore{}
		store := metadata.NewValidatingStore(fake)

		entry := metadata.Entry{
			ID:       "root",
			ParentID: nil,
			Name:     "",
			Kind:     metadata.EntryKindDirectory,
			Revision: 1,
		}

		_, err := store.Create(ctx, entry)
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.createCalled {
			t.Errorf("next.Create should not be called")
		}
	})

	t.Run("Reject missing ParentID", func(t *testing.T) {
		fake := &fakeMetadataStore{}
		store := metadata.NewValidatingStore(fake)

		entry := metadata.Entry{
			ID:       "file-1",
			ParentID: nil,
			Name:     "doc.txt",
			Kind:     metadata.EntryKindFile,
			ObjectID: &dummyObjectID,
			Revision: 1,
		}

		_, err := store.Create(ctx, entry)
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.createCalled {
			t.Errorf("next.Create should not be called on validation failure")
		}
	})

	t.Run("Reject empty parent ID", func(t *testing.T) {
		fake := &fakeMetadataStore{}
		store := metadata.NewValidatingStore(fake)

		emptyParent := metadata.EntryID("")
		entry := metadata.Entry{
			ID:       "file-1",
			ParentID: &emptyParent,
			Name:     "doc.txt",
			Kind:     metadata.EntryKindFile,
			ObjectID: &dummyObjectID,
			Revision: 1,
		}

		_, err := store.Create(ctx, entry)
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.createCalled {
			t.Errorf("next.Create should not be called")
		}
	})

	t.Run("Reject empty Name", func(t *testing.T) {
		fake := &fakeMetadataStore{}
		store := metadata.NewValidatingStore(fake)

		entry := metadata.Entry{
			ID:       "file-1",
			ParentID: &parentRoot,
			Name:     "",
			Kind:     metadata.EntryKindFile,
			ObjectID: &dummyObjectID,
			Revision: 1,
		}

		_, err := store.Create(ctx, entry)
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.createCalled {
			t.Errorf("next.Create should not be called")
		}
	})

	t.Run("Reject unknown Kind", func(t *testing.T) {
		fake := &fakeMetadataStore{}
		store := metadata.NewValidatingStore(fake)

		entry := metadata.Entry{
			ID:       "file-1",
			ParentID: &parentRoot,
			Name:     "unknown",
			Kind:     metadata.EntryKind("invalid_kind"),
			ObjectID: &dummyObjectID,
			Revision: 1,
		}

		_, err := store.Create(ctx, entry)
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.createCalled {
			t.Errorf("next.Create should not be called")
		}
	})

	t.Run("Reject file without ObjectID", func(t *testing.T) {
		fake := &fakeMetadataStore{}
		store := metadata.NewValidatingStore(fake)

		entry := metadata.Entry{
			ID:       "file-1",
			ParentID: &parentRoot,
			Name:     "doc.txt",
			Kind:     metadata.EntryKindFile,
			ObjectID: nil,
			Revision: 1,
		}

		_, err := store.Create(ctx, entry)
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.createCalled {
			t.Errorf("next.Create should not be called")
		}
	})

	t.Run("Reject directory with ObjectID", func(t *testing.T) {
		fake := &fakeMetadataStore{}
		store := metadata.NewValidatingStore(fake)

		entry := metadata.Entry{
			ID:       "dir-1",
			ParentID: &parentRoot,
			Name:     "documents",
			Kind:     metadata.EntryKindDirectory,
			ObjectID: &dummyObjectID,
			Revision: 1,
		}

		_, err := store.Create(ctx, entry)
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.createCalled {
			t.Errorf("next.Create should not be called")
		}
	})

	t.Run("Reject Revision 0", func(t *testing.T) {
		fake := &fakeMetadataStore{}
		store := metadata.NewValidatingStore(fake)

		entry := metadata.Entry{
			ID:       "file-1",
			ParentID: &parentRoot,
			Name:     "doc.txt",
			Kind:     metadata.EntryKindFile,
			ObjectID: &dummyObjectID,
			Revision: 0,
		}

		_, err := store.Create(ctx, entry)
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.createCalled {
			t.Errorf("next.Create should not be called")
		}
	})

	t.Run("Reject non-existent parent", func(t *testing.T) {
		fake := &fakeMetadataStore{
			getByIDFunc: func(ctx context.Context, id metadata.EntryID) (metadata.Entry, error) {
				return metadata.Entry{}, metadata.ErrEntryNotFound
			},
		}
		store := metadata.NewValidatingStore(fake)

		entry := metadata.Entry{
			ID:       "file-1",
			ParentID: &parentRoot,
			Name:     "doc.txt",
			Kind:     metadata.EntryKindFile,
			ObjectID: &dummyObjectID,
			Revision: 1,
		}

		_, err := store.Create(ctx, entry)
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.createCalled {
			t.Errorf("next.Create should not be called")
		}
	})

	t.Run("Reject file as parent", func(t *testing.T) {
		fake := &fakeMetadataStore{
			getByIDFunc: func(ctx context.Context, id metadata.EntryID) (metadata.Entry, error) {
				return metadata.Entry{
					ID:   id,
					Kind: metadata.EntryKindFile,
				}, nil
			},
		}
		store := metadata.NewValidatingStore(fake)

		entry := metadata.Entry{
			ID:       "file-1",
			ParentID: &parentRoot,
			Name:     "doc.txt",
			Kind:     metadata.EntryKindFile,
			ObjectID: &dummyObjectID,
			Revision: 1,
		}

		_, err := store.Create(ctx, entry)
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.createCalled {
			t.Errorf("next.Create should not be called")
		}
	})
}
