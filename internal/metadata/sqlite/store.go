package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/metadata"
	repositoryformat "github.com/kamichidu/go-nando/internal/repository/format"
	_ "modernc.org/sqlite"
)

// Store implements the metadata.MetadataStore interface using SQLite.
type Store struct {
	db *sql.DB
}

// ErrSchemaVersionUnsupported identifies a database version this adapter
// cannot safely use or migrate.
var ErrSchemaVersionUnsupported = errors.New("unsupported SQLite schema version")

// ErrSchemaUninitialized identifies a database with no application schema.
var ErrSchemaUninitialized = errors.New("SQLite schema is not initialized")

// ErrRepositoryFormatMissing indicates that no Repository Format record was persisted.
var ErrRepositoryFormatMissing = errors.New("repository format metadata is missing")

// NewStore creates a new Store instance backed by SQLite.
func NewStore(dbPath string) (*Store, error) {
	return newStore(dbPath, "rwc")
}

// NewExistingStore opens an existing database without SQLite's create-if-
// missing behavior. It is used by explicit administrative operations.
func NewExistingStore(dbPath string) (*Store, error) {
	return newStore(dbPath, "rw")
}

func newStore(dbPath, mode string) (*Store, error) {
	// Write operations read state before mutating it. Start their transactions
	// with a RESERVED lock so concurrent writers queue before taking a read
	// snapshot that cannot later be upgraded. beginTx retries SQLITE_BUSY with
	// Context-aware waits instead of blocking inside SQLite's busy handler.
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("resolve SQLite database path %q: %w", dbPath, err)
	}
	uri := url.URL{Scheme: "file", Path: absPath}
	query := uri.Query()
	query.Set("mode", mode)
	query.Set("_txlock", "immediate")
	query.Add("_pragma", "foreign_keys=ON")
	uri.RawQuery = query.Encode()
	dsn := uri.String()
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

