package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/objectstore"
)

// ErrCrossFilesystem is returned when staging and object paths are on
// different filesystems and atomic local publication is unavailable.
var ErrCrossFilesystem = errors.New("staging and object paths must be on the same filesystem")

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

// Commit writes an object to the store atomically by deriving the hash from the exact persisted bytes.
// tempDir and objectsDir must reside on the same filesystem for atomic publication.
func (s *Store) Commit(ctx context.Context, src io.Reader) (result objectstore.CommitResult, retErr error) {
	// Create a temporary file inside s.tempDir
	tempFile, err := os.CreateTemp(s.tempDir, "nando-obj-*")
	if err != nil {
		return objectstore.CommitResult{}, fmt.Errorf("failed to create temporary file: %w", err)
	}
	tempName := tempFile.Name()

	// Ensure cleanup on failure
	cleanup := true
	closed := false
	defer func() {
		if cleanup {
			if !closed {
				if err := tempFile.Close(); err != nil {
					retErr = errors.Join(retErr, fmt.Errorf("failed to close temporary file during cleanup: %w", err))
				}
			}
			if err := removeStagingFile(tempName); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("failed to remove temporary file during cleanup: %w", err))
			}
		}
	}()

	written, err := io.Copy(tempFile, src)
	if err != nil {
		return objectstore.CommitResult{}, fmt.Errorf("failed to write to temporary file: %w", err)
	}

	// Flush to disk
	if err := tempFile.Sync(); err != nil {
		return objectstore.CommitResult{}, fmt.Errorf("failed to sync temporary file: %w", err)
	}

	if _, err := tempFile.Seek(0, io.SeekStart); err != nil {
		return objectstore.CommitResult{}, fmt.Errorf("failed to seek temporary file for hashing: %w", err)
	}
	id, err := s.hasher.Sum(ctx, tempFile)
	if err != nil {
		return objectstore.CommitResult{}, fmt.Errorf("failed to hash temporary object: %w", err)
	}
	if err := id.Validate(); err != nil {
		return objectstore.CommitResult{}, fmt.Errorf("hasher returned invalid object ID: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return objectstore.CommitResult{}, fmt.Errorf("failed to close temporary file: %w", err)
	}
	closed = true

	destPath := filepath.Join(s.objectsDir, id.String())

	// Check if already exists
	exists, err := s.Exists(ctx, id)
	if err != nil {
		return objectstore.CommitResult{}, err
	}

	if exists {
		// Verify existing matches
		if err := s.Verify(ctx, id); err != nil {
			return objectstore.CommitResult{}, fmt.Errorf("existing object %s is corrupt: %w", id.String(), err)
		}
		// Existing valid object can be reused. Clean up the temp file.
		if err := removeStagingFile(tempName); err != nil {
			return objectstore.CommitResult{}, fmt.Errorf("failed to remove reused temporary object: %w", err)
		}
		cleanup = false
		return objectstore.CommitResult{
			ID:   id,
			Size: written,
		}, nil
	}

	// Publish temp file to final destination path (exclusive creation).
	// This ensures that we do not overwrite an existing file.
	published, err := publishNoReplace(tempName, destPath)
	if err != nil {
		return objectstore.CommitResult{}, fmt.Errorf("failed to publish object: %w", err)
	}

	if !published {
		// Already exists! Verify it.
		if err := s.Verify(ctx, id); err != nil {
			return objectstore.CommitResult{}, fmt.Errorf("existing object %s is corrupt: %w", id.String(), err)
		}
		if err := removeStagingFile(tempName); err != nil {
			return objectstore.CommitResult{}, fmt.Errorf("failed to remove duplicate temporary object: %w", err)
		}
		cleanup = false
		return objectstore.CommitResult{
			ID:   id,
			Size: written,
		}, nil
	}

	if err := removeStagingFile(tempName); err != nil {
		return objectstore.CommitResult{}, fmt.Errorf("object was published but temporary link cleanup failed: %w", err)
	}

	cleanup = false
	return objectstore.CommitResult{
		ID:   id,
		Size: written,
	}, nil
}

func removeStagingFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Open retrieves the reader for the given object.
func (s *Store) Open(ctx context.Context, id content.ID) (io.ReadCloser, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	path := filepath.Join(s.objectsDir, id.String())
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, objectstore.ErrObjectNotFound
		}
		return nil, fmt.Errorf("failed to open object %q: %w", id.String(), err)
	}
	return file, nil
}

// Exists checks if the object is present in the store.
func (s *Store) Exists(ctx context.Context, id content.ID) (bool, error) {
	if err := id.Validate(); err != nil {
		return false, err
	}
	path := filepath.Join(s.objectsDir, id.String())
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("failed to check existence of object %q: %w", id.String(), err)
}

// Delete removes an object from the store.
func (s *Store) Delete(ctx context.Context, id content.ID) error {
	if err := id.Validate(); err != nil {
		return err
	}
	path := filepath.Join(s.objectsDir, id.String())
	err := os.Remove(path)
	if err != nil {
		if os.IsNotExist(err) {
			return objectstore.ErrObjectNotFound
		}
		return fmt.Errorf("failed to delete object %q: %w", id.String(), err)
	}
	return nil
}

// Verify validates that the stored object matches its hash.
func (s *Store) Verify(ctx context.Context, id content.ID) error {
	if err := id.Validate(); err != nil {
		return err
	}
	file, err := s.Open(ctx, id)
	if err != nil {
		return err
	}
	defer file.Close()

	computed, err := s.hasher.Sum(ctx, file)
	if err != nil {
		return fmt.Errorf("failed to compute hash of object %q: %w", id.String(), err)
	}

	if !computed.Equal(id) {
		return fmt.Errorf("%w: expected %q, got %q", objectstore.ErrHashMismatch, id.String(), computed.String())
	}

	return nil
}

// List returns all valid committed object IDs currently stored in the objects directory.
func (s *Store) List(ctx context.Context) ([]content.ID, error) {
	entries, err := os.ReadDir(s.objectsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read objects directory: %w", err)
	}

	var ids []content.ID
	for _, entry := range entries {
		if !entry.IsDir() {
			id, err := content.ParseID(entry.Name())
			if err != nil {
				// Skip invalid filenames to not allow them to escape as valid IDs
				continue
			}
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// ListRaw returns the raw filenames of all files stored in the objects directory.
func (s *Store) ListRaw(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(s.objectsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read objects directory: %w", err)
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}
