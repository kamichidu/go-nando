package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kamichidu/go-nando/internal/content"
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

	// Explicitly enable foreign key constraints for this connection.
	// Since SetMaxOpenConns(1) limits the pool to exactly 1 persistent connection,
	// executing this PRAGMA on the DB ensures it is applied to the active connection.
	// NOTE: If the connection pool is expanded to multiple connections in the future,
	// this PRAGMA must be applied to every connection (e.g., using a connect hook like
	// sqlite.RegisterConnectHook or setting it in the DSN if supported by the driver).
	_, err = db.Exec("PRAGMA foreign_keys = ON;")
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to enable foreign key constraints: %w", err)
	}

	return &Store{db: db}, nil
}

// Close closes the underlying SQLite database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Initialize executes schema migrations and ensures a single root entry is present.
func (s *Store) Initialize(ctx context.Context) error {
	// Execute schema migration
	_, err := s.db.ExecContext(ctx, MigrationV1)
	if err != nil {
		return fmt.Errorf("failed to initialize schema: %w", err)
	}

	// Insert the root entry if it does not exist. Bypasses validateEntry as root creation is only allowed during initialization.
	now := time.Now().UTC().Truncate(time.Second)
	_, err = s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO entries (
			entry_id, parent_id, name, kind, object_id, size, mtime, mode, content_type, revision, created_at, updated_at
		) VALUES (
			'root', NULL, '', 'directory', NULL, 0, ?, 0755, '', 1, ?, ?
		)
	`, now.Format(time.RFC3339), now.Format(time.RFC3339), now.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("failed to create root entry: %w", err)
	}

	return nil
}

// CheckSchemaCompatibility checks if the schema is compatible.
func (s *Store) CheckSchemaCompatibility(ctx context.Context) error {
	var name string
	err := s.db.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name='entries'").Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to check entries table: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info(entries)")
	if err != nil {
		return fmt.Errorf("failed to get table info: %w", err)
	}
	defer rows.Close()

	hasPathColumn := false
	hasEntryIDColumn := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltVal sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltVal, &pk); err != nil {
			return fmt.Errorf("failed to scan table info row: %w", err)
		}
		if name == "path" {
			hasPathColumn = true
		}
		if name == "entry_id" {
			hasEntryIDColumn = true
		}
	}

	if hasPathColumn && !hasEntryIDColumn {
		return fmt.Errorf("incompatible obsolete Phase 0 repository format detected (uses obsolete path-based schema)")
	}

	return nil
}

func (s *Store) scanEntry(scanner interface{ Scan(dest ...any) error }) (metadata.Entry, error) {
	var entry metadata.Entry
	var idStr string
	var parentIDStr sql.NullString
	var objectIDStr sql.NullString
	var mtimeStr, createdAtStr, updatedAtStr string
	var kindStr string

	err := scanner.Scan(
		&idStr,
		&parentIDStr,
		&entry.Name,
		&kindStr,
		&objectIDStr,
		&entry.Size,
		&mtimeStr,
		&entry.Mode,
		&entry.ContentType,
		&entry.Revision,
		&createdAtStr,
		&updatedAtStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return metadata.Entry{}, metadata.ErrEntryNotFound
		}
		return metadata.Entry{}, fmt.Errorf("failed to scan entry: %w", err)
	}

	entry.ID = metadata.EntryID(idStr)
	if parentIDStr.Valid {
		pid := metadata.EntryID(parentIDStr.String)
		entry.ParentID = &pid
	}
	entry.Kind = metadata.EntryKind(kindStr)

	if objectIDStr.Valid && objectIDStr.String != "" {
		oid, err := content.ParseID(objectIDStr.String)
		if err != nil {
			return metadata.Entry{}, fmt.Errorf("failed to parse object ID %q: %w", objectIDStr.String, err)
		}
		entry.ObjectID = &oid
	}

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

// Root retrieves the root entry of the logical repository.
func (s *Store) Root(ctx context.Context) (metadata.Entry, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT entry_id, parent_id, name, kind, object_id, size, mtime, mode, content_type, revision, created_at, updated_at
		FROM entries
		WHERE entry_id = 'root'
	`)
	return s.scanEntry(row)
}

