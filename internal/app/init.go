package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	coordinatorsqlite "github.com/kamichidu/go-nando/internal/coordinator/sqlite"
	"github.com/kamichidu/go-nando/internal/metadata/sqlite"
	"github.com/kamichidu/go-nando/internal/repository"
	repositoryformat "github.com/kamichidu/go-nando/internal/repository/format"
	"golang.org/x/sys/unix"
)

// InitRepository initializes the directory structure and schema for a new nando repository.
func InitRepository(ctx context.Context, repoDir string) error {
	return InitRepositoryWithSettings(ctx, repoDir, repository.DefaultSettings())
}

// InitRepositoryWithSettings initializes a repository with its delivery settings.
// Existing repositories are never silently reconfigured.
func InitRepositoryWithSettings(ctx context.Context, repoDir string, settings repository.Settings) (retErr error) {
	if err := settings.Validate(); err != nil {
		return fmt.Errorf("invalid repository settings: %w", err)
	}
	// Create the root first; all subsequent steps are the same resumable sequence
	// used for an existing partial initialization.
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		return fmt.Errorf("failed to create repository root directory: %w", err)
	}
	// Serialize provisioning across processes before inspecting or creating any
	// repository component. The lock file is stable across path aliases and is
	// released automatically after crashes, so interrupted init remains resumable.
	initLock, err := os.Open(repoDir)
	if err != nil {
		return fmt.Errorf("open repository initialization lock: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, initLock.Close()) }()
	if err := unix.Flock(int(initLock.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("lock repository initialization: %w", err)
	}
	defer func() {
		if err := unix.Flock(int(initLock.Fd()), unix.LOCK_UN); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("unlock repository initialization: %w", err))
		}
	}()
	if err := validateInitRoot(repoDir); err != nil {
		return err
	}
	dbPath := filepath.Join(repoDir, "metadata.sqlite")
	settingsPath := filepath.Join(repoDir, "repository.json")
	settingsLoaded := false
	if _, err := os.Stat(settingsPath); err == nil {
		persisted, err := LoadRepositorySettings(repoDir)
		if err != nil {
			return fmt.Errorf("unsupported or invalid repository settings; existing data was left unchanged: %w", err)
		}
		if settings != repository.DefaultSettings() && settings != persisted {
			return fmt.Errorf("existing repository settings cannot be changed by init")
		}
		settings = persisted
		settingsLoaded = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect repository settings path: %w", err)
	} else if _, statErr := os.Stat(dbPath); statErr == nil {
		store, openErr := sqlite.NewExistingStore(dbPath)
		if openErr != nil {
			return fmt.Errorf("repository settings are missing and existing metadata state is unknown; existing data was left unchanged: %w", openErr)
		}
		checkErr := store.CheckMigrations(ctx)
		closeErr := store.Close()
		if !errors.Is(checkErr, sqlite.ErrSchemaUninitialized) {
			return errors.Join(
				fmt.Errorf("%w: repository.json is missing while initialized metadata.sqlite exists; repository settings are unknown and existing data was left unchanged", repositoryformat.ErrUnsupported),
				checkErr,
				closeErr,
			)
		}
		if closeErr != nil {
			return fmt.Errorf("close metadata store while checking missing settings: %w", closeErr)
		}
		for _, name := range []string{"objects", "temp"} {
			path := filepath.Join(repoDir, name)
			info, statErr := os.Lstat(path)
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			if statErr != nil {
				return fmt.Errorf("inspect %s while checking missing settings: %w", name, statErr)
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%w: %s is not a regular directory while repository settings are missing", repositoryformat.ErrUnsupported, name)
			}
			children, readErr := os.ReadDir(path)
			if readErr != nil {
				return fmt.Errorf("inspect %s while checking missing settings: %w", name, readErr)
			}
			if len(children) != 0 {
				return fmt.Errorf("%w: repository.json is missing and %s contains data; existing repository settings are unknown and data was left unchanged", repositoryformat.ErrUnsupported, name)
			}
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect metadata database path: %w", statErr)
	}
	dbExists := false
	if info, err := os.Stat(dbPath); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: metadata.sqlite is not a regular file", repositoryformat.ErrUnsupported)
		}
		dbExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect metadata database path: %w", err)
	}
	if !dbExists {
		entries, err := os.ReadDir(repoDir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			name := entry.Name()
			isSettingsTemp := strings.HasPrefix(name, ".repository.json-") && strings.HasSuffix(name, ".tmp")
			isPublishedSettings := name == "repository.json" && settingsLoaded
			if name != "objects" && name != "temp" && !isSettingsTemp && !isPublishedSettings {
				return fmt.Errorf("%w: repository state without metadata.sqlite is not a recognized initialization state", repositoryformat.ErrUnsupported)
			}
			if name == "objects" || name == "temp" {
				path := filepath.Join(repoDir, name)
				info, err := os.Lstat(path)
				if err != nil {
					return fmt.Errorf("inspect %s without metadata.sqlite: %w", name, err)
				}
				if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("%w: %s without metadata.sqlite is not a regular directory", repositoryformat.ErrUnsupported, name)
				}
				children, err := os.ReadDir(path)
				if err != nil {
					return fmt.Errorf("inspect %s without metadata.sqlite: %w", name, err)
				}
				if len(children) != 0 {
					return fmt.Errorf("%w: %s contains data without metadata.sqlite; existing data was left unchanged", repositoryformat.ErrUnsupported, name)
				}
			}
		}
	}
	coordPath := filepath.Join(repoDir, "coordination.db")
	if info, err := os.Stat(coordPath); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("%w: coordination.db is not a regular file", repositoryformat.ErrUnsupported)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := validateCoordinationDatabase(coordPath); err != nil {
		return err
	}
	// Existing databases are accepted only when they are a complete supported
	// v1 schema with the expected format and root entry. SQLite schema creation
	// itself remains transactional and is attempted only for an empty database.
	var store *sqlite.Store
	if dbExists {
		store, err = sqlite.NewExistingStore(dbPath)
	} else {
		store, err = sqlite.NewStore(dbPath)
	}
	if err != nil {
		return fmt.Errorf("failed to open metadata store: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, store.Close()) }()
	repoFormat := repositoryformat.Current()
	if err := repoFormat.Validate(); err != nil {
		return err
	}
	if err := store.CheckMigrations(ctx); errors.Is(err, sqlite.ErrSchemaUninitialized) {
		// Publish the selected settings before initializing Metadata. If the
		// process stops after this point, a retry can recover the same settings
		// rather than guessing from an initialized database with no settings.
		if err := removeRepositorySettingsTemps(repoDir); err != nil {
			return err
		}
		if _, err := os.Lstat(settingsPath); errors.Is(err, os.ErrNotExist) {
			if err := publishRepositorySettings(repoDir, settings); err != nil {
				return err
			}
		} else if err != nil {
			return fmt.Errorf("inspect repository settings path: %w", err)
		}
		if err := store.Initialize(ctx, repoFormat); err != nil {
			if dbExists {
				return fmt.Errorf("initialize empty metadata database: %w", err)
			}
			return fmt.Errorf("failed to initialize metadata schema: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("existing metadata database is not a supported initialized repository: %w", err)
	} else {
		if !dbExists {
			return fmt.Errorf("metadata database appeared during initialization")
		}
		persistedFormat, found, err := store.RepositoryFormat(ctx)
		if err != nil {
			return err
		}
		if !found || persistedFormat != repoFormat {
			return fmt.Errorf("%w: metadata database has missing or incompatible repository format", repositoryformat.ErrUnsupported)
		}
		root, err := store.GetByID(ctx, "root")
		if err != nil || root.Name != "" || root.Kind != "directory" || root.ParentID != nil {
			return fmt.Errorf("%w: metadata database has an invalid root entry: %v", repositoryformat.ErrUnsupported, err)
		}
		if err := removeRepositorySettingsTemps(repoDir); err != nil {
			return err
		}
	}
	// Complete each remaining durable component idempotently after validation.
	objectsDir := filepath.Join(repoDir, "objects")
	if err := ensureInitDirectory(objectsDir); err != nil {
		return fmt.Errorf("failed to create objects directory: %w", err)
	}
	tempDir := filepath.Join(repoDir, "temp")
	if err := ensureInitDirectory(tempDir); err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}

	coordinator, err := coordinatorsqlite.Open(coordPath)
	if err != nil {
		return fmt.Errorf("failed to open repository coordinator: %w", err)
	}
	if err := coordinator.Exclusive(ctx, func() error { return nil }); err != nil {
		return errors.Join(fmt.Errorf("initialize repository coordinator: %w", err), coordinator.Close())
	}
	if err := coordinator.Close(); err != nil {
		return fmt.Errorf("close repository coordinator: %w", err)
	}

	if _, err := os.Lstat(settingsPath); err != nil {
		return fmt.Errorf("inspect repository settings path: %w", err)
	}
	return nil
}

func publishRepositorySettings(repoDir string, settings repository.Settings) (retErr error) {
	tmp, err := os.CreateTemp(repoDir, ".repository.json-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary repository settings: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err := os.Remove(tmpName); err != nil && !errors.Is(err, os.ErrNotExist) {
			retErr = errors.Join(retErr, fmt.Errorf("remove temporary repository settings: %w", err))
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return errors.Join(fmt.Errorf("set repository settings permissions: %w", err), tmp.Close())
	}
	if err := json.NewEncoder(tmp).Encode(settings); err != nil {
		return errors.Join(fmt.Errorf("write temporary repository settings: %w", err), tmp.Close())
	}
	if err := tmp.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync temporary repository settings: %w", err), tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary repository settings: %w", err)
	}
	// Link publishes atomically and fails if the destination already exists.
	// Both paths are on the same filesystem because the temp file is in repoDir.
	if err := os.Link(tmpName, filepath.Join(repoDir, "repository.json")); err != nil {
		return fmt.Errorf("publish repository settings without replacement: %w", err)
	}
	dir, err := os.Open(repoDir)
	if err != nil {
		return fmt.Errorf("open repository directory for sync: %w", err)
	}
	if err := errors.Join(dir.Sync(), dir.Close()); err != nil {
		return fmt.Errorf("sync repository settings directory: %w", err)
	}
	return nil
}