// Initialize creates the v1 SQLite schema only for a pristine database.
// Repeated initialization of an existing database is rejected.
func (s *Store) Initialize(ctx context.Context, repoFormat repositoryformat.Format) error {
	if err := s.CheckMigrations(ctx); !errors.Is(err, ErrSchemaUninitialized) {
		if err == nil {
			return fmt.Errorf("%w: init creates a new database only", ErrSchemaVersionUnsupported)
		}
		return err
	}
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read SQLite schema version: %w", err)
	}
	var existingObjects int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&existingObjects)
	if err != nil {
		return fmt.Errorf("failed to inspect uninitialized SQLite schema objects: %w", err)
	}
	if version != 0 || existingObjects != 0 {
		return fmt.Errorf("%w: init creates a new database only (user_version=%d, application schema objects=%d)", ErrSchemaVersionUnsupported, version, existingObjects)
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin v1 schema initialization: %w", err)
	}
	defer tx.Rollback()
	// Recheck under the SQLite writer lock so two concurrent init commands
	// cannot both accept the same initially empty database.
	var lockedVersion, lockedObjects int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&lockedVersion); err != nil {
		return fmt.Errorf("read SQLite schema version during initialization: %w", err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&lockedObjects); err != nil {
		return fmt.Errorf("inspect SQLite schema objects during initialization: %w", err)
	}
	if lockedVersion != 0 || lockedObjects != 0 {
		return fmt.Errorf("%w: database became initialized before schema creation", ErrSchemaVersionUnsupported)
	}
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
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", CurrentSchemaVersion)); err != nil {
		return fmt.Errorf("failed to set SQLite schema baseline: %w", err)
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

// CheckMigrations accepts only the formal v1 baseline. Version 0 is accepted
// only for a pristine database so callers can distinguish it from legacy data.
func (s *Store) CheckMigrations(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("failed to read SQLite schema version: %w", err)
	}
	var schemaObjects int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&schemaObjects); err != nil {
		return fmt.Errorf("inspect SQLite schema tables: %w", err)
	}
	if version == 0 {
		if schemaObjects == 0 {
			return ErrSchemaUninitialized
		}
		return fmt.Errorf("%w: database has user_version=0 and %d application schema objects (legacy or unknown schema)", ErrSchemaVersionUnsupported, schemaObjects)
	}
	if version != CurrentSchemaVersion {
		return fmt.Errorf("%w: database is version %d; this release supports version %d", ErrSchemaVersionUnsupported, version, CurrentSchemaVersion)
	}
	var tables int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
		return fmt.Errorf("inspect SQLite schema tables: %w", err)
	}
	if tables != 6 {
		return fmt.Errorf("%w: v1 schema has %d application tables, expected 6", ErrSchemaVersionUnsupported, tables)
	}
	var objects int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&objects); err != nil {
		return fmt.Errorf("inspect SQLite schema objects: %w", err)
	}
	if objects != 6 {
		return fmt.Errorf("%w: v1 schema has %d application schema objects, expected 6", ErrSchemaVersionUnsupported, objects)
	}
	for table, required := range map[string][]string{
		"entries":           {"entry_id", "parent_id", "name", "kind", "object_id", "size", "mtime", "mode", "content_type", "revision", "created_at", "updated_at"},
		"repository_format": {"singleton", "format_version", "content_algorithm", "content_id_format"},
		"transfer_queue":    {"content_id", "enqueued_at"},
		"content_metadata":  {"content_id", "md5"},
		"dead_properties":   {"entry_id", "namespace_uri", "localname", "xml_value"},
		"internal_buffers":  {"buffer_id", "root_entry_id", "expires_at", "fencing", "claimed"},
	} {
		columns, err := s.tableColumns(ctx, table)
		if err != nil {
			return fmt.Errorf("inspect SQLite v1 table %s: %w", table, err)
		}
		for _, column := range required {
			if !columns[column] {
				return fmt.Errorf("%w: v1 table %s is missing column %s", ErrSchemaVersionUnsupported, table, column)
			}
		}
		if table == "dead_properties" {
			if len(columns) != len(required) {
				return fmt.Errorf("%w: v1 dead_properties has unexpected columns", ErrSchemaVersionUnsupported)
			}
			var ddl string
			if err := s.db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name='dead_properties'").Scan(&ddl); err != nil {
				return err
			}
			normalize := func(v string) string {
				v = strings.Join(strings.Fields(strings.ToLower(v)), " ")
				v = strings.ReplaceAll(v, "( ", "(")
				v = strings.ReplaceAll(v, " )", ")")
				v = strings.ReplaceAll(v, ", ", ",")
				return v
			}
			expected := "CREATE TABLE dead_properties (entry_id TEXT NOT NULL REFERENCES entries(entry_id) ON DELETE CASCADE, namespace_uri TEXT NOT NULL, localname TEXT NOT NULL, xml_value TEXT NOT NULL, PRIMARY KEY(entry_id, namespace_uri, localname))"
			if normalize(ddl) != normalize(expected) {
				return fmt.Errorf("%w: dead_properties has an unknown definition", ErrSchemaVersionUnsupported)
			}
		}
		if table == "internal_buffers" {
			if len(columns) != len(required) {
				return fmt.Errorf("%w: v1 internal_buffers has unexpected columns", ErrSchemaVersionUnsupported)
			}
			var ddl string
			if err := s.db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name='internal_buffers'").Scan(&ddl); err != nil {
				return err
			}
			normalize := func(v string) string {
				v = strings.Join(strings.Fields(strings.ToLower(v)), " ")
				v = strings.ReplaceAll(v, "( ", "(")
				v = strings.ReplaceAll(v, " )", ")")
				v = strings.ReplaceAll(v, ", ", ",")
				return v
			}
			expected := "CREATE TABLE internal_buffers (buffer_id TEXT PRIMARY KEY, root_entry_id TEXT NOT NULL UNIQUE REFERENCES entries(entry_id) ON DELETE CASCADE, expires_at INTEGER NOT NULL DEFAULT 0, fencing INTEGER NOT NULL DEFAULT 1, claimed INTEGER NOT NULL DEFAULT 0)"
			if normalize(ddl) != normalize(expected) {
				return fmt.Errorf("%w: internal_buffers has an unknown definition", ErrSchemaVersionUnsupported)
			}
		}
	}
	return nil
}