type dbtx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) getByID(ctx context.Context, q dbtx, id metadata.EntryID) (metadata.Entry, error) {
	row := q.QueryRowContext(ctx, `
		SELECT entry_id, parent_id, name, kind, object_id, size, mtime, mode, content_type, revision, created_at, updated_at
		FROM entries
		WHERE entry_id = ?
	`, id.String())
	return s.scanEntry(row)
}

// GetByID retrieves a single metadata entry by its ID.
func (s *Store) GetByID(ctx context.Context, id metadata.EntryID) (metadata.Entry, error) {
	return s.getByID(ctx, s.db, id)
}

func (s *Store) getChild(ctx context.Context, q dbtx, parentID metadata.EntryID, name string) (metadata.Entry, error) {
	row := q.QueryRowContext(ctx, `
		SELECT entry_id, parent_id, name, kind, object_id, size, mtime, mode, content_type, revision, created_at, updated_at
		FROM entries
		WHERE parent_id = ? AND name = ?
	`, parentID.String(), name)
	return s.scanEntry(row)
}

// GetChild retrieves a single metadata entry under parentID by its local name.
func (s *Store) GetChild(ctx context.Context, parentID metadata.EntryID, name string) (metadata.Entry, error) {
	return s.getChild(ctx, s.db, parentID, name)
}

