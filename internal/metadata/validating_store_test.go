package metadata_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/metadata"
)

type fakeMetadataStore struct {
	metadata.MetadataStore
	createCalled bool
	getByIDFunc  func(ctx context.Context, id metadata.EntryID) (metadata.Entry, error)
	createFunc   func(ctx context.Context, entry metadata.Entry) (metadata.Entry, error)

	moveCalled         bool
	movePassedID       metadata.EntryID
	movePassedParentID metadata.EntryID
	movePassedName     string

	updateCalled bool
	updateFunc   func(ctx context.Context, entry metadata.Entry) (metadata.Entry, error)

	deleteCalled bool
	deleteFunc   func(ctx context.Context, id metadata.EntryID) error
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

func (f *fakeMetadataStore) Update(ctx context.Context, entry metadata.Entry) (metadata.Entry, error) {
	f.updateCalled = true
	if f.updateFunc != nil {
		return f.updateFunc(ctx, entry)
	}
	return entry, nil
}

func (f *fakeMetadataStore) Delete(ctx context.Context, id metadata.EntryID) error {
	f.deleteCalled = true
	if f.deleteFunc != nil {
		return f.deleteFunc(ctx, id)
	}
	return nil
}

func (f *fakeMetadataStore) Move(ctx context.Context, entry metadata.Entry) (metadata.Entry, error) {
	f.moveCalled = true
	f.movePassedID = entry.ID
	if entry.ParentID != nil {
		f.movePassedParentID = *entry.ParentID
	}
	f.movePassedName = entry.Name
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
		getByIDCalled := false
		fake := &fakeMetadataStore{
			getByIDFunc: func(ctx context.Context, id metadata.EntryID) (metadata.Entry, error) {
				getByIDCalled = true
				return metadata.Entry{}, metadata.ErrEntryNotFound
			},
		}
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
		if getByIDCalled {
			t.Errorf("ValidatingStore.Create should not fetch parent entry beforehand")
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

	t.Run("Move file to another directory successfully", func(t *testing.T) {
		fake := &fakeMetadataStore{
			getByIDFunc: func(ctx context.Context, id metadata.EntryID) (metadata.Entry, error) {
				if id == "file-1" {
					return metadata.Entry{
						ID:   "file-1",
						Kind: metadata.EntryKindFile,
					}, nil
				}
				if id == "dir-dest" {
					return metadata.Entry{
						ID:   "dir-dest",
						Kind: metadata.EntryKindDirectory,
					}, nil
				}
				return metadata.Entry{}, metadata.ErrEntryNotFound
			},
		}
		store := metadata.NewValidatingStore(fake)

		destParent := metadata.EntryID("dir-dest")
		_, err := store.Move(ctx, metadata.Entry{
			ID:       "file-1",
			ParentID: &destParent,
			Name:     "newname.txt",
			Revision: 1,
		})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !fake.moveCalled {
			t.Errorf("expected next.Move to be called")
		}
		if fake.movePassedID != "file-1" || fake.movePassedParentID != "dir-dest" || fake.movePassedName != "newname.txt" {
			t.Errorf("expected arguments to be passed unchanged")
		}
	})

	t.Run("Move directory to another directory successfully", func(t *testing.T) {
		fake := &fakeMetadataStore{
			getByIDFunc: func(ctx context.Context, id metadata.EntryID) (metadata.Entry, error) {
				if id == "dir-1" {
					return metadata.Entry{
						ID:   "dir-1",
						Kind: metadata.EntryKindDirectory,
					}, nil
				}
				if id == "dir-dest" {
					return metadata.Entry{
						ID:       "dir-dest",
						Kind:     metadata.EntryKindDirectory,
						ParentID: &parentRoot,
					}, nil
				}
				if id == "root" {
					return metadata.Entry{
						ID:   "root",
						Kind: metadata.EntryKindDirectory,
					}, nil
				}
				return metadata.Entry{}, metadata.ErrEntryNotFound
			},
		}
		store := metadata.NewValidatingStore(fake)

		destParent := metadata.EntryID("dir-dest")
		_, err := store.Move(ctx, metadata.Entry{
			ID:       "dir-1",
			ParentID: &destParent,
			Name:     "newdir",
			Revision: 1,
		})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !fake.moveCalled {
			t.Errorf("expected next.Move to be called")
		}
	})

	t.Run("Move rejects Revision 0 before calling backend", func(t *testing.T) {
		fake := &fakeMetadataStore{}
		store := metadata.NewValidatingStore(fake)

		destParent := metadata.EntryID("dir-dest")
		_, err := store.Move(ctx, metadata.Entry{
			ID:       "file-1",
			ParentID: &destParent,
			Name:     "newname.txt",
			Revision: 0,
		})
		if !errors.Is(err, metadata.ErrEntryConflict) || !strings.Contains(err.Error(), "invalid revision 0") {
			t.Errorf("expected ErrEntryConflict for Revision 0, got %v", err)
		}
		if fake.moveCalled {
			t.Errorf("next.Move should not be called")
		}
	})

	t.Run("Reject empty target ID", func(t *testing.T) {
		fake := &fakeMetadataStore{}
		store := metadata.NewValidatingStore(fake)

		destParent := metadata.EntryID("dir-dest")
		_, err := store.Move(ctx, metadata.Entry{
			ID:       "",
			ParentID: &destParent,
			Name:     "newname.txt",
		})
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.moveCalled {
			t.Errorf("next.Move should not be called")
		}
	})

	t.Run("Reject moving root", func(t *testing.T) {
		fake := &fakeMetadataStore{}
		store := metadata.NewValidatingStore(fake)

		destParent := metadata.EntryID("dir-dest")
		_, err := store.Move(ctx, metadata.Entry{
			ID:       "root",
			ParentID: &destParent,
			Name:     "newname",
		})
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.moveCalled {
			t.Errorf("next.Move should not be called")
		}
	})

	t.Run("Reject empty destination Parent ID", func(t *testing.T) {
		fake := &fakeMetadataStore{}
		store := metadata.NewValidatingStore(fake)

		destParent := metadata.EntryID("")
		_, err := store.Move(ctx, metadata.Entry{
			ID:       "file-1",
			ParentID: &destParent,
			Name:     "newname.txt",
		})
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.moveCalled {
			t.Errorf("next.Move should not be called")
		}
	})

	t.Run("Reject empty newName", func(t *testing.T) {
		fake := &fakeMetadataStore{}
		store := metadata.NewValidatingStore(fake)

		destParent := metadata.EntryID("dir-dest")
		_, err := store.Move(ctx, metadata.Entry{
			ID:       "file-1",
			ParentID: &destParent,
			Name:     "",
		})
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.moveCalled {
			t.Errorf("next.Move should not be called")
		}
	})

	t.Run("Update normal file successfully", func(t *testing.T) {
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

		res, err := store.Update(ctx, entry)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !fake.updateCalled {
			t.Errorf("expected next.Update to be called")
		}
		if res.ID != entry.ID {
			t.Errorf("expected returned entry ID %s, got %s", entry.ID, res.ID)
		}
	})

	t.Run("Reject empty EntryID on Update", func(t *testing.T) {
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

		_, err := store.Update(ctx, entry)
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.updateCalled {
			t.Errorf("next.Update should not be called")
		}
	})

	t.Run("Reject root modification via Update", func(t *testing.T) {
		fake := &fakeMetadataStore{}
		store := metadata.NewValidatingStore(fake)

		entry := metadata.Entry{
			ID:       "root",
			ParentID: nil,
			Name:     "",
			Kind:     metadata.EntryKindDirectory,
			Revision: 1,
		}

		_, err := store.Update(ctx, entry)
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.updateCalled {
			t.Errorf("next.Update should not be called")
		}
	})

	t.Run("Reject missing ParentID on Update", func(t *testing.T) {
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

		_, err := store.Update(ctx, entry)
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.updateCalled {
			t.Errorf("next.Update should not be called")
		}
	})

	t.Run("Reject empty parent ID on Update", func(t *testing.T) {
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

		_, err := store.Update(ctx, entry)
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.updateCalled {
			t.Errorf("next.Update should not be called")
		}
	})

	t.Run("Reject empty Name on Update", func(t *testing.T) {
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

		_, err := store.Update(ctx, entry)
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.updateCalled {
			t.Errorf("next.Update should not be called")
		}
	})

	t.Run("Reject unknown Kind on Update", func(t *testing.T) {
		fake := &fakeMetadataStore{}
		store := metadata.NewValidatingStore(fake)

		entry := metadata.Entry{
			ID:       "file-1",
			ParentID: &parentRoot,
			Name:     "doc.txt",
			Kind:     metadata.EntryKind("invalid"),
			ObjectID: &dummyObjectID,
			Revision: 1,
		}

		_, err := store.Update(ctx, entry)
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.updateCalled {
			t.Errorf("next.Update should not be called")
		}
	})

	t.Run("Reject file without ObjectID on Update", func(t *testing.T) {
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

		_, err := store.Update(ctx, entry)
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.updateCalled {
			t.Errorf("next.Update should not be called")
		}
	})

	t.Run("Reject directory with ObjectID on Update", func(t *testing.T) {
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

		_, err := store.Update(ctx, entry)
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.updateCalled {
			t.Errorf("next.Update should not be called")
		}
	})

	t.Run("Reject Revision 0 on Update", func(t *testing.T) {
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

		_, err := store.Update(ctx, entry)
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.updateCalled {
			t.Errorf("next.Update should not be called")
		}
	})

	t.Run("Delete normal entry successfully", func(t *testing.T) {
		fake := &fakeMetadataStore{}
		store := metadata.NewValidatingStore(fake)

		err := store.Delete(ctx, "file-1")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !fake.deleteCalled {
			t.Errorf("expected next.Delete to be called")
		}
	})

	t.Run("Reject empty EntryID on Delete", func(t *testing.T) {
		fake := &fakeMetadataStore{}
		store := metadata.NewValidatingStore(fake)

		err := store.Delete(ctx, "")
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.deleteCalled {
			t.Errorf("next.Delete should not be called")
		}
	})

	t.Run("Reject root deletion via Delete", func(t *testing.T) {
		fake := &fakeMetadataStore{}
		store := metadata.NewValidatingStore(fake)

		err := store.Delete(ctx, "root")
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.deleteCalled {
			t.Errorf("next.Delete should not be called")
		}
	})
}