func removeRepositorySettingsTemps(repoDir string) error {
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		return fmt.Errorf("inspect temporary repository settings: %w", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".repository.json-") || !strings.HasSuffix(entry.Name(), ".tmp") {
			continue
		}
		path := filepath.Join(repoDir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: temporary repository settings path is not a regular file", repositoryformat.ErrUnsupported)
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove interrupted temporary repository settings: %w", err)
		}
	}
	return nil
}

func validateInitRoot(repoDir string) error {
	allowed := map[string]bool{"metadata.sqlite": true, "coordination.db": true, "objects": true, "temp": true, "repository.json": true, ".init.lock": true}
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		return fmt.Errorf("inspect repository directory: %w", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".repository.json-") && strings.HasSuffix(entry.Name(), ".tmp") {
			info, err := os.Lstat(filepath.Join(repoDir, entry.Name()))
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%w: temporary repository settings path is not a regular file", repositoryformat.ErrUnsupported)
			}
			continue
		}
		if !allowed[entry.Name()] {
			return fmt.Errorf("%w: unknown repository path %q; existing data was left unchanged", repositoryformat.ErrUnsupported, entry.Name())
		}
		info, err := os.Lstat(filepath.Join(repoDir, entry.Name()))
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: repository path %q is a symlink", repositoryformat.ErrUnsupported, entry.Name())
		}
	}
	return nil
}

