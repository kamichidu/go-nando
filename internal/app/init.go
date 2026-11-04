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
	// repository component. flock is advisory and released automatically after
	// crashes, so an interrupted init remains resumable.
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
	if _, err := os.Stat(settingsPath); err == nil {
		persisted, err := LoadRepositorySettings(repoDir)
		if err != nil {
			return fmt.Errorf("unsupported or invalid repository settings; existing data was left unchanged: %w", err)
		}
		if settings != repository.DefaultSettings() && settings != persisted {
			return fmt.Errorf("existing repository settings cannot be changed by init")
		}
		settings = persisted
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect repository settings path: %w", err)
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
			if entry.Name() != "objects" && entry.Name() != "temp" && !isTemporaryRepositorySettings(entry.Name()) {
				return fmt.Errorf("%w: repository state without metadata.sqlite is not a recognized initialization state", repositoryformat.ErrUnsupported)
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
	defer store.Close()
	repoFormat := repositoryformat.Current()
	if err := repoFormat.Validate(); err != nil {
		return err
	}
	if dbExists {
		if err := store.CheckMigrations(ctx); errors.Is(err, sqlite.ErrSchemaUninitialized) {
			if err := store.Initialize(ctx, repoFormat); err != nil {
				return fmt.Errorf("initialize empty metadata database: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("existing metadata database is not a supported initialized repository: %w", err)
		} else {
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
		}
	} else if err := store.Initialize(ctx, repoFormat); err != nil {
		return fmt.Errorf("failed to initialize metadata schema: %w", err)
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

	if _, err := os.Lstat(settingsPath); errors.Is(err, os.ErrNotExist) {
		if err := publishRepositorySettings(repoDir, settings); err != nil {
			return err
		}
	} else if err != nil {
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

func validateInitRoot(repoDir string) error {
	allowed := map[string]bool{"metadata.sqlite": true, "coordination.db": true, "objects": true, "temp": true, "repository.json": true}
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		return fmt.Errorf("inspect repository directory: %w", err)
	}
	for _, entry := range entries {
		if isTemporaryRepositorySettings(entry.Name()) {
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

func isTemporaryRepositorySettings(name string) bool {
	return strings.HasPrefix(name, ".repository.json-") && strings.HasSuffix(name, ".tmp")
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
	var objects int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type IN ('table','index','trigger','view') AND name NOT LIKE 'sqlite_%'").Scan(&objects); err != nil {
		return fmt.Errorf("inspect repository coordinator database: %w", err)
	}
	if objects == 0 {
		return nil
	}
	if objects != 1 {
		return fmt.Errorf("%w: coordination database has unknown tables", repositoryformat.ErrUnsupported)
	}
	var name string
	if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&name); err != nil || name != "resource_locks" {
		return fmt.Errorf("%w: coordination database has an unknown schema", repositoryformat.ErrUnsupported)
	}
	var ddl string
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='resource_locks'").Scan(&ddl); err != nil {
		return fmt.Errorf("%w: coordination database has an invalid resource_locks definition: %v", repositoryformat.ErrUnsupported, err)
	}
	canonical := "CREATE TABLE resource_locks ( resource_id TEXT NOT NULL, operation_id TEXT NOT NULL, mode TEXT NOT NULL CHECK (mode IN ('R','W')), expires_at INTEGER NOT NULL, PRIMARY KEY (resource_id, operation_id) )"
	normalize := func(s string) string { return strings.Join(strings.Fields(strings.ToUpper(s)), " ") }
	if normalize(ddl) != normalize(canonical) {
		return fmt.Errorf("%w: coordination database resource_locks definition is incompatible", repositoryformat.ErrUnsupported)
	}
	rows, err := db.Query("PRAGMA table_info(resource_locks)")
	if err != nil {
		return err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primary int
		var col, typ string
		var def sql.NullString
		if err := rows.Scan(&cid, &col, &typ, &notNull, &def, &primary); err != nil {
			return err
		}
		columns[col] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	expected := map[string]bool{"resource_id": true, "operation_id": true, "mode": true, "expires_at": true}
	if len(columns) != len(expected) {
		return fmt.Errorf("%w: coordination database has unexpected resource_locks columns", repositoryformat.ErrUnsupported)
	}
	for col := range expected {
		if !columns[col] {
			return fmt.Errorf("%w: coordination database is missing column %s", repositoryformat.ErrUnsupported, col)
		}
	}
	return nil
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
