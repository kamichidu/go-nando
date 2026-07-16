package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/kamichidu/go-nando/internal/metadata"
	_ "modernc.org/sqlite"
)

// Store implements the metadata.MetadataStore interface using SQLite.
type Store struct {
	db *sql.DB
}

// NewStore creates a new Store instance backed by SQLite.
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open SQLite database at %q: %w", dbPath, err)
	}

	// Limit to 1 open connection to prevent write-lock/busy contention in SQLite.
	db.SetMaxOpenConns(1)

	return &Store{db: db}, nil
}

// Close closes the underlying SQLite database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Initialize executes schema migrations.
func (s *Store) Initialize(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, MigrationV1)
	if err != nil {
		return fmt.Errorf("failed to initialize schema: %w", err)
	}
	return nil
}

// GetEntry retrieves a single active metadata entry by its absolute logical path.
func (s *Store) GetEntry(ctx context.Context, path string) (metadata.Entry, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT path, hash, size, mtime, mode, content_type, state, is_dir, created_at, updated_at
		FROM entries
		WHERE path = ? AND state = 'ready'
	`, path)

	var entry metadata.Entry
	var mtimeStr, createdAtStr, updatedAtStr string
	var stateStr string
	var isDirInt int

	err := row.Scan(
		&entry.Path,
		&entry.Hash,
		&entry.Size,
		&mtimeStr,
		&entry.Mode,
		&entry.ContentType,
		&stateStr,
		&isDirInt,
		&createdAtStr,
		&updatedAtStr,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return metadata.Entry{}, metadata.ErrEntryNotFound
		}
		return metadata.Entry{}, fmt.Errorf("failed to retrieve entry: %w", err)
	}

	entry.State = metadata.EntryState(stateStr)
	entry.IsDir = isDirInt != 0

	entry.Mtime, err = time.Parse(time.RFC3339, mtimeStr)
	if err != nil {
		return metadata.Entry{}, fmt.Errorf("failed to parse mtime: %w", err)
	}
	entry.CreatedAt, err = time.Parse(time.RFC3339, createdAtStr)
	if err != nil {
		return metadata.Entry{}, fmt.Errorf("failed to parse created_at: %w", err)
	}
	entry.UpdatedAt, err = time.Parse(time.RFC3339, updatedAtStr)
	if err != nil {
		return metadata.Entry{}, fmt.Errorf("failed to parse updated_at: %w", err)
	}

	return entry, nil
}

// ListEntries returns active child entries immediately under the given path.
func (s *Store) ListEntries(ctx context.Context, path string) ([]metadata.Entry, error) {
	prefix := path
	if prefix == "" {
		prefix = "/"
	}
	if prefix != "/" && prefix[len(prefix)-1] != '/' {
		prefix = prefix + "/"
	}

	// 1-based index in SQLite substr
	substrIndex := len(prefix) + 1

	rows, err := s.db.QueryContext(ctx, `
		SELECT path, hash, size, mtime, mode, content_type, state, is_dir, created_at, updated_at
		FROM entries
		WHERE state = 'ready' AND path LIKE ? AND instr(substr(path, ?), '/') = 0
	`, prefix+"%", substrIndex)
	if err != nil {
		return nil, fmt.Errorf("failed to list entries: %w", err)
	}
	defer rows.Close()

	var entries []metadata.Entry
	for rows.Next() {
		var entry metadata.Entry
		var mtimeStr, createdAtStr, updatedAtStr string
		var stateStr string
		var isDirInt int

		err := rows.Scan(
			&entry.Path,
			&entry.Hash,
			&entry.Size,
			&mtimeStr,
			&entry.Mode,
			&entry.ContentType,
			&stateStr,
			&isDirInt,
			&createdAtStr,
			&updatedAtStr,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan list entry: %w", err)
		}

		entry.State = metadata.EntryState(stateStr)
		entry.IsDir = isDirInt != 0

		entry.Mtime, err = time.Parse(time.RFC3339, mtimeStr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse mtime: %w", err)
		}
		entry.CreatedAt, err = time.Parse(time.RFC3339, createdAtStr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse created_at: %w", err)
		}
		entry.UpdatedAt, err = time.Parse(time.RFC3339, updatedAtStr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse updated_at: %w", err)
		}

		entries = append(entries, entry)
	}

	return entries, nil
}

// PutEntry creates or updates an entry.
func (s *Store) PutEntry(ctx context.Context, entry metadata.Entry) error {
	mtimeStr := entry.Mtime.UTC().Format(time.RFC3339)
	createdAtStr := entry.CreatedAt.UTC().Format(time.RFC3339)
	updatedAtStr := entry.UpdatedAt.UTC().Format(time.RFC3339)

	isDirInt := 0
	if entry.IsDir {
		isDirInt = 1
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO entries (path, hash, size, mtime, mode, content_type, state, is_dir, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			hash = excluded.hash,
			size = excluded.size,
			mtime = excluded.mtime,
			mode = excluded.mode,
			content_type = excluded.content_type,
			state = excluded.state,
			is_dir = excluded.is_dir,
			updated_at = excluded.updated_at
	`,
		entry.Path,
		entry.Hash,
		entry.Size,
		mtimeStr,
		entry.Mode,
		entry.ContentType,
		string(entry.State),
		isDirInt,
		createdAtStr,
		updatedAtStr,
	)
	if err != nil {
		return fmt.Errorf("failed to save entry metadata: %w", err)
	}
	return nil
}