func validateCoordinationDatabase(path string) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	var tables int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
		return fmt.Errorf("inspect repository coordinator database: %w", err)
	}
	if tables == 0 {
		var objects int
		if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&objects); err != nil {
			return err
		}
		if objects != 0 {
			return fmt.Errorf("%w: coordination database has schema objects without resource_locks", repositoryformat.ErrUnsupported)
		}
		return nil
	}
	var objects int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type IN ('table','index','trigger','view') AND name NOT LIKE 'sqlite_%'").Scan(&objects); err != nil {
		return fmt.Errorf("inspect repository coordinator objects: %w", err)
	}
	if objects != 1 {
		return fmt.Errorf("%w: coordination database has unknown schema objects", repositoryformat.ErrUnsupported)
	}
	var name string
	if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&name); err != nil || name != "resource_locks" {
		return fmt.Errorf("%w: coordination database has an unknown schema", repositoryformat.ErrUnsupported)
	}
	rows, err := db.Query("PRAGMA table_info(resource_locks)")
	if err != nil {
		return err
	}
	defer rows.Close()
	type column struct {
		typ              string
		notNull, primary int
		defaultValue     sql.NullString
	}
	columns := map[string]column{}
	for rows.Next() {
		var cid, notNull, primary int
		var col, typ string
		var def sql.NullString
		if err := rows.Scan(&cid, &col, &typ, &notNull, &def, &primary); err != nil {
			return err
		}
		columns[col] = column{typ: strings.ToUpper(typ), notNull: notNull, primary: primary, defaultValue: def}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	expected := map[string]column{
		"resource_id":  {typ: "TEXT", notNull: 1, primary: 1},
		"operation_id": {typ: "TEXT", notNull: 1, primary: 2},
		"mode":         {typ: "TEXT", notNull: 1},
		"expires_at":   {typ: "INTEGER", notNull: 1},
	}
	if len(columns) != len(expected) {
		return fmt.Errorf("%w: coordination database has unexpected resource_locks columns", repositoryformat.ErrUnsupported)
	}
	for col, want := range expected {
		got, ok := columns[col]
		if !ok {
			return fmt.Errorf("%w: coordination database is missing column %s", repositoryformat.ErrUnsupported, col)
		}
		if got.typ != want.typ || got.notNull != want.notNull || got.primary != want.primary || got.defaultValue.Valid {
			return fmt.Errorf("%w: coordination database column %s has incompatible type or constraints", repositoryformat.ErrUnsupported, col)
		}
	}
	var modeCheck bool
	if err := db.QueryRow("SELECT instr(upper(sql), \"CHECK (MODE IN ('R','W'))\") > 0 FROM sqlite_master WHERE type='table' AND name='resource_locks'").Scan(&modeCheck); err != nil || !modeCheck {
		return fmt.Errorf("%w: coordination database resource_locks mode constraint is missing", repositoryformat.ErrUnsupported)
	}
	indexRows, err := db.Query("PRAGMA index_list(resource_locks)")
	if err != nil {
		return err
	}
	for indexRows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := indexRows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			indexRows.Close()
			return err
		}
		// The primary-key autoindex is required. Additional indexes indicate an
		// unrecognized schema that init must leave untouched.
		if origin != "pk" || unique != 1 || partial != 0 {
			indexRows.Close()
			return fmt.Errorf("%w: coordination database has an unexpected resource_locks index", repositoryformat.ErrUnsupported)
		}
		indexCols, err := db.Query("PRAGMA index_info(" + quoteSQLiteIdentifier(name) + ")")
		if err != nil {
			indexRows.Close()
			return err
		}
		got := make([]string, 0, 2)
		for indexCols.Next() {
			var seq, cid int
			var col string
			if err := indexCols.Scan(&seq, &cid, &col); err != nil {
				indexCols.Close()
				indexRows.Close()
				return err
			}
			got = append(got, col)
		}
		if err := indexCols.Err(); err != nil {
			indexCols.Close()
			indexRows.Close()
			return err
		}
		if err := indexCols.Close(); err != nil {
			indexRows.Close()
			return err
		}
		if len(got) != 2 || got[0] != "resource_id" || got[1] != "operation_id" {
			indexRows.Close()
			return fmt.Errorf("%w: coordination database primary key is incompatible", repositoryformat.ErrUnsupported)
		}
	}
	if err := indexRows.Err(); err != nil {
		indexRows.Close()
		return err
	}
	if err := indexRows.Close(); err != nil {
		return err
	}
	return nil
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func ensureInitDirectory(path string) error {
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%w: %s is not a directory", repositoryformat.ErrUnsupported, path)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		return err
	}
	return nil
}

