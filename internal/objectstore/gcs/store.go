package gcs

import (
	"context"
	"errors"
	"io"
)

// ErrNotImplemented is returned for unimplemented GCS operations.
var ErrNotImplemented = errors.New("gcs object store is not implemented yet (TODO)")

// Store implements the ObjectStore interface on top of Google Cloud Storage.
// TODO: Implement this GCS object store using cloud.google.com/go/storage in a future phase.
type Store struct{}

// NewStore initializes a new GCS ObjectStore.
func NewStore() *Store {
	return &Store{}
}

// Put is not implemented yet.
func (s *Store) Put(ctx context.Context, hash string, src io.Reader) error {
	return ErrNotImplemented
}

// Open is not implemented yet.
func (s *Store) Open(ctx context.Context, hash string) (io.ReadCloser, error) {
	return nil, ErrNotImplemented
}

// Exists is not implemented yet.
func (s *Store) Exists(ctx context.Context, hash string) (bool, error) {
	return false, ErrNotImplemented
}

// Delete is not implemented yet.
func (s *Store) Delete(ctx context.Context, hash string) error {
	return ErrNotImplemented
}

// Verify is not implemented yet.
func (s *Store) Verify(ctx context.Context, hash string) error {
	return ErrNotImplemented
}

// ListHashes is not implemented yet.
func (s *Store) ListHashes(ctx context.Context) ([]string, error) {
	return nil, ErrNotImplemented
}
