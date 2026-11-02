package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/metadata"
	repositoryformat "github.com/kamichidu/go-nando/internal/repository/format"
	_ "modernc.org/sqlite"
)

// Store implements the metadata.MetadataStore interface using SQLite.
type Store struct {
	db *sql.DB
}

// ErrMigrationsUnsupported identifies a SQLite schema history this adapter
// cannot safely apply or use.
var ErrMigrationsUnsupported = errors.New("unsupported SQLite schema migrations")

// ErrMigrationsUninitialized identifies a database without a migration ledger.
var ErrMigrationsUninitialized = errors.New("SQLite schema migrations are not initialized")

// ErrRepositoryFormatMissing indicates that no Repository Format record was persisted.
var ErrRepositoryFormatMissing = errors.New("repository format metadata is missing")

// NewStore creates a new Store instance backed by SQLite.
func NewStore(dbPath string) (*Store, error) {
	// Write operations read state before mutating it. Start their transactions
	// with a RESERVED lock so concurrent writers queue before taking a read
	// snapshot that cannot later be upgraded. beginTx retries SQLITE_BUSY with
	// Context-aware waits instead of blocking inside SQLite's busy handler.
	dsn := dbPath + "?_txlock=immediate&_pragma=foreign_keys%3dON"
	if strings.Contains(dbPath, "?") {
		dsn = dbPath + "&_txlock=immediate&_pragma=foreign_keys%3dON"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open SQLite database at %q: %w", dbPath, err)
	}

	// Limit to 1 open connection to prevent write-lock/busy contention in SQLite.
	db.SetMaxOpenConns(1)

	return &Store{db: db}, nil
}

func (s *Store) beginTx(ctx context.Context) (*sql.Tx, error) {
	const retryDelay = 5 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err == nil {
			return tx, nil
		}
		var sqliteErr interface{ Code() int }
		if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != 5 { // SQLITE_BUSY
			return nil, err
		}
		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// Close closes the underlying SQLite database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Initialize creates the v1 SQLite schema for a new database and persists the
// supplied Repository Format record in the same transaction.
func (s *Store) Initialize(ctx context.Context, repoFormat repositoryformat.Format) error {
	if err := s.CheckMigrations(ctx); err == nil {
		_, found, err := s.RepositoryFormat(ctx)
		if err != nil {
			return err
		}
		if !found {
			return ErrRepositoryFormatMissing
		}
		return nil
	} else if !errors.Is(err, ErrMigrationsUninitialized) {
		return err
	}
	var existingTables int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&existingTables)
	if err != nil {
		return fmt.Errorf("failed to inspect uninitialized SQLite database: %w", err)
	}
	if existingTables != 0 {
		return fmt.Errorf("%w: existing database has tables but no migration history", ErrMigrationsUnsupported)
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin v1 schema initialization: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, V1Schema); err != nil {
		return fmt.Errorf("failed to create SQLite v1 schema: %w", err)
	}
	now := metadata.TimestampFromTime(time.Now().UTC())
	if err := insertRoot(ctx, tx, now); err != nil {
		return err
	}
	if err := writeRepositoryFormat(ctx, tx, repoFormat); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)", CurrentSchemaVersion, int64(now)); err != nil {
		return fmt.Errorf("failed to record SQLite schema baseline: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit SQLite schema migration: %w", err)
	}
	return nil
}

