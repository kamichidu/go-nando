package metadata

import (
	"context"
	"time"

	"github.com/kamichidu/go-nando/internal/content"
)

// MetadataStore defines the interface for managing repository metadata.
type MetadataStore interface {
	InternalBufferStore
	Root(ctx context.Context) (Entry, error)
	GetByID(ctx context.Context, id EntryID) (Entry, error)
	GetChild(ctx context.Context, parentID EntryID, name string) (Entry, error)
	ListChildren(ctx context.Context, parentID EntryID) ([]Entry, error)
	Create(ctx context.Context, entry Entry) (Entry, error)
	Update(ctx context.Context, entry Entry) (Entry, error)

	// Move moves or renames an entry.
	//
	// Entry.ID identifies the target entry.
	// Entry.ParentID and Entry.Name specify the destination.
	// Entry.Revision is the revision observed by the caller and is used for
	// optimistic concurrency control.
	// Other Entry fields are ignored and are not modified by Move.
	Move(ctx context.Context, entry Entry) (Entry, error)

	Delete(ctx context.Context, id EntryID) error
	// CommitPut atomically creates or updates a file Entry and, when
	// enqueueTransfer is true, records its content ID for asynchronous delivery.
	// Local-only Put passes false. Backends must not expose transaction types.
	CommitPut(ctx context.Context, entry Entry, enqueueTransfer bool) (Entry, error)
	PendingTransfers(ctx context.Context) ([]content.ID, error)
	OldestPendingTransfer(ctx context.Context) (time.Time, bool, error)
	CompleteTransfer(ctx context.Context, id content.ID) error
	ReferencedObjectIDs(ctx context.Context) ([]content.ID, error)
	ContentMD5(ctx context.Context, id content.ID) ([]byte, error)
	ListAll(ctx context.Context) ([]Entry, error)
	GetDeadProperties(ctx context.Context, id EntryID) ([]DeadProperty, error)
	UpdateDeadProperties(ctx context.Context, id EntryID, changes []DeadPropertyChange) error
}

// InternalBufferStore provides operations for detached trees that are never
// part of the public repository namespace. Each metadata operation is atomic;
// reclaiming a complete tree is resumable but not one global transaction.
type InternalBufferStore interface {
	CreateDetachedRoot(context.Context, Entry) (DetachedBuffer, error)
	CreateDetachedEntry(context.Context, BufferID, Entry) (Entry, error)
	SetDetachedDeadProperties(context.Context, BufferID, EntryID, []DeadPropertyChange) error
	PublishDetachedRoot(context.Context, BufferID, EntryID, string) (Entry, error)
	DetachTree(context.Context, EntryID, uint64, EntryID) (DetachedBuffer, error)
	DetachedBuffers(context.Context) ([]DetachedBuffer, error)
	ListDetachedChildren(context.Context, BufferID, EntryID) ([]Entry, error)
	DeleteDetachedEntry(context.Context, BufferID, EntryID) error
}
