package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
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

// ObjectInfo describes a committed Local object for capacity accounting.
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

// Commit stages and atomically publishes an object. Repository Put uses the
// explicit Stage/Publish lifecycle so input hashing can run outside its
// repository-wide exclusion.
func (s *Store) Commit(ctx context.Context, src io.Reader) (objectstore.CommitResult, error) {
	operationID := uuid.NewString()
	session, err := s.BeginStage(ctx, operationID)
	if err != nil {
		return objectstore.CommitResult{}, err
	}
	staged, stageErr := session.Stage(ctx, src)
	closeErr := session.Close()
	if stageErr != nil || closeErr != nil {
		cleanupErr := s.CleanupStaging(context.Background(), operationID)
		return objectstore.CommitResult{}, errors.Join(stageErr, closeErr, cleanupErr)
	}
	result, publishErr := s.Publish(ctx, staged)
	cleanupErr := s.CleanupStaging(context.Background(), operationID)
	if publishErr != nil {
		return objectstore.CommitResult{}, errors.Join(publishErr, cleanupErr)
	}
	if cleanupErr != nil {
		return result, fmt.Errorf("object committed but staging cleanup failed: %w", cleanupErr)
	}
	return result, nil
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

// Size returns the size of a committed Local object.
func (s *Store) Size(ctx context.Context, id content.ID) (int64, error) {
	if err := id.Validate(); err != nil {
		return 0, err
	}
	info, err := os.Stat(filepath.Join(s.objectsDir, id.String()))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, objectstore.ErrObjectNotFound
		}
		return 0, fmt.Errorf("stat object %q: %w", id.String(), err)
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("object %q is not a regular file", id.String())
	}
	return info.Size(), nil
}

// ListObjectInfo returns valid committed objects and their filesystem size/time.
func (s *Store) ListObjectInfo(ctx context.Context) ([]objectstore.LocalObjectInfo, error) {
	entries, err := os.ReadDir(s.objectsDir)
	if err != nil {
		return nil, fmt.Errorf("read objects directory: %w", err)
	}
	result := make([]objectstore.LocalObjectInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		id, err := content.ParseID(entry.Name())
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("stat Local object %q: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		result = append(result, objectstore.LocalObjectInfo{ID: id, Size: info.Size(), ModTime: info.ModTime()})
	}
	return result, nil
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

// DeleteRaw removes a malformed object namespace file without interpreting its
// name as a content ID. Directories and non-regular files are preserved.
func (s *Store) DeleteRaw(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if name == "" || filepath.Base(name) != name {
		return fmt.Errorf("invalid raw object name %q", name)
	}
	if _, err := content.ParseID(name); err == nil {
		return fmt.Errorf("raw object name %q is a valid content ID", name)
	}
	path := filepath.Join(s.objectsDir, name)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to remove non-regular raw object %q", name)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove raw object %q: %w", name, err)
	}
	return nil
}