// MigrateRepository runs the explicit schema migration command against an
// existing repository database. It never creates the target database.
func MigrateRepository(ctx context.Context, repoDir string) (int, error) {
	dbPath := filepath.Join(repoDir, "metadata.sqlite")
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("repository metadata database does not exist")
	} else if err != nil {
		return 0, fmt.Errorf("inspect repository metadata database: %w", err)
	}
	store, err := sqlite.NewExistingStore(dbPath)
	if err != nil {
		return 0, err
	}
	defer store.Close()
	// Validate the repository format before asking the backend to perform any
	// schema transition. The current v1 command is a no-op, but later releases
	// keep this ordering as part of the migration boundary.
	if err := store.CheckMigrations(ctx); err != nil {
		return 0, err
	}
	repoFormat, found, err := store.RepositoryFormat(ctx)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, fmt.Errorf("%w: repository format metadata is missing", repositoryformat.ErrUnsupported)
	}
	if err := repoFormat.Validate(); err != nil {
		return 0, err
	}
	version, err := store.Migrate(ctx)
	if err != nil {
		return 0, err
	}
	return version, nil
}

// LoadRepositorySettings reads canonical v1 repository-owned delivery settings.
func LoadRepositorySettings(repoDir string) (repository.Settings, error) {
	file, err := os.Open(filepath.Join(repoDir, "repository.json"))
	if err != nil {
		return repository.Settings{}, err
	}
	defer file.Close()
	var settings repository.Settings
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return repository.Settings{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return repository.Settings{}, fmt.Errorf("repository settings contain multiple JSON values")
	}
	if err := settings.Validate(); err != nil {
		return repository.Settings{}, err
	}
	return settings, nil
}
