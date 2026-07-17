package gcs

import (
	"context"
	"errors"
	"io"

	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/objectstore"
)

// ErrNotImplemented is returned for unimplemented GCS operations.
var ErrNotImplemented = errors.New("gcs object store is not implemented yet (TODO)")

// Store implements the ObjectStore interface on top of Google Cloud Storage.
type Store struct{}

// NewStore initializes a new GCS ObjectStore.
func NewStore() *Store {
	return &Store{}
}

// Commit is not implemented yet.
func (s *Store) Commit(ctx context.Context, src io.Reader) (objectstore.CommitResult, error) {
	return objectstore.CommitResult{}, ErrNotImplemented
}

// Open is not implemented yet.
func (s *Store) Open(ctx context.Context, id content.ID) (io.ReadCloser, error) {
	return nil, ErrNotImplemented
}

// Exists is not implemented yet.
func (s *Store) Exists(ctx context.Context, id content.ID) (bool, error) {
	return false, ErrNotImplemented
}

// Delete is not implemented yet.
func (s *Store) Delete(ctx context.Context, id content.ID) error {
	return ErrNotImplemented
}

// Verify is not implemented yet.
func (s *Store) Verify(ctx context.Context, id content.ID) error {
	return ErrNotImplemented
}

// List is not implemented yet.
func (s *Store) List(ctx context.Context) ([]content.ID, error) {
	return nil, ErrNotImplemented
}

// ListRaw is not implemented yet.
func (s *Store) ListRaw(ctx context.Context) ([]string, error) {
	return nil, ErrNotImplemented
}
