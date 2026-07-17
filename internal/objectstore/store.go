package objectstore

import (
	"context"
	"errors"
	"io"

	"github.com/kamichidu/go-nando/internal/content"
)

// ErrObjectNotFound is returned when an object is not found in the store.
var ErrObjectNotFound = errors.New("object not found")

// ErrHashMismatch is returned when the object's content hash does not match its identifier.
var ErrHashMismatch = errors.New("object hash mismatch")

// CommitResult holds the content ID and persisted size of a committed object.
type CommitResult struct {
	ID   content.ID
	Size int64
}

// ObjectStore defines the interface for interacting with the content-addressable object store.
type ObjectStore interface {
	Commit(ctx context.Context, src io.Reader) (CommitResult, error)
	Open(ctx context.Context, id content.ID) (io.ReadCloser, error)
	Exists(ctx context.Context, id content.ID) (bool, error)
	Delete(ctx context.Context, id content.ID) error
	Verify(ctx context.Context, id content.ID) error
	List(ctx context.Context) ([]content.ID, error)
	ListRaw(ctx context.Context) ([]string, error)
}