func writeRepositoryFormat(ctx context.Context, tx *sql.Tx, repoFormat repositoryformat.Format) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO repository_format
		(singleton, format_version, content_algorithm, content_id_format)
		VALUES (1, ?, ?, ?)`, repoFormat.Version, repoFormat.ContentAlgorithm, repoFormat.ContentIDFormat)
	if err != nil {
		return fmt.Errorf("failed to persist Repository Format metadata: %w", err)
	}
	return nil
}

func insertRoot(ctx context.Context, tx *sql.Tx, now metadata.Timestamp) error {
	// Root creation is part of the initial v1 schema transaction.
	_, err := tx.ExecContext(ctx, `
		INSERT INTO entries (
			entry_id, parent_id, name, kind, object_id, size, mtime, mode, content_type, revision, created_at, updated_at
		) VALUES (
			'root', NULL, '', 'directory', NULL, 0, ?, ?, '', 1, ?, ?
		)
	`, int64(now), 493, int64(now), int64(now))
	if err != nil {
		return fmt.Errorf("failed to create root entry: %w", err)
	}
	return nil
}

// RepositoryFormat reads the raw persisted Repository Format values. Policy
// validation is deliberately performed by the Repository composition root.
func (s *Store) RepositoryFormat(ctx context.Context) (repositoryformat.Format, bool, error) {
	var result repositoryformat.Format
	err := s.db.QueryRowContext(ctx, `SELECT format_version, content_algorithm, content_id_format
		FROM repository_format WHERE singleton=1`).Scan(&result.Version, &result.ContentAlgorithm, &result.ContentIDFormat)
	if errors.Is(err, sql.ErrNoRows) {
		return repositoryformat.Format{}, false, nil
	}
	if err != nil {
		return repositoryformat.Format{}, false, fmt.Errorf("failed to read Repository Format metadata: %w", err)
	}
	return result, true, nil
}

// CheckMigrations accepts only the formal v1 baseline. Pre-v1 schema histories
// are rejected without attempting an in-place migration.
func (s *Store) CheckMigrations(ctx context.Context) error {
	var ledgerExists int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'").Scan(&ledgerExists); err != nil {
		return fmt.Errorf("failed to inspect SQLite migration ledger: %w", err)
	}
	if ledgerExists == 0 {
		var existingTables int
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&existingTables); err != nil {
			return fmt.Errorf("failed to inspect database without migration ledger: %w", err)
		}
		if existingTables != 0 {
			return fmt.Errorf("%w: database has tables but no migration history", ErrMigrationsUnsupported)
		}
		return ErrMigrationsUninitialized
	}
	var count, minVersion, maxVersion int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*), coalesce(min(version), 0), coalesce(max(version), 0) FROM schema_migrations").Scan(&count, &minVersion, &maxVersion); err != nil {
		return fmt.Errorf("failed to read SQLite migration ledger: %w", err)
	}
	if count != 1 || minVersion != CurrentSchemaVersion || maxVersion != CurrentSchemaVersion {
		return fmt.Errorf("%w: found %d migration records from version %d through %d; this release accepts only the v1 baseline (version %d)", ErrMigrationsUnsupported, count, minVersion, maxVersion, CurrentSchemaVersion)
	}
	var tables int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
		return fmt.Errorf("inspect SQLite schema baseline: %w", err)
	}
	if tables != 5 { // entries, repository_format, schema_migrations, transfer_queue, content_metadata
		return fmt.Errorf("%w: v1 schema has %d application tables, expected 5", ErrMigrationsUnsupported, tables)
	}
	return nil
}

func (s *Store) scanEntry(scanner interface{ Scan(dest ...any) error }) (metadata.Entry, error) {
	var entry metadata.Entry
	var idStr string
	var parentIDStr sql.NullString
	var objectIDStr sql.NullString
	var objectMD5 []byte
	var mtimeVal, createdAtVal, updatedAtVal int64
	var kindStr string

	err := scanner.Scan(
		&idStr,
		&parentIDStr,
		&entry.Name,
		&kindStr,
		&objectIDStr,
		&objectMD5,
		&entry.Size,
		&mtimeVal,
		&entry.Mode,
		&entry.ContentType,
		&entry.Revision,
		&createdAtVal,
		&updatedAtVal,
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
	entry.MD5 = append([]byte(nil), objectMD5...)

	if objectIDStr.Valid && objectIDStr.String != "" {
		oid, err := content.ParseID(objectIDStr.String)
		if err != nil {
			return metadata.Entry{}, fmt.Errorf("failed to parse object ID %q: %w", objectIDStr.String, err)
		}
		entry.ObjectID = &oid
	}

	entry.Mtime = metadata.Timestamp(mtimeVal)
	entry.CreatedAt = metadata.Timestamp(createdAtVal)
	entry.UpdatedAt = metadata.Timestamp(updatedAtVal)

	return entry, nil
}

func setContentMD5(ctx context.Context, tx dbtx, id content.ID, sum []byte) error {
	if len(sum) == 0 {
		return nil
	}
	if len(sum) != 16 {
		return fmt.Errorf("content ID %s requires 16-byte MD5 metadata", id)
	}
	var existing []byte
	err := tx.QueryRowContext(ctx, `SELECT md5 FROM content_metadata WHERE content_id=?`, id.String()).Scan(&existing)
	if err == nil {
		if !bytes.Equal(existing, sum) {
			return fmt.Errorf("conflicting MD5 metadata for Content ID %s", id)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read Content ID MD5 metadata: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO content_metadata(content_id,md5) VALUES(?,?)`, id.String(), sum); err != nil {
		return fmt.Errorf("store Content ID MD5 metadata: %w", err)
	}
	return nil
}