// CompletePreReleaseV1 completes the exact known four-table or five-table
// pre-release v1 baseline. Callers validate Repository Format and root first.
func (s *Store) CompletePreReleaseV1(ctx context.Context) error {
	var version, tables int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
		return err
	}
	if version != 1 {
		return fmt.Errorf("%w: completion requires v1", ErrSchemaVersionUnsupported)
	}
	if tables == 6 {
		if err := s.CheckMigrations(ctx); err == nil {
			return nil
		}
		cols, err := s.tableColumns(ctx, "internal_buffers")
		if err != nil || len(cols) != 2 || !cols["buffer_id"] || !cols["root_entry_id"] {
			return fmt.Errorf("%w: unknown six-table pre-release schema", ErrSchemaVersionUnsupported)
		}
		var ddl string
		if err := s.db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name='internal_buffers'").Scan(&ddl); err != nil {
			return err
		}
		normalize := func(v string) string {
			v = strings.Join(strings.Fields(strings.ToLower(v)), " ")
			v = strings.ReplaceAll(v, "( ", "(")
			v = strings.ReplaceAll(v, " )", ")")
			v = strings.ReplaceAll(v, ", ", ",")
			return v
		}
		want := "CREATE TABLE internal_buffers (buffer_id TEXT PRIMARY KEY, root_entry_id TEXT NOT NULL UNIQUE REFERENCES entries(entry_id) ON DELETE CASCADE)"
		if normalize(ddl) != normalize(want) {
			return fmt.Errorf("%w: unknown pre-release internal_buffers schema", ErrSchemaVersionUnsupported)
		}
		tx, err := s.beginTx(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, stmt := range []string{
			`ALTER TABLE internal_buffers ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE internal_buffers ADD COLUMN fencing INTEGER NOT NULL DEFAULT 1`,
			`ALTER TABLE internal_buffers ADD COLUMN claimed INTEGER NOT NULL DEFAULT 0`,
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return s.CheckMigrations(ctx)
	}
	if tables != 4 && tables != 5 {
		return fmt.Errorf("%w: pre-release v1 has %d tables", ErrSchemaVersionUnsupported, tables)
	}
	hasDeadProperties := tables == 5
	var objects int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&objects); err != nil {
		return err
	}
	if objects != tables {
		return fmt.Errorf("%w: pre-release v1 has extra schema objects", ErrSchemaVersionUnsupported)
	}
	format, found, err := s.RepositoryFormat(ctx)
	if err != nil {
		return err
	}
	if !found || format != repositoryformat.Current() {
		return fmt.Errorf("%w: legacy Repository Format is invalid", ErrSchemaVersionUnsupported)
	}
	var roots int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM entries WHERE entry_id='root' AND parent_id IS NULL AND name='' AND kind='directory'`).Scan(&roots); err != nil || roots != 1 {
		return fmt.Errorf("%w: legacy root entry is invalid", ErrSchemaVersionUnsupported)
	}
	expectedSQL := map[string]string{
		"entries":           "CREATE TABLE entries (entry_id TEXT PRIMARY KEY, parent_id TEXT NULL, name TEXT NOT NULL, kind TEXT NOT NULL, object_id TEXT NULL, size INTEGER NOT NULL, mtime INTEGER NOT NULL, mode INTEGER NOT NULL, content_type TEXT NOT NULL, revision INTEGER NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, UNIQUE (parent_id, name))",
		"repository_format": "CREATE TABLE repository_format (singleton INTEGER PRIMARY KEY CHECK (singleton = 1), format_version INTEGER NOT NULL, content_algorithm TEXT NOT NULL, content_id_format TEXT NOT NULL)",
		"transfer_queue":    "CREATE TABLE transfer_queue (content_id TEXT PRIMARY KEY, enqueued_at INTEGER NOT NULL)",
		"content_metadata":  "CREATE TABLE content_metadata (content_id TEXT PRIMARY KEY, md5 BLOB NOT NULL CHECK(length(md5) = 16))",
	}
	for name, expected := range expectedSQL {
		var actual string
		if err := s.db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&actual); err != nil {
			return fmt.Errorf("%w: legacy table %s is missing", ErrSchemaVersionUnsupported, name)
		}
		normalize := func(v string) string {
			v = strings.Join(strings.Fields(strings.ToLower(v)), " ")
			v = strings.ReplaceAll(v, "( ", "(")
			v = strings.ReplaceAll(v, " )", ")")
			v = strings.ReplaceAll(v, ", ", ",")
			return v
		}
		if normalize(actual) != normalize(expected) {
			return fmt.Errorf("%w: legacy table %s has an unknown definition (%q != %q)", ErrSchemaVersionUnsupported, name, normalize(actual), normalize(expected))
		}
	}
	for table, required := range map[string][]string{"entries": {"entry_id", "parent_id", "name", "kind", "object_id", "size", "mtime", "mode", "content_type", "revision", "created_at", "updated_at"}, "repository_format": {"singleton", "format_version", "content_algorithm", "content_id_format"}, "transfer_queue": {"content_id", "enqueued_at"}, "content_metadata": {"content_id", "md5"}} {
		cols, err := s.tableColumns(ctx, table)
		if err != nil {
			return err
		}
		if len(cols) != len(required) {
			return fmt.Errorf("%w: legacy table %s has unexpected columns", ErrSchemaVersionUnsupported, table)
		}
		for _, c := range required {
			if !cols[c] {
				return fmt.Errorf("%w: legacy table %s missing %s", ErrSchemaVersionUnsupported, table, c)
			}
		}
	}
	if hasDeadProperties {
		cols, err := s.tableColumns(ctx, "dead_properties")
		if err != nil || len(cols) != 4 || !cols["entry_id"] || !cols["namespace_uri"] || !cols["localname"] || !cols["xml_value"] {
			return fmt.Errorf("%w: pre-release dead_properties columns are unknown", ErrSchemaVersionUnsupported)
		}
		var ddl string
		if err := s.db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name='dead_properties'").Scan(&ddl); err != nil {
			return err
		}
		normalize := func(v string) string {
			v = strings.Join(strings.Fields(strings.ToLower(v)), " ")
			v = strings.ReplaceAll(v, "( ", "(")
			v = strings.ReplaceAll(v, " )", ")")
			v = strings.ReplaceAll(v, ", ", ",")
			return v
		}
		want := "CREATE TABLE dead_properties (entry_id TEXT NOT NULL REFERENCES entries(entry_id) ON DELETE CASCADE, namespace_uri TEXT NOT NULL, localname TEXT NOT NULL, xml_value TEXT NOT NULL, PRIMARY KEY(entry_id, namespace_uri, localname))"
		if normalize(ddl) != normalize(want) {
			return fmt.Errorf("%w: pre-release dead_properties definition is unknown", ErrSchemaVersionUnsupported)
		}
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !hasDeadProperties {
		if _, err := tx.ExecContext(ctx, `CREATE TABLE dead_properties (entry_id TEXT NOT NULL REFERENCES entries(entry_id) ON DELETE CASCADE, namespace_uri TEXT NOT NULL, localname TEXT NOT NULL, xml_value TEXT NOT NULL, PRIMARY KEY(entry_id, namespace_uri, localname))`); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE internal_buffers (buffer_id TEXT PRIMARY KEY, root_entry_id TEXT NOT NULL UNIQUE REFERENCES entries(entry_id) ON DELETE CASCADE, expires_at INTEGER NOT NULL DEFAULT 0, fencing INTEGER NOT NULL DEFAULT 1, claimed INTEGER NOT NULL DEFAULT 0)`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) IsPreReleaseV1Candidate(ctx context.Context) bool {
	var version, tables int
	if s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version) != nil || version != 1 {
		return false
	}
	if s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables) != nil || (tables != 4 && tables != 5 && tables != 6) {
		return false
	}
	var objects int
	if s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&objects) != nil || objects != tables {
		return false
	}
	if tables == 6 {
		cols, err := s.tableColumns(ctx, "internal_buffers")
		if err != nil || len(cols) != 2 || !cols["buffer_id"] || !cols["root_entry_id"] {
			return false
		}
	}
	return true
}

