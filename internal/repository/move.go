package repository

import (
	"context"
)

// Move renames or moves a logical path (file or directory) inside the repository.
func (r *Repository) Move(ctx context.Context, srcPath, dstPath string) error {
	return r.metaStore.MoveEntry(ctx, srcPath, dstPath)
}