// ListChildren returns direct child entries under parentID.
func (s *Store) ListChildren(ctx context.Context, parentID metadata.EntryID) ([]metadata.Entry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT entry_id, parent_id, name, kind, object_id, size, mtime, mode, content_type, revision, created_at, updated_at
		FROM entries
		WHERE parent_id = ?
	`, parentID.String())
	if err != nil {
		return nil, fmt.Errorf("failed to list children: %w", err)
	}
	defer rows.Close()

	var entries []metadata.Entry
	for rows.Next() {
		entry, err := s.scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func isNamespaceUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "UNIQUE constraint failed") &&
		strings.Contains(s, "parent_id") &&
		strings.Contains(s, "name")
}

func (s *Store) validateCreateInvariants(ctx context.Context, q dbtx, entry metadata.Entry) error {
	// If ParentID is specified, verify its existence and that it is a directory.
	if entry.ParentID != nil {
		parent, err := s.getByID(ctx, q, *entry.ParentID)
		if err != nil {
			if errors.Is(err, metadata.ErrEntryNotFound) {
				return fmt.Errorf("%w: parent entry %s does not exist", metadata.ErrEntryConflict, *entry.ParentID)
			}
			return fmt.Errorf("failed to verify parent existence: %w", err)
		}
		if parent.Kind != metadata.EntryKindDirectory {
			return fmt.Errorf("%w: parent entry %s must be a directory, but got %s", metadata.ErrEntryConflict, *entry.ParentID, parent.Kind)
		}

		// namespace collision check
		_, err = s.getChild(ctx, q, *entry.ParentID, entry.Name)
		switch {
		case err == nil:
			return fmt.Errorf("%w: entry %q already exists under parent %s", metadata.ErrEntryConflict, entry.Name, *entry.ParentID)
		case errors.Is(err, metadata.ErrEntryNotFound):
			return nil
		default:
			return fmt.Errorf("failed to verify entry uniqueness: %w", err)
		}
	}
	return nil
}

// Create inserts a new entry into SQLite.
func (s *Store) Create(ctx context.Context, entry metadata.Entry) (metadata.Entry, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return metadata.Entry{}, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	if err := s.validateCreateInvariants(ctx, tx, entry); err != nil {
		return metadata.Entry{}, err
	}

	var parentIDVal sql.NullString
	if entry.ParentID != nil {
		parentIDVal = sql.NullString{String: entry.ParentID.String(), Valid: true}
	}

	var objectIDVal sql.NullString
	if entry.ObjectID != nil {
		objectIDVal = sql.NullString{String: entry.ObjectID.String(), Valid: true}
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO entries (
			entry_id, parent_id, name, kind, object_id, size, mtime, mode, content_type, revision, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		entry.ID.String(),
		parentIDVal,
		entry.Name,
		string(entry.Kind),
		objectIDVal,
		entry.Size,
		entry.Mtime.Format(time.RFC3339),
		entry.Mode,
		entry.ContentType,
		entry.Revision,
		entry.CreatedAt.Format(time.RFC3339),
		entry.UpdatedAt.Format(time.RFC3339),
	)
	if err != nil {
		if isNamespaceUniqueConstraintError(err) {
			parentIDStr := ""
			if entry.ParentID != nil {
				parentIDStr = entry.ParentID.String()
			}
			return metadata.Entry{}, fmt.Errorf("%w: entry %q already exists under parent %s", metadata.ErrEntryConflict, entry.Name, parentIDStr)
		}
		return metadata.Entry{}, fmt.Errorf("%w: failed to create entry: %w", metadata.ErrEntryConflict, err)
	}

	if err := tx.Commit(); err != nil {
		return metadata.Entry{}, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return entry, nil
}

func (s *Store) validateUpdateInvariants(ctx context.Context, q dbtx, entry metadata.Entry) error {
	// 5. Parent existence and parent kind validation
	if entry.ParentID != nil {
		parent, err := s.getByID(ctx, q, *entry.ParentID)
		if err != nil {
			if errors.Is(err, metadata.ErrEntryNotFound) {
				return fmt.Errorf("%w: parent entry %s does not exist", metadata.ErrEntryConflict, *entry.ParentID)
			}
			return fmt.Errorf("failed to verify parent existence: %w", err)
		}
		if parent.Kind != metadata.EntryKindDirectory {
			return fmt.Errorf("%w: parent entry %s must be a directory, but got %s", metadata.ErrEntryConflict, *entry.ParentID, parent.Kind)
		}
	}

	// 6. Ancestor / hierarchy cycle check for directory
	if entry.Kind == metadata.EntryKindDirectory && entry.ParentID != nil {
		currentParentID := *entry.ParentID
		visited := make(map[metadata.EntryID]bool)
		for currentParentID != "root" {
			if currentParentID == entry.ID {
				return fmt.Errorf("%w: hierarchy cycle detected: directory cannot be its own ancestor or child of its descendant", metadata.ErrEntryConflict)
			}
			if visited[currentParentID] {
				return fmt.Errorf("%w: cycle detected in parent ancestry", metadata.ErrEntryConflict)
			}
			visited[currentParentID] = true

			parentEntry, err := s.getByID(ctx, q, currentParentID)
			if err != nil {
				return fmt.Errorf("failed to verify ancestry for %s: %w", currentParentID, err)
			}
			if parentEntry.ParentID == nil {
				break
			}
			currentParentID = *parentEntry.ParentID
		}
	}

	return nil
}

// Update updates an existing entry in SQLite after performing basic invariant validation.
func (s *Store) Update(ctx context.Context, entry metadata.Entry) (metadata.Entry, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return metadata.Entry{}, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Verify target entry existence and current revision for optimistic concurrency check
	existing, err := s.getByID(ctx, tx, entry.ID)
	if err != nil {
		if errors.Is(err, metadata.ErrEntryNotFound) {
			return metadata.Entry{}, metadata.ErrEntryNotFound
		}
		return metadata.Entry{}, fmt.Errorf("failed to verify target entry existence: %w", err)
	}
	if existing.Revision != entry.Revision {
		return metadata.Entry{}, fmt.Errorf("%w: optimistic concurrency conflict: current revision %d, update request revision %d", metadata.ErrEntryConflict, existing.Revision, entry.Revision)
	}

	if err := s.validateUpdateInvariants(ctx, tx, entry); err != nil {
		return metadata.Entry{}, err
	}

	var parentIDVal sql.NullString
	if entry.ParentID != nil {
		parentIDVal = sql.NullString{String: entry.ParentID.String(), Valid: true}
	}

	var objectIDVal sql.NullString
	if entry.ObjectID != nil {
		objectIDVal = sql.NullString{String: entry.ObjectID.String(), Valid: true}
	}

	newRevision := entry.Revision + 1

	res, err := tx.ExecContext(ctx, `
		UPDATE entries SET
			parent_id = ?,
			name = ?,
			kind = ?,
			object_id = ?,
			size = ?,
			mtime = ?,
			mode = ?,
			content_type = ?,
			revision = ?,
			updated_at = ?
		WHERE entry_id = ? AND revision = ?
	`,
		parentIDVal,
		entry.Name,
		string(entry.Kind),
		objectIDVal,
		entry.Size,
		entry.Mtime.Format(time.RFC3339),
		entry.Mode,
		entry.ContentType,
		newRevision,
		time.Now().UTC().Format(time.RFC3339),
		entry.ID.String(),
		entry.Revision,
	)
	if err != nil {
		return metadata.Entry{}, fmt.Errorf("%w: failed to update entry: %w", metadata.ErrEntryConflict, err)
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return metadata.Entry{}, fmt.Errorf("failed to check rows affected: %w", err)
	}
	if rows == 0 {
		return metadata.Entry{}, metadata.ErrEntryConflict
	}

	if err := tx.Commit(); err != nil {
		return metadata.Entry{}, fmt.Errorf("failed to commit transaction: %w", err)
	}

	updatedEntry := entry
	updatedEntry.Revision = newRevision
	updatedEntry.UpdatedAt = time.Now().UTC()

	return updatedEntry, nil
}

func (s *Store) validateMoveInvariants(
	ctx context.Context,
	q dbtx,
	current metadata.Entry,
	parentID metadata.EntryID,
	name string,
) error {
	parent, err := s.getByID(ctx, q, parentID)
	if err != nil {
		if errors.Is(err, metadata.ErrEntryNotFound) {
			return fmt.Errorf("%w: parent entry %s does not exist", metadata.ErrEntryConflict, parentID)
		}
		return fmt.Errorf("failed to verify parent existence: %w", err)
	}
	if parent.Kind != metadata.EntryKindDirectory {
		return fmt.Errorf("%w: parent entry %s must be a directory, but got %s", metadata.ErrEntryConflict, parentID, parent.Kind)
	}

	if current.Kind == metadata.EntryKindDirectory {
		if err := s.ensureMoveDoesNotCreateCycle(ctx, q, current.ID, parentID); err != nil {
			return err
		}
	}

	existing, err := s.getChild(ctx, q, parentID, name)
	switch {
	case err == nil && existing.ID != current.ID:
		return fmt.Errorf("%w: entry %q already exists under parent %s", metadata.ErrEntryConflict, name, parentID)
	case err == nil:
		// No-op for same parent + name is allowed
		return nil
	case errors.Is(err, metadata.ErrEntryNotFound):
		return nil
	default:
		return fmt.Errorf("failed to verify destination namespace: %w", err)
	}
}

func (s *Store) ensureMoveDoesNotCreateCycle(
	ctx context.Context,
	q dbtx,
	entryID metadata.EntryID,
	parentID metadata.EntryID,
) error {
	current := parentID
	visited := make(map[metadata.EntryID]bool)
	for current != "root" {
		if current == entryID {
			return fmt.Errorf("%w: directory cannot be moved under its own descendant (hierarchy cycle)", metadata.ErrEntryConflict)
		}
		if visited[current] {
			return fmt.Errorf("%w: cycle detected in parent ancestry during move", metadata.ErrEntryConflict)
		}
		visited[current] = true

		p, err := s.getByID(ctx, q, current)
		if err != nil {
			return fmt.Errorf("failed to verify ancestry during move: %w", err)
		}
		if p.ParentID == nil {
			break
		}
		current = *p.ParentID
	}
	return nil
}

// Move moves/renames an entry in SQLite.
func (s *Store) Move(ctx context.Context, entry metadata.Entry) (metadata.Entry, error) {
	if entry.ParentID == nil {
		return metadata.Entry{}, fmt.Errorf("%w: parent ID cannot be empty", metadata.ErrEntryConflict)
	}

	copiedParentID := *entry.ParentID

	if entry.ID == copiedParentID {
		return metadata.Entry{}, fmt.Errorf("%w: entry cannot be moved to itself", metadata.ErrEntryConflict)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return metadata.Entry{}, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	current, err := s.getByID(ctx, tx, entry.ID)
	if err != nil {
		if errors.Is(err, metadata.ErrEntryNotFound) {
			return metadata.Entry{}, err
		}
		return metadata.Entry{}, fmt.Errorf("failed to get entry inside transaction: %w", err)
	}

	if current.Revision != entry.Revision {
		return metadata.Entry{}, fmt.Errorf("%w: expected revision %d, got %d", metadata.ErrEntryConflict, entry.Revision, current.Revision)
	}

	if err := s.validateMoveInvariants(ctx, tx, current, copiedParentID, entry.Name); err != nil {
		return metadata.Entry{}, err
	}

	newRevision := entry.Revision + 1
	now := time.Now().UTC()

	res, err := tx.ExecContext(ctx, `
		UPDATE entries SET
			parent_id = ?,
			name = ?,
			revision = ?,
			updated_at = ?
		WHERE entry_id = ? AND revision = ?
	`,
		copiedParentID.String(),
		entry.Name,
		newRevision,
		now.Format(time.RFC3339),
		entry.ID.String(),
		entry.Revision,
	)
	if err != nil {
		if isNamespaceUniqueConstraintError(err) {
			return metadata.Entry{}, fmt.Errorf("%w: entry %q already exists under parent %s", metadata.ErrEntryConflict, entry.Name, copiedParentID)
		}
		return metadata.Entry{}, fmt.Errorf("%w: failed to move entry: %w", metadata.ErrEntryConflict, err)
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return metadata.Entry{}, fmt.Errorf("failed to check rows affected: %w", err)
	}
	if rows == 0 {
		return metadata.Entry{}, metadata.ErrEntryConflict
	}

	if err := tx.Commit(); err != nil {
		return metadata.Entry{}, fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Use the persisted entry as the return-value base.
	// Move only applies ParentID and Name from the requested entry.
	current.ParentID = &copiedParentID
	current.Name = entry.Name
	current.Revision = newRevision
	current.UpdatedAt = now

	return current, nil
}

// Delete deletes an entry from SQLite atomically inside a transaction, preventing non-empty directory deletion.
func (s *Store) Delete(ctx context.Context, id metadata.EntryID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Attempt atomic DELETE of the entry if it's not root and has no children referencing it as parent_id
	res, err := tx.ExecContext(ctx, `
		DELETE FROM entries
		WHERE entry_id = ?
		  AND entry_id != 'root'
		  AND NOT EXISTS (
			  SELECT 1 FROM entries AS child WHERE child.parent_id = entries.entry_id
		  )
	`, id.String())
	if err != nil {
		return fmt.Errorf("failed to delete entry %s: %w", id.String(), err)
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}

	// If successfully deleted, commit and return nil immediately (no pre-queries in the happy path!)
	if rows == 1 {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit transaction: %w", err)
		}
		return nil
	}

	// RowsAffected == 0: Perform additional checks inside the transaction to determine the exact failure reason
	if id == "root" {
		return fmt.Errorf("%w: operation on root entry is forbidden", metadata.ErrEntryConflict)
	}

	// Check if the entry exists
	_, err = s.getByID(ctx, tx, id)
	if err != nil {
		if errors.Is(err, metadata.ErrEntryNotFound) {
			return metadata.ErrEntryNotFound
		}
		return fmt.Errorf("failed to check entry existence: %w", err)
	}

	// The entry exists and is not root, so the DELETE failed solely because child entries exist
	return fmt.Errorf("%w: cannot delete non-empty directory", metadata.ErrEntryConflict)
}

// ReferencedObjectIDs retrieves unique object IDs referenced by active file entries.
func (s *Store) ReferencedObjectIDs(ctx context.Context) ([]content.ID, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT DISTINCT object_id FROM entries WHERE kind = 'file' AND object_id IS NOT NULL")
	if err != nil {
		return nil, fmt.Errorf("failed to query referenced object IDs: %w", err)
	}
	defer rows.Close()

	var ids []content.ID
	for rows.Next() {
		var rawID string
		if err := rows.Scan(&rawID); err != nil {
			return nil, fmt.Errorf("failed to scan object ID: %w", err)
		}
		id, err := content.ParseID(rawID)
		if err != nil {
			return nil, fmt.Errorf("referenced object ID %q is invalid: %w", rawID, err)
		}
		ids = append(ids, id)
	}

	return ids, nil
}

// ListAll lists all entries.
func (s *Store) ListAll(ctx context.Context) ([]metadata.Entry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT entry_id, parent_id, name, kind, object_id, size, mtime, mode, content_type, revision, created_at, updated_at
		FROM entries
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query all entries: %w", err)
	}
	defer rows.Close()

	var entries []metadata.Entry
	for rows.Next() {
		entry, err := s.scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}