func (s *Store) tableColumns(ctx context.Context, table string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

// Migrate validates the database and applies a migration only from the
// immediately preceding schema. This release is baseline v1 and has no v0
// repository migration, so the only supported operation is a validated v1
// no-op.
func (s *Store) Migrate(ctx context.Context) (int, error) {
	if err := s.CheckMigrations(ctx); err != nil {
		return 0, err
	}
	return CurrentSchemaVersion, nil
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

func (s *Store) GetDeadProperties(ctx context.Context, id metadata.EntryID) ([]metadata.DeadProperty, error) {
	visible, err := s.isPublicEntry(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, metadata.ErrEntryNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT namespace_uri,localname,xml_value FROM dead_properties WHERE entry_id=? ORDER BY namespace_uri,localname`, id.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []metadata.DeadProperty
	for rows.Next() {
		var p metadata.DeadProperty
		p.EntryID = id
		if err := rows.Scan(&p.Namespace, &p.Name, &p.XML); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) UpdateDeadProperties(ctx context.Context, id metadata.EntryID, changes []metadata.DeadPropertyChange) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	visible, err := s.isPublicEntry(ctx, tx, id)
	if err != nil {
		return err
	}
	if !visible {
		return metadata.ErrEntryNotFound
	}
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM entries WHERE entry_id=?", id.String()).Scan(&exists); err != nil {
		return metadata.ErrEntryNotFound
	}
	for _, change := range changes {
		p := change.Property
		if p.Name == "" || p.XML == "" {
			if !change.Remove || p.Name == "" {
				return fmt.Errorf("invalid property value")
			}
		}
		if change.Remove {
			if _, err := tx.ExecContext(ctx, "DELETE FROM dead_properties WHERE entry_id=? AND namespace_uri=? AND localname=?", id.String(), p.Namespace, p.Name); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO dead_properties(entry_id,namespace_uri,localname,xml_value) VALUES(?,?,?,?) ON CONFLICT(entry_id,namespace_uri,localname) DO UPDATE SET xml_value=excluded.xml_value`, id.String(), p.Namespace, p.Name, p.XML); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Root retrieves the root entry of the logical repository.
func (s *Store) Root(ctx context.Context) (metadata.Entry, error) {
	visible, err := s.isPublicEntry(ctx, s.db, "root")
	if err != nil {
		return metadata.Entry{}, err
	}
	if !visible {
		return metadata.Entry{}, metadata.ErrEntryNotFound
	}
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
	visible, err := s.isPublicEntry(ctx, s.db, id)
	if err != nil {
		return metadata.Entry{}, err
	}
	if !visible {
		return metadata.Entry{}, metadata.ErrEntryNotFound
	}
	return s.getByID(ctx, s.db, id)
}

func (s *Store) isPublicEntry(ctx context.Context, q dbtx, id metadata.EntryID) (bool, error) {
	var visible int
	err := q.QueryRowContext(ctx, `WITH RECURSIVE ancestry(entry_id,parent_id) AS (
		SELECT entry_id,parent_id FROM entries WHERE entry_id=?
		UNION ALL
		SELECT parent.entry_id,parent.parent_id FROM entries AS parent JOIN ancestry ON parent.entry_id=ancestry.parent_id
	) SELECT CASE WHEN EXISTS(SELECT 1 FROM ancestry WHERE entry_id='root') AND NOT EXISTS(SELECT 1 FROM ancestry JOIN internal_buffers ON internal_buffers.root_entry_id=ancestry.entry_id) THEN 1 ELSE 0 END`, id.String()).Scan(&visible)
	return visible == 1, err
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
	visible, err := s.isPublicEntry(ctx, s.db, parentID)
	if err != nil || !visible {
		return metadata.Entry{}, metadata.ErrEntryNotFound
	}
	return s.getChild(ctx, s.db, parentID, name)
}

// ListChildren returns direct child entries under parentID.
func (s *Store) ListChildren(ctx context.Context, parentID metadata.EntryID) ([]metadata.Entry, error) {
	visible, err := s.isPublicEntry(ctx, s.db, parentID)
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, metadata.ErrEntryNotFound
	}
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
			return err
		}
		visible, err := s.isPublicEntry(ctx, q, *entry.ParentID)
		if err != nil {
			return err
		}
		if !visible {
			return fmt.Errorf("%w: parent entry %s is not in the public namespace", metadata.ErrEntryConflict, *entry.ParentID)
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
	visible, err := s.isPublicEntry(ctx, tx, entry.ID)
	if err != nil {
		return metadata.Entry{}, err
	}
	if !visible {
		return metadata.Entry{}, metadata.ErrEntryNotFound
	}
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
func (s *Store) CommitPut(ctx context.Context, entry metadata.Entry, enqueueTransfer bool, requireExisting bool) (metadata.Entry, error) {
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
		if requireExisting {
			return metadata.Entry{}, metadata.ErrEntryNotFound
		}
		if entry.ID != "" {
			var exists int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM entries WHERE entry_id=?`, entry.ID.String()).Scan(&exists); err == nil {
				return metadata.Entry{}, metadata.ErrEntryNotFound
			} else if !errors.Is(err, sql.ErrNoRows) {
				return metadata.Entry{}, err
			}
		}
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
		visible, err := s.isPublicEntry(ctx, tx, entry.ID)
		if err != nil {
			return metadata.Entry{}, err
		}
		if !visible {
			return metadata.Entry{}, metadata.ErrEntryNotFound
		}
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
		// database/sql cancels an in-flight SQLite operation by interrupting
		// its connection. Prefer the caller's cancellation cause when that
		// interrupt is what ended writer-lock acquisition.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return metadata.Entry{}, fmt.Errorf("failed to begin transaction: %w", ctxErr)
		}
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
	visible, err := s.isPublicEntry(ctx, tx, entry.ID)
	if err != nil {
		return metadata.Entry{}, err
	}
	if !visible {
		return metadata.Entry{}, metadata.ErrEntryNotFound
	}
	parentVisible, err := s.isPublicEntry(ctx, tx, copiedParentID)
	if err != nil {
		return metadata.Entry{}, err
	}
	if !parentVisible {
		if _, err := s.getByID(ctx, tx, copiedParentID); errors.Is(err, metadata.ErrEntryNotFound) {
			return metadata.Entry{}, fmt.Errorf("%w: destination parent %s does not exist", metadata.ErrEntryConflict, copiedParentID)
		}
		return metadata.Entry{}, fmt.Errorf("%w: destination parent is not in the public namespace", metadata.ErrEntryConflict)
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
	visible, err := s.isPublicEntry(ctx, tx, id)
	if err != nil {
		return err
	}
	if !visible {
		return metadata.ErrEntryNotFound
	}

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
		WITH RECURSIVE visible(entry_id) AS (
			SELECT 'root'
			UNION ALL
			SELECT entries.entry_id FROM entries JOIN visible ON entries.parent_id=visible.entry_id
		)
		SELECT entry_id, parent_id, name, kind, object_id, (SELECT md5 FROM content_metadata WHERE content_id=entries.object_id), size, mtime, mode, content_type, revision, created_at, updated_at
		FROM entries WHERE entry_id IN (SELECT entry_id FROM visible)
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

// DetachTree atomically removes a public subtree root from path lookup and
// registers it as one private, independently reclaimable buffer.
func (s *Store) CreateDetachedRoot(ctx context.Context, entry metadata.Entry, ttl time.Duration) (metadata.DetachedBuffer, error) {
	if entry.ID == "" || entry.ID == "root" || entry.ParentID != nil || entry.Name != "" || entry.Kind != metadata.EntryKindDirectory || entry.Revision == 0 {
		return metadata.DetachedBuffer{}, fmt.Errorf("invalid detached root entry")
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return metadata.DetachedBuffer{}, err
	}
	defer tx.Rollback()
	if err := insertEntry(ctx, tx, entry); err != nil {
		return metadata.DetachedBuffer{}, err
	}
	buffer := metadata.DetachedBuffer{ID: metadata.BufferID(uuid.NewString()), RootID: entry.ID, ExpiresAt: metadata.TimestampFromTime(time.Now().Add(ttl)), Fencing: 1}
	if _, err := tx.ExecContext(ctx, `INSERT INTO internal_buffers(buffer_id,root_entry_id,expires_at,fencing,claimed) VALUES(?,?,?,?,0)`, string(buffer.ID), entry.ID.String(), int64(buffer.ExpiresAt), buffer.Fencing); err != nil {
		return metadata.DetachedBuffer{}, err
	}
	if err := tx.Commit(); err != nil {
		return metadata.DetachedBuffer{}, err
	}
	return buffer, nil
}

func insertEntry(ctx context.Context, tx *sql.Tx, entry metadata.Entry) error {
	var parentID, objectID any
	if entry.ParentID != nil {
		parentID = entry.ParentID.String()
	}
	if entry.ObjectID != nil {
		if err := entry.ObjectID.Validate(); err != nil {
			return err
		}
		objectID = entry.ObjectID.String()
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO entries(entry_id,parent_id,name,kind,object_id,size,mtime,mode,content_type,revision,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, entry.ID.String(), parentID, entry.Name, string(entry.Kind), objectID, entry.Size, int64(entry.Mtime), entry.Mode, entry.ContentType, entry.Revision, int64(entry.CreatedAt), int64(entry.UpdatedAt))
	if err != nil {
		return err
	}
	if entry.ObjectID != nil {
		return setContentMD5(ctx, tx, *entry.ObjectID, entry.MD5)
	}
	return nil
}

func (s *Store) CreateDetachedEntry(ctx context.Context, buffer metadata.BufferID, fence uint64, entry metadata.Entry) (metadata.Entry, error) {
	if entry.ID == "" || entry.ParentID == nil || entry.Name == "" || entry.Revision == 0 || (entry.Kind != metadata.EntryKindFile && entry.Kind != metadata.EntryKindDirectory) {
		return metadata.Entry{}, fmt.Errorf("invalid detached child entry")
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return metadata.Entry{}, err
	}
	defer tx.Rollback()
	var belongs int
	if err := tx.QueryRowContext(ctx, `WITH RECURSIVE tree(entry_id) AS (SELECT root_entry_id FROM internal_buffers WHERE buffer_id=? AND fencing=? AND claimed=0 AND expires_at>? UNION ALL SELECT entries.entry_id FROM entries JOIN tree ON entries.parent_id=tree.entry_id) SELECT EXISTS(SELECT 1 FROM tree WHERE entry_id=?)`, string(buffer), fence, int64(metadata.TimestampFromTime(time.Now())), entry.ParentID.String()).Scan(&belongs); err != nil {
		return metadata.Entry{}, err
	}
	if belongs == 0 {
		return metadata.Entry{}, metadata.ErrEntryNotFound
	}
	if entry.Kind == metadata.EntryKindFile && entry.ObjectID == nil {
		return metadata.Entry{}, fmt.Errorf("detached file entry requires an object ID")
	}
	if entry.Kind == metadata.EntryKindDirectory && entry.ObjectID != nil {
		return metadata.Entry{}, fmt.Errorf("detached directory entry cannot have an object ID")
	}
	if err := insertEntry(ctx, tx, entry); err != nil {
		return metadata.Entry{}, fmt.Errorf("create detached child: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return metadata.Entry{}, err
	}
	return entry, nil
}

func (s *Store) SetDetachedDeadProperties(ctx context.Context, buffer metadata.BufferID, fence uint64, id metadata.EntryID, changes []metadata.DeadPropertyChange) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var belongs int
	if err := tx.QueryRowContext(ctx, `WITH RECURSIVE tree(entry_id) AS (SELECT root_entry_id FROM internal_buffers WHERE buffer_id=? AND fencing=? AND claimed=0 AND expires_at>? UNION ALL SELECT entries.entry_id FROM entries JOIN tree ON entries.parent_id=tree.entry_id) SELECT EXISTS(SELECT 1 FROM tree WHERE entry_id=?)`, string(buffer), fence, int64(metadata.TimestampFromTime(time.Now())), id.String()).Scan(&belongs); err != nil {
		return err
	}
	if belongs == 0 {
		return metadata.ErrEntryNotFound
	}
	for _, change := range changes {
		p := change.Property
		if p.Name == "" || (!change.Remove && p.XML == "") {
			return fmt.Errorf("invalid property value")
		}
		if change.Remove {
			if _, err := tx.ExecContext(ctx, `DELETE FROM dead_properties WHERE entry_id=? AND namespace_uri=? AND localname=?`, id.String(), p.Namespace, p.Name); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `INSERT INTO dead_properties(entry_id,namespace_uri,localname,xml_value) VALUES(?,?,?,?) ON CONFLICT(entry_id,namespace_uri,localname) DO UPDATE SET xml_value=excluded.xml_value`, id.String(), p.Namespace, p.Name, p.XML); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) PublishDetachedRoot(ctx context.Context, buffer metadata.BufferID, fence uint64, parentID metadata.EntryID, name string) (metadata.Entry, error) {
	if name == "" {
		return metadata.Entry{}, metadata.ErrEntryConflict
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return metadata.Entry{}, err
	}
	defer tx.Rollback()
	parentVisible, err := s.isPublicEntry(ctx, tx, parentID)
	if err != nil {
		return metadata.Entry{}, err
	}
	if !parentVisible {
		return metadata.Entry{}, metadata.ErrEntryNotFound
	}
	parent, err := s.getByID(ctx, tx, parentID)
	if err != nil {
		return metadata.Entry{}, err
	}
	if parent.Kind != metadata.EntryKindDirectory {
		return metadata.Entry{}, metadata.ErrEntryConflict
	}
	if _, err := s.getChild(ctx, tx, parentID, name); err == nil {
		return metadata.Entry{}, metadata.ErrEntryConflict
	} else if !errors.Is(err, metadata.ErrEntryNotFound) {
		return metadata.Entry{}, err
	}
	var rootID string
	var claimed int
	if err := tx.QueryRowContext(ctx, `SELECT root_entry_id,claimed FROM internal_buffers WHERE buffer_id=? AND fencing=? AND expires_at>?`, string(buffer), fence, int64(metadata.TimestampFromTime(time.Now().UTC()))).Scan(&rootID, &claimed); err != nil {
		return metadata.Entry{}, metadata.ErrEntryConflict
	}
	if claimed != 0 {
		return metadata.Entry{}, metadata.ErrEntryConflict
	}
	root, err := s.getByID(ctx, tx, metadata.EntryID(rootID))
	if err != nil {
		return metadata.Entry{}, err
	}
	now := metadata.TimestampFromTime(time.Now().UTC())
	res, err := tx.ExecContext(ctx, `UPDATE entries SET parent_id=?,name=?,revision=revision+1,updated_at=? WHERE entry_id=? AND parent_id IS NULL`, parentID.String(), name, int64(now), rootID)
	if err != nil {
		return metadata.Entry{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return metadata.Entry{}, metadata.ErrEntryConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM internal_buffers WHERE buffer_id=?`, string(buffer)); err != nil {
		return metadata.Entry{}, err
	}
	if err := tx.Commit(); err != nil {
		return metadata.Entry{}, err
	}
	root.ParentID, root.Name, root.Revision, root.UpdatedAt = &parentID, name, root.Revision+1, now
	return root, nil
}

func (s *Store) DetachTree(ctx context.Context, id metadata.EntryID, revision uint64, expectedParent metadata.EntryID) (metadata.DetachedBuffer, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return metadata.DetachedBuffer{}, err
	}
	defer tx.Rollback()
	visible, err := s.isPublicEntry(ctx, tx, id)
	if err != nil {
		return metadata.DetachedBuffer{}, err
	}
	if !visible || id == "root" {
		return metadata.DetachedBuffer{}, metadata.ErrEntryNotFound
	}
	entry, err := s.getByID(ctx, tx, id)
	if err != nil {
		return metadata.DetachedBuffer{}, err
	}
	if entry.Revision != revision || entry.ParentID == nil || *entry.ParentID != expectedParent {
		return metadata.DetachedBuffer{}, metadata.ErrEntryConflict
	}
	buffer := metadata.DetachedBuffer{ID: metadata.BufferID(uuid.NewString()), RootID: id, Fencing: 1, ExpiresAt: metadata.TimestampFromTime(time.Now())}
	res, err := tx.ExecContext(ctx, `UPDATE entries SET parent_id=NULL,name='',revision=revision+1,updated_at=? WHERE entry_id=? AND revision=?`, int64(metadata.TimestampFromTime(time.Now().UTC())), id.String(), revision)
	if err != nil {
		return metadata.DetachedBuffer{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return metadata.DetachedBuffer{}, metadata.ErrEntryConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO internal_buffers(buffer_id,root_entry_id,expires_at,fencing,claimed) VALUES(?,?,?,?,0)`, buffer.ID, id.String(), int64(buffer.ExpiresAt), buffer.Fencing); err != nil {
		return metadata.DetachedBuffer{}, err
	}
	if err := tx.Commit(); err != nil {
		return metadata.DetachedBuffer{}, err
	}
	return buffer, nil
}

func (s *Store) DetachedBuffers(ctx context.Context) ([]metadata.DetachedBuffer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT buffer_id,root_entry_id,expires_at,fencing,claimed FROM internal_buffers ORDER BY buffer_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var buffers []metadata.DetachedBuffer
	for rows.Next() {
		var buffer metadata.DetachedBuffer
		var id, root string
		var expires int64
		var claimed int
		if err := rows.Scan(&id, &root, &expires, &buffer.Fencing, &claimed); err != nil {
			return nil, err
		}
		buffer.ID, buffer.RootID, buffer.ExpiresAt, buffer.Claimed = metadata.BufferID(id), metadata.EntryID(root), metadata.Timestamp(expires), claimed != 0
		buffers = append(buffers, buffer)
	}
	return buffers, rows.Err()
}

func (s *Store) ListDetachedChildren(ctx context.Context, bufferID metadata.BufferID, fence uint64, parentID metadata.EntryID) ([]metadata.Entry, error) {
	var belongs int
	if err := s.db.QueryRowContext(ctx, `WITH RECURSIVE tree(entry_id) AS (
		SELECT root_entry_id FROM internal_buffers WHERE buffer_id=? AND fencing=? AND claimed=1
		UNION ALL SELECT entries.entry_id FROM entries JOIN tree ON entries.parent_id=tree.entry_id
	) SELECT EXISTS(SELECT 1 FROM tree WHERE entry_id=?)`, string(bufferID), fence, parentID.String()).Scan(&belongs); err != nil {
		return nil, err
	}
	if belongs == 0 {
		return nil, metadata.ErrEntryNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT entry_id,parent_id,name,kind,object_id,(SELECT md5 FROM content_metadata WHERE content_id=entries.object_id),size,mtime,mode,content_type,revision,created_at,updated_at FROM entries WHERE parent_id=? ORDER BY name`, parentID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var children []metadata.Entry
	for rows.Next() {
		entry, err := s.scanEntry(rows)
		if err != nil {
			return nil, err
		}
		children = append(children, entry)
	}
	return children, rows.Err()
}

func (s *Store) DeleteDetachedEntry(ctx context.Context, bufferID metadata.BufferID, fence uint64, id metadata.EntryID) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var belongs int
	if err := tx.QueryRowContext(ctx, `WITH RECURSIVE tree(entry_id) AS (
		SELECT root_entry_id FROM internal_buffers WHERE buffer_id=? AND fencing=? AND claimed=1
		UNION ALL SELECT entries.entry_id FROM entries JOIN tree ON entries.parent_id=tree.entry_id
	) SELECT EXISTS(SELECT 1 FROM tree WHERE entry_id=?)`, string(bufferID), fence, id.String()).Scan(&belongs); err != nil {
		return err
	}
	if belongs == 0 {
		return metadata.ErrEntryNotFound
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM entries WHERE entry_id=? AND NOT EXISTS(SELECT 1 FROM entries child WHERE child.parent_id=entries.entry_id)`, id.String())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return metadata.ErrEntryConflict
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *Store) RenewDetachedBuffer(ctx context.Context, id metadata.BufferID, fence uint64, ttl time.Duration) (metadata.DetachedBuffer, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return metadata.DetachedBuffer{}, err
	}
	defer tx.Rollback()
	now := metadata.TimestampFromTime(time.Now())
	res, err := tx.ExecContext(ctx, `UPDATE internal_buffers SET expires_at=? WHERE buffer_id=? AND fencing=? AND claimed=0 AND expires_at>?`, int64(now)+int64(ttl), string(id), fence, int64(now))
	if err != nil {
		return metadata.DetachedBuffer{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return metadata.DetachedBuffer{}, metadata.ErrEntryConflict
	}
	var b metadata.DetachedBuffer
	var root string
	var expires int64
	if err := tx.QueryRowContext(ctx, `SELECT buffer_id,root_entry_id,expires_at,fencing,claimed FROM internal_buffers WHERE buffer_id=?`, string(id)).Scan(&id, &root, &expires, &b.Fencing, &b.Claimed); err != nil {
		return metadata.DetachedBuffer{}, err
	}
	b.ID, b.RootID, b.ExpiresAt = id, metadata.EntryID(root), metadata.Timestamp(expires)
	if err := tx.Commit(); err != nil {
		return metadata.DetachedBuffer{}, err
	}
	return b, nil
}

func (s *Store) ClaimExpiredDetachedBuffer(ctx context.Context, id metadata.BufferID, now metadata.Timestamp) (metadata.DetachedBuffer, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return metadata.DetachedBuffer{}, err
	}
	defer tx.Rollback()
	// Claim is durable GC ownership, not another time lease. The daemon holds
	// the repository process lock, so only restart recovery may clear claimed.
	res, err := tx.ExecContext(ctx, `UPDATE internal_buffers SET claimed=1,fencing=fencing+1,expires_at=0 WHERE buffer_id=? AND claimed=0 AND expires_at<=?`, string(id), int64(now))
	if err != nil {
		return metadata.DetachedBuffer{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return metadata.DetachedBuffer{}, metadata.ErrEntryConflict
	}
	var b metadata.DetachedBuffer
	var root string
	var expires int64
	if err := tx.QueryRowContext(ctx, `SELECT buffer_id,root_entry_id,expires_at,fencing,claimed FROM internal_buffers WHERE buffer_id=?`, string(id)).Scan(&id, &root, &expires, &b.Fencing, &b.Claimed); err != nil {
		return metadata.DetachedBuffer{}, err
	}
	b.ID, b.RootID, b.ExpiresAt = id, metadata.EntryID(root), metadata.Timestamp(expires)
	if err := tx.Commit(); err != nil {
		return metadata.DetachedBuffer{}, err
	}
	return b, nil
}

func (s *Store) ExpireDetachedBuffersAfterRestart(ctx context.Context, now metadata.Timestamp) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE internal_buffers SET expires_at=?,fencing=fencing+1,claimed=0`, int64(now)); err != nil {
		return err
	}
	return tx.Commit()
}
