package repository

import (
	"context"
	"fmt"
	"io"

	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/objectstore"
)

// Repository coordinates the interactions between MetadataStore and ObjectStore.
type Repository struct {
	metaStore   metadata.MetadataStore
	objectStore objectstore.ObjectStore
	hasher      content.Hasher
}

// NewRepository creates a new Repository instance.
func NewRepository(metaStore metadata.MetadataStore, objStore objectstore.ObjectStore, hasher content.Hasher) *Repository {
	return &Repository{
		metaStore:   metaStore,
		objectStore: objStore,
		hasher:      hasher,
	}
}

// List returns immediate children of the logical path.
func (r *Repository) List(ctx context.Context, repoPath string) ([]metadata.Entry, error) {
	return r.metaStore.ListEntries(ctx, repoPath)
}

// Stat returns metadata details of the given logical path.
func (r *Repository) Stat(ctx context.Context, repoPath string) (metadata.Entry, error) {
	return r.metaStore.GetEntry(ctx, repoPath)
}

// Cat streams content of the given logical path to the specified writer.
func (r *Repository) Cat(ctx context.Context, repoPath string, w io.Writer) error {
	entry, err := r.metaStore.GetEntry(ctx, repoPath)
	if err != nil {
		return err
	}

	rc, err := r.objectStore.Open(ctx, entry.Hash)
	if err != nil {
		return fmt.Errorf("failed to open object from store: %w", err)
	}
	defer rc.Close()

	if _, err := io.Copy(w, rc); err != nil {
		return fmt.Errorf("failed to stream object data: %w", err)
	}
	return nil
}

// GC deletes unreferenced objects from the object store.
func (r *Repository) GC(ctx context.Context) (int, error) {
	refHashes, err := r.metaStore.ReferencedHashes(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to list referenced hashes: %w", err)
	}

	refSet := make(map[string]bool)
	for _, h := range refHashes {
		refSet[h] = true
	}

	allHashes, err := r.objectStore.ListHashes(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to list object hashes: %w", err)
	}

	deletedCount := 0
	for _, h := range allHashes {
		if !refSet[h] {
			if err := r.objectStore.Delete(ctx, h); err != nil {
				return deletedCount, fmt.Errorf("failed to delete unreferenced object %q: %w", h, err)
			}
			deletedCount++
		}
	}

	return deletedCount, nil
}

// Doctor verifies repository integrity (checking database reference matches physical object and is intact).
func (r *Repository) Doctor(ctx context.Context) error {
	refHashes, err := r.metaStore.ReferencedHashes(ctx)
	if err != nil {
		return fmt.Errorf("failed to list referenced hashes: %w", err)
	}

	for _, h := range refHashes {
		exists, err := r.objectStore.Exists(ctx, h)
		if err != nil {
			return fmt.Errorf("failed to check existence of object %q: %w", h, err)
		}
		if !exists {
			return fmt.Errorf("repository corruption: referenced object %q is missing from object store", h)
		}

		if err := r.objectStore.Verify(ctx, h); err != nil {
			return fmt.Errorf("repository corruption: verification failed for object %q: %w", h, err)
		}
	}

	return nil
}
