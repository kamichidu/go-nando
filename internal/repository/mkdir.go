package repository

import (
	"context"
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/google/uuid"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/paths"
)

// Mkdir creates one explicit directory Entry. Its parent must already exist.
func (r *Repository) Mkdir(ctx context.Context, repositoryPath string, mode uint32) error {
	normalized, err := paths.NormalizeLogicalPath(repositoryPath)
	if err != nil || normalized == "/" {
		return fmt.Errorf("invalid directory path %q: %w", repositoryPath, metadata.ErrEntryConflict)
	}
	parent, err := r.ResolvePath(ctx, path.Dir(normalized))
	if err != nil {
		return err
	}
	if parent.Kind != metadata.EntryKindDirectory {
		return fmt.Errorf("parent is not a directory: %w", metadata.ErrEntryConflict)
	}
	if _, err := r.metaStore.GetChild(ctx, parent.ID, path.Base(normalized)); err == nil {
		return fmt.Errorf("directory already exists: %w", metadata.ErrEntryConflict)
	} else if !errors.Is(err, metadata.ErrEntryNotFound) {
		return err
	}
	now := metadata.TimestampFromTime(time.Now().UTC())
	_, err = r.metaStore.Create(ctx, metadata.Entry{
		ID: metadata.EntryID(uuid.NewString()), ParentID: &parent.ID, Name: path.Base(normalized),
		Kind: metadata.EntryKindDirectory, Mode: mode, Revision: 1, CreatedAt: now, UpdatedAt: now,
	})
	return err
}