// ContentMD5 returns integrity metadata keyed by immutable Content ID.
func (s *Store) ContentMD5(ctx context.Context, id content.ID) ([]byte, error) {
	var sum []byte
	if err := s.db.QueryRowContext(ctx, `SELECT md5 FROM content_metadata WHERE content_id=?`, id.String()).Scan(&sum); err != nil {
		return nil, fmt.Errorf("read Content ID MD5 metadata: %w", err)
	}
	return append([]byte(nil), sum...), nil
}

// Root retrieves the root entry of the logical repository.
func (s *Store) Root(ctx context.Context) (metadata.Entry, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT entry_id, parent_id, name, kind, object_id, (SELECT md5 FROM content_metadata WHERE content_id=entries.object_id), size, mtime, mode, content_type, revision, created_at, updated_at
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
		SELECT entry_id, parent_id, name, kind, object_id, (SELECT md5 FROM content_metadata WHERE content_id=entries.object_id), size, mtime, mode, content_type, revision, created_at, updated_at
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
		SELECT entry_id, parent_id, name, kind, object_id, (SELECT md5 FROM content_metadata WHERE content_id=entries.object_id), size, mtime, mode, content_type, revision, created_at, updated_at
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
		SELECT entry_id, parent_id, name, kind, object_id, (SELECT md5 FROM content_metadata WHERE content_id=entries.object_id), size, mtime, mode, content_type, revision, created_at, updated_at
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
	if entry.ObjectID != nil {
		if err := entry.ObjectID.Validate(); err != nil {
			return metadata.Entry{}, fmt.Errorf("invalid object ID: %w", err)
		}
	}
	tx, err := s.beginTx(ctx)
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
		int64(entry.Mtime),
		entry.Mode,
		entry.ContentType,
		entry.Revision,
		int64(entry.CreatedAt),
		int64(entry.UpdatedAt),
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
	if entry.ObjectID != nil {
		if err := setContentMD5(ctx, tx, *entry.ObjectID, entry.MD5); err != nil {
			return metadata.Entry{}, err
		}
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
	if entry.ObjectID != nil {
		if err := entry.ObjectID.Validate(); err != nil {
			return metadata.Entry{}, fmt.Errorf("invalid object ID: %w", err)
		}
	}
	tx, err := s.beginTx(ctx)
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
	now := metadata.TimestampFromTime(time.Now().UTC())

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
		int64(entry.Mtime),
		entry.Mode,
		entry.ContentType,
		newRevision,
		int64(now),
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
	if entry.ObjectID != nil {
		if err := setContentMD5(ctx, tx, *entry.ObjectID, entry.MD5); err != nil {
			return metadata.Entry{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return metadata.Entry{}, fmt.Errorf("failed to commit transaction: %w", err)
	}

	updatedEntry := entry
	updatedEntry.Revision = newRevision
	updatedEntry.UpdatedAt = now

	return updatedEntry, nil
}

// CommitPut atomically creates or updates a file Entry and optionally enqueues
// its content ID for asynchronous delivery. It is intentionally one semantic
// MetadataStore operation so backends can preserve the same atomic boundary.
func (s *Store) CommitPut(ctx context.Context, entry metadata.Entry, enqueueTransfer bool) (metadata.Entry, error) {
	if entry.Kind != metadata.EntryKindFile || entry.ObjectID == nil {
		return metadata.Entry{}, fmt.Errorf("%w: Put commit requires a file with content ID", metadata.ErrEntryConflict)
	}
	if err := entry.ObjectID.Validate(); err != nil {
		return metadata.Entry{}, fmt.Errorf("invalid object ID: %w", err)
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return metadata.Entry{}, fmt.Errorf("begin Put metadata transaction: %w", err)
	}
	defer tx.Rollback()

	existing, getErr := s.getByID(ctx, tx, entry.ID)
	if errors.Is(getErr, metadata.ErrEntryNotFound) {
		if err := s.validateCreateInvariants(ctx, tx, entry); err != nil {
			return metadata.Entry{}, err
		}
		var parentID sql.NullString
		if entry.ParentID != nil {
			parentID = sql.NullString{String: entry.ParentID.String(), Valid: true}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO entries (entry_id,parent_id,name,kind,object_id,size,mtime,mode,content_type,revision,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			entry.ID.String(), parentID, entry.Name, string(entry.Kind), entry.ObjectID.String(), entry.Size, int64(entry.Mtime), entry.Mode, entry.ContentType, entry.Revision, int64(entry.CreatedAt), int64(entry.UpdatedAt))
		if err != nil {
			return metadata.Entry{}, fmt.Errorf("%w: failed to create Put Entry: %w", metadata.ErrEntryConflict, err)
		}
		if err := setContentMD5(ctx, tx, *entry.ObjectID, entry.MD5); err != nil {
			return metadata.Entry{}, err
		}
	} else if getErr != nil {
		return metadata.Entry{}, fmt.Errorf("read Put target Entry: %w", getErr)
	} else {
		if existing.Kind != metadata.EntryKindFile {
			return metadata.Entry{}, fmt.Errorf("%w: cannot replace directory with file", metadata.ErrEntryConflict)
		}
		if existing.Revision != entry.Revision {
			return metadata.Entry{}, fmt.Errorf("%w: expected revision %d, found %d", metadata.ErrEntryConflict, entry.Revision, existing.Revision)
		}
		if err := s.validateUpdateInvariants(ctx, tx, entry); err != nil {
			return metadata.Entry{}, err
		}
		newRevision := entry.Revision + 1
		now := metadata.TimestampFromTime(time.Now().UTC())
		res, err := tx.ExecContext(ctx, `UPDATE entries SET object_id=?,size=?,mtime=?,mode=?,content_type=?,revision=?,updated_at=? WHERE entry_id=? AND revision=?`,
			entry.ObjectID.String(), entry.Size, int64(entry.Mtime), entry.Mode, entry.ContentType, newRevision, int64(now), entry.ID.String(), entry.Revision)
		if err != nil {
			return metadata.Entry{}, fmt.Errorf("update Put Entry: %w", err)
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return metadata.Entry{}, err
		}
		if rows != 1 {
			return metadata.Entry{}, metadata.ErrEntryConflict
		}
		if err := setContentMD5(ctx, tx, *entry.ObjectID, entry.MD5); err != nil {
			return metadata.Entry{}, err
		}
		entry.Revision, entry.UpdatedAt = newRevision, now
	}
	if enqueueTransfer {
		if _, err := tx.ExecContext(ctx, `INSERT INTO transfer_queue(content_id,enqueued_at) VALUES(?,?) ON CONFLICT(content_id) DO NOTHING`, entry.ObjectID.String(), int64(metadata.TimestampFromTime(time.Now().UTC()))); err != nil {
			return metadata.Entry{}, fmt.Errorf("enqueue content transfer: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return metadata.Entry{}, fmt.Errorf("commit Put metadata: %w", err)
	}
	return entry, nil
}

// PendingTransfers lists distinct content IDs whose remote delivery is pending.
func (s *Store) PendingTransfers(ctx context.Context) ([]content.ID, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT content_id FROM transfer_queue ORDER BY content_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []content.ID
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		id, err := content.ParseID(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid transfer queue content ID %q: %w", raw, err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// OldestPendingTransfer returns the durable enqueue time of the oldest queue row.
func (s *Store) OldestPendingTransfer(ctx context.Context) (time.Time, bool, error) {
	var raw int64
	err := s.db.QueryRowContext(ctx, "SELECT enqueued_at FROM transfer_queue ORDER BY enqueued_at LIMIT 1").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return metadata.Timestamp(raw).Time(), true, nil
}

// CompleteTransfer removes a content-ID obligation after delivery confirmation.
func (s *Store) CompleteTransfer(ctx context.Context, id content.ID) error {
	if err := id.Validate(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM transfer_queue WHERE content_id = ?", id.String())
	return err
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

	tx, err := s.beginTx(ctx)
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
	now := metadata.TimestampFromTime(time.Now().UTC())

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
		int64(now),
		entry.ID.String(),
		entry.Revision,
	)
	if err != nil {
		if isNamespaceUniqueConstraintError(err) {
			return metadata.Entry{}, fmt.Errorf("%w: entry %q already exists under parent %s", metadata.ErrEntryConflict, entry.Name, copiedParentID)
		}
		return metadata.Entry{}, fmt.Errorf("failed to move entry: %w", err)
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
	tx, err := s.beginTx(ctx)
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
		SELECT entry_id, parent_id, name, kind, object_id, (SELECT md5 FROM content_metadata WHERE content_id=entries.object_id), size, mtime, mode, content_type, revision, created_at, updated_at
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
