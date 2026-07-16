package objectstore

import (
	"context"
	"errors"
	"io"
)

// ErrObjectNotFound is returned when an object is not found in the store.
var ErrObjectNotFound = errors.New("object not found")

// ErrHashMismatch is returned when the object's content hash does not match its identifier.
var ErrHashMismatch = errors.New("object hash mismatch")

// ObjectStore defines the interface for interacting with the content-addressable object store.
type ObjectStore interface {
	Put(ctx context.Context, hash string, src io.Reader) error
	Open(ctx context.Context, hash string) (io.ReadCloser, error)
	Exists(ctx context.Context, hash string) (bool, error)
	Delete(ctx context.Context, hash string) error
	Verify(ctx context.Context, hash string) error
	ListHashes(ctx context.Context) ([]string, error)
}
