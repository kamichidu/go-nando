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

func (f *fakeMetadataStore) Move(ctx context.Context, id metadata.EntryID, newParentID metadata.EntryID, newName string) (metadata.Entry, error) {
	f.moveCalled = true
	f.movePassedID = id
	f.movePassedParentID = newParentID
	f.movePassedName = newName
	return metadata.Entry{
		ID:       id,
		ParentID: &newParentID,
		Name:     newName,
	}, nil
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

		_, err := store.Move(ctx, "file-1", "dir-dest", "newname.txt")
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

		_, err := store.Move(ctx, "dir-1", "dir-dest", "newdir")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !fake.moveCalled {
			t.Errorf("expected next.Move to be called")
		}
	})

	t.Run("Reject empty target ID", func(t *testing.T) {
		fake := &fakeMetadataStore{}
		store := metadata.NewValidatingStore(fake)

		_, err := store.Move(ctx, "", "dir-dest", "newname.txt")
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

		_, err := store.Move(ctx, "root", "dir-dest", "newname")
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

		_, err := store.Move(ctx, "file-1", "", "newname.txt")
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

		_, err := store.Move(ctx, "file-1", "dir-dest", "")
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.moveCalled {
			t.Errorf("next.Move should not be called")
		}
	})

	t.Run("Non-existent target returns ErrEntryNotFound", func(t *testing.T) {
		fake := &fakeMetadataStore{
			getByIDFunc: func(ctx context.Context, id metadata.EntryID) (metadata.Entry, error) {
				return metadata.Entry{}, metadata.ErrEntryNotFound
			},
		}
		store := metadata.NewValidatingStore(fake)

		_, err := store.Move(ctx, "file-1", "dir-dest", "newname.txt")
		if !errors.Is(err, metadata.ErrEntryNotFound) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryNotFound, err)
		}
		if fake.moveCalled {
			t.Errorf("next.Move should not be called")
		}
	})

	t.Run("Non-existent destination Parent returns ErrEntryConflict", func(t *testing.T) {
		fake := &fakeMetadataStore{
			getByIDFunc: func(ctx context.Context, id metadata.EntryID) (metadata.Entry, error) {
				if id == "file-1" {
					return metadata.Entry{
						ID:   "file-1",
						Kind: metadata.EntryKindFile,
					}, nil
				}
				return metadata.Entry{}, metadata.ErrEntryNotFound
			},
		}
		store := metadata.NewValidatingStore(fake)

		_, err := store.Move(ctx, "file-1", "dir-dest", "newname.txt")
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.moveCalled {
			t.Errorf("next.Move should not be called")
		}
	})

	t.Run("Reject file as destination Parent", func(t *testing.T) {
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
						Kind: metadata.EntryKindFile,
					}, nil
				}
				return metadata.Entry{}, metadata.ErrEntryNotFound
			},
		}
		store := metadata.NewValidatingStore(fake)

		_, err := store.Move(ctx, "file-1", "dir-dest", "newname.txt")
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.moveCalled {
			t.Errorf("next.Move should not be called")
		}
	})

	t.Run("Reject self move", func(t *testing.T) {
		fake := &fakeMetadataStore{
			getByIDFunc: func(ctx context.Context, id metadata.EntryID) (metadata.Entry, error) {
				return metadata.Entry{
					ID:   id,
					Kind: metadata.EntryKindDirectory,
				}, nil
			},
		}
		store := metadata.NewValidatingStore(fake)

		_, err := store.Move(ctx, "dir-1", "dir-1", "newname")
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryConflict, err)
		}
		if fake.moveCalled {
			t.Errorf("next.Move should not be called")
		}
	})

	t.Run("Reject directory to direct child move", func(t *testing.T) {
		dir1ID := metadata.EntryID("dir-1")
		fake := &fakeMetadataStore{
			getByIDFunc: func(ctx context.Context, id metadata.EntryID) (metadata.Entry, error) {
				if id == "dir-1" {
					return metadata.Entry{
						ID:   "dir-1",
						Kind: metadata.EntryKindDirectory,
					}, nil
				}
				if id == "child-1" {
					return metadata.Entry{
						ID:       "child-1",
						Kind:     metadata.EntryKindDirectory,
						ParentID: &dir1ID,
					}, nil
				}
				return metadata.Entry{}, metadata.ErrEntryNotFound
			},
		}
		store := metadata.NewValidatingStore(fake)

		_, err := store.Move(ctx, "dir-1", "child-1", "moved")
		if !errors.Is(err, metadata.ErrEntryConflict) || !strings.Contains(err.Error(), "hierarchy cycle") {
			t.Errorf("expected hierarchy cycle error, got %v", err)
		}
		if fake.moveCalled {
			t.Errorf("next.Move should not be called")
		}
	})

	t.Run("Reject directory to deep descendant move", func(t *testing.T) {
		dir1ID := metadata.EntryID("dir-1")
		child1ID := metadata.EntryID("child-1")
		fake := &fakeMetadataStore{
			getByIDFunc: func(ctx context.Context, id metadata.EntryID) (metadata.Entry, error) {
				if id == "dir-1" {
					return metadata.Entry{
						ID:   "dir-1",
						Kind: metadata.EntryKindDirectory,
					}, nil
				}
				if id == "child-1" {
					return metadata.Entry{
						ID:       "child-1",
						Kind:     metadata.EntryKindDirectory,
						ParentID: &dir1ID,
					}, nil
				}
				if id == "grandchild-1" {
					return metadata.Entry{
						ID:       "grandchild-1",
						Kind:     metadata.EntryKindDirectory,
						ParentID: &child1ID,
					}, nil
				}
				return metadata.Entry{}, metadata.ErrEntryNotFound
			},
		}
		store := metadata.NewValidatingStore(fake)

		_, err := store.Move(ctx, "dir-1", "grandchild-1", "moved")
		if !errors.Is(err, metadata.ErrEntryConflict) || !strings.Contains(err.Error(), "hierarchy cycle") {
			t.Errorf("expected hierarchy cycle error, got %v", err)
		}
		if fake.moveCalled {
			t.Errorf("next.Move should not be called")
		}
	})

	t.Run("Reject cyclic metadata hierarchy with cycle detection to prevent infinite loop", func(t *testing.T) {
		cycle1ID := metadata.EntryID("cycle-1")
		cycle2ID := metadata.EntryID("cycle-2")

		fake := &fakeMetadataStore{
			getByIDFunc: func(ctx context.Context, id metadata.EntryID) (metadata.Entry, error) {
				if id == "dir-target" {
					return metadata.Entry{
						ID:   "dir-target",
						Kind: metadata.EntryKindDirectory,
					}, nil
				}
				if id == "cycle-1" {
					return metadata.Entry{
						ID:       "cycle-1",
						Kind:     metadata.EntryKindDirectory,
						ParentID: &cycle2ID,
					}, nil
				}
				if id == "cycle-2" {
					return metadata.Entry{
						ID:       "cycle-2",
						Kind:     metadata.EntryKindDirectory,
						ParentID: &cycle1ID,
					}, nil
				}
				return metadata.Entry{}, metadata.ErrEntryNotFound
			},
		}
		store := metadata.NewValidatingStore(fake)

		_, err := store.Move(ctx, "dir-target", "cycle-1", "moved")
		if !errors.Is(err, metadata.ErrEntryConflict) || !strings.Contains(err.Error(), "cycle detected in parent ancestry") {
			t.Errorf("expected cycle detected ErrEntryConflict, got %v", err)
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
