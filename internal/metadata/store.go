package metadata

import (
	"context"

	"github.com/kamichidu/go-nando/internal/content"
)

// MetadataStore defines the interface for managing path metadata inside a SQLite database.
type MetadataStore interface {
	Initialize(ctx context.Context) error
	Root(ctx context.Context) (Entry, error)
	GetByID(ctx context.Context, id EntryID) (Entry, error)
	GetChild(ctx context.Context, parentID EntryID, name string) (Entry, error)
	ListChildren(ctx context.Context, parentID EntryID) ([]Entry, error)
	Create(ctx context.Context, entry Entry) (Entry, error)
	Update(ctx context.Context, entry Entry) (Entry, error)
	Move(ctx context.Context, id EntryID, newParentID EntryID, newName string) (Entry, error)
	Delete(ctx context.Context, id EntryID) error
	ReferencedObjectIDs(ctx context.Context) ([]content.ID, error)
	ListAll(ctx context.Context) ([]Entry, error)
	CheckSchemaCompatibility(ctx context.Context) error
}