// MoveEntry renames an entry and its recursive children (if directory).
func (s *Store) MoveEntry(ctx context.Context, src, dst string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to start transaction for move: %w", err)
	}
	defer tx.Rollback()

	var isDir bool
	var stateStr string
	err = tx.QueryRowContext(ctx, "SELECT is_dir, state FROM entries WHERE path = ? AND state = 'ready'", src).Scan(&isDir, &stateStr)
	if err != nil {
		if err == sql.ErrNoRows {
			return metadata.ErrEntryNotFound
		}
		return fmt.Errorf("failed to query source entry: %w", err)
	}

	// Check destination conflict
	var destExists bool
	err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM entries WHERE path = ? AND state = 'ready')", dst).Scan(&destExists)
	if err != nil {
		return fmt.Errorf("failed to check destination conflict: %w", err)
	}
	if destExists {
		return metadata.ErrEntryConflict
	}

	nowStr := time.Now().UTC().Format(time.RFC3339)

	// Move the node itself
	_, err = tx.ExecContext(ctx, `
		UPDATE entries
		SET path = ?, updated_at = ?
		WHERE path = ? AND state = 'ready'
	`, dst, nowStr, src)
	if err != nil {
		return fmt.Errorf("failed to rename source entry: %w", err)
	}

	// Move children recursively if it's a directory
	if isDir {
		srcPrefix := src + "/"
		substrIndex := len(src) + 1

		_, err = tx.ExecContext(ctx, `
			UPDATE entries
			SET path = ? || substr(path, ?), updated_at = ?
			WHERE path LIKE ? AND state = 'ready'
		`, dst, substrIndex, nowStr, srcPrefix+"%")
		if err != nil {
			return fmt.Errorf("failed to rename child entries: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit move transaction: %w", err)
	}
	return nil
}

// DeleteEntry marks an entry (and recursive children if directory) as deleted.
func (s *Store) DeleteEntry(ctx context.Context, path string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to start transaction for delete: %w", err)
	}
	defer tx.Rollback()

	var isDir bool
	err = tx.QueryRowContext(ctx, "SELECT is_dir FROM entries WHERE path = ? AND state = 'ready'", path).Scan(&isDir)
	if err != nil {
		if err == sql.ErrNoRows {
			return metadata.ErrEntryNotFound
		}
		return fmt.Errorf("failed to query entry for delete: %w", err)
	}

	nowStr := time.Now().UTC().Format(time.RFC3339)

	// Soft delete the main entry
	_, err = tx.ExecContext(ctx, `
		UPDATE entries
		SET state = 'deleted', updated_at = ?
		WHERE path = ? AND state = 'ready'
	`, nowStr, path)
	if err != nil {
		return fmt.Errorf("failed to soft delete entry: %w", err)
	}

	// Soft delete children if directory
	if isDir {
		srcPrefix := path + "/"
		_, err = tx.ExecContext(ctx, `
			UPDATE entries
			SET state = 'deleted', updated_at = ?
			WHERE path LIKE ? AND state = 'ready'
		`, nowStr, srcPrefix+"%")
		if err != nil {
			return fmt.Errorf("failed to soft delete children: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit delete transaction: %w", err)
	}
	return nil
}

// ReferencedHashes retrieves distinct hashes from ready or pending entries.
func (s *Store) ReferencedHashes(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT hash
		FROM entries
		WHERE state = 'ready' OR state = 'pending'
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query referenced hashes: %w", err)
	}
	defer rows.Close()

	var hashes []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, fmt.Errorf("failed to scan hash: %w", err)
		}
		if h != "" {
			hashes = append(hashes, h)
		}
	}
	return hashes, nil
}
