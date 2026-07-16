package local

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/objectstore"
)

// Store implements the ObjectStore interface on top of the local filesystem.
type Store struct {
	objectsDir string
	tempDir    string
	hasher     content.Hasher
}

// NewStore initializes a new local ObjectStore.
func NewStore(objectsDir, tempDir string, hasher content.Hasher) (*Store, error) {
	if err := os.MkdirAll(objectsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create objects directory %q: %w", objectsDir, err)
	}
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create temp directory %q: %w", tempDir, err)
	}
	return &Store{
		objectsDir: objectsDir,
		tempDir:    tempDir,
		hasher:     hasher,
	}, nil
}

// Put writes an object to the store atomically if it doesn't already exist.
func (s *Store) Put(ctx context.Context, hash string, src io.Reader) error {
	destPath := filepath.Join(s.objectsDir, hash)

	// Check if already exists to prevent redundant saving
	exists, err := s.Exists(ctx, hash)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	// Create a temporary file inside s.tempDir to support atomic rename
	tempFile, err := os.CreateTemp(s.tempDir, "nando-obj-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %w", err)
	}
	tempName := tempFile.Name()

	// Ensure cleanup on failure
	cleanup := true
	defer func() {
		if cleanup {
			_ = tempFile.Close()
			_ = os.Remove(tempName)
		}
	}()

	// Stream write
	if _, err := io.Copy(tempFile, src); err != nil {
		return fmt.Errorf("failed to write to temporary file: %w", err)
	}

	// Flush to disk
	if err := tempFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync temporary file: %w", err)
	}

	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("failed to close temporary file: %w", err)
	}

	// Atomic rename within the same filesystem
	if err := os.Rename(tempName, destPath); err != nil {
		return fmt.Errorf("failed to atomic rename temporary file to destination %q: %w", destPath, err)
	}

	cleanup = false
	return nil
}

// Open retrieves the reader for the given object.
func (s *Store) Open(ctx context.Context, hash string) (io.ReadCloser, error) {
	path := filepath.Join(s.objectsDir, hash)
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, objectstore.ErrObjectNotFound
		}
		return nil, fmt.Errorf("failed to open object %q: %w", hash, err)
	}
	return file, nil
}

// Exists checks if the object is present in the store.
func (s *Store) Exists(ctx context.Context, hash string) (bool, error) {
	path := filepath.Join(s.objectsDir, hash)
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("failed to check existence of object %q: %w", hash, err)
}

// Delete removes an object from the store.
func (s *Store) Delete(ctx context.Context, hash string) error {
	path := filepath.Join(s.objectsDir, hash)
	err := os.Remove(path)
	if err != nil {
		if os.IsNotExist(err) {
			return objectstore.ErrObjectNotFound
		}
		return fmt.Errorf("failed to delete object %q: %w", hash, err)
	}
	return nil
}

// Verify validates that the stored object matches its hash.
func (s *Store) Verify(ctx context.Context, hash string) error {
	file, err := s.Open(ctx, hash)
	if err != nil {
		return err
	}
	defer file.Close()

	computed, err := s.hasher.Sum(ctx, file)
	if err != nil {
		return fmt.Errorf("failed to compute hash of object %q: %w", hash, err)
	}

	if computed.String() != hash {
		return fmt.Errorf("%w: expected %q, got %q", objectstore.ErrHashMismatch, hash, computed)
	}

	return nil
}

// ListHashes returns all hashes currently stored in the objects directory.
func (s *Store) ListHashes(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(s.objectsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read objects directory: %w", err)
	}

	var hashes []string
	for _, entry := range entries {
		if !entry.IsDir() {
			hashes = append(hashes, entry.Name())
		}
	}
	return hashes, nil
}
