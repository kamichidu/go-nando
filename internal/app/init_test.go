package app

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/kamichidu/go-nando/internal/metadata/sqlite"
	"github.com/kamichidu/go-nando/internal/repository"
)

func TestInitRepositoryPersistsDeliverySettingsAndDoesNotReconfigure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	want := repository.Settings{DeliveryMode: repository.DeliveryModeRemote, RemoteURI: "gs://bucket-a"}
	if err := InitRepositoryWithSettings(context.Background(), root, want); err != nil {
		t.Fatalf("init remote repository: %v", err)
	}
	got, err := LoadRepositorySettings(root)
	if err != nil || got.DeliveryMode != want.DeliveryMode || got.RemoteURI != want.RemoteURI {
		t.Fatalf("load settings: got=%+v err=%v", got, err)
	}
	changed := repository.Settings{DeliveryMode: repository.DeliveryModeRemote, RemoteURI: "gs://bucket-b"}
	if err := InitRepositoryWithSettings(context.Background(), root, changed); err == nil {
		t.Fatal("init unexpectedly reconfigured an existing repository")
	}
	got, err = LoadRepositorySettings(root)
	if err != nil || got.DeliveryMode != want.DeliveryMode || got.RemoteURI != want.RemoteURI {
		t.Fatalf("existing settings changed: got=%+v err=%v", got, err)
	}
	if err := InitRepository(context.Background(), root); err != nil {
		t.Fatalf("repeated init should preserve and accept an existing repository: %v", err)
	}
}

func TestInitRepositoryResumesValidatedPartialRepository(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	want := repository.Settings{DeliveryMode: repository.DeliveryModeRemote, RemoteURI: "gs://keep-me"}
	if err := InitRepositoryWithSettings(context.Background(), root, want); err != nil {
		t.Fatal(err)
	}
	object := filepath.Join(root, "objects", "keep")
	if err := os.WriteFile(object, []byte("committed object"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"coordination.db", "temp"} {
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := os.ReadFile(object); err != nil || string(got) != "committed object" {
		t.Fatalf("existing object changed: %q err=%v", got, err)
	}
	if err := InitRepositoryWithSettings(context.Background(), root, repository.DefaultSettings()); err != nil {
		t.Fatalf("resume valid partial repository: %v", err)
	}
	got, err := LoadRepositorySettings(root)
	if err != nil || got != want {
		t.Fatalf("resume changed repository settings: got=%+v err=%v", got, err)
	}
	for _, name := range []string{"coordination.db", "objects", "temp"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Errorf("resumed repository missing %s: %v", name, err)
		}
	}
}

func TestInitRepositoryKeepsExistingSettingsFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	want := repository.Settings{DeliveryMode: repository.DeliveryModeRemote, RemoteURI: "gs://keep-existing"}
	if err := InitRepositoryWithSettings(context.Background(), root, want); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "repository.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := InitRepository(context.Background(), root); err != nil {
		t.Fatalf("repeat init: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("repository settings changed: before=%q after=%q", before, after)
	}
}

func TestInitRepositoryRejectsIncompatibleCoordinationSchemaWithoutMutation(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "coordination.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE resource_locks (resource_id TEXT NOT NULL, operation_id TEXT NOT NULL, mode TEXT NOT NULL, expires_at INTEGER NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := InitRepository(context.Background(), root); err == nil {
		t.Fatal("incompatible coordinator schema was accepted")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "coordination.db" {
		t.Fatalf("unsupported state was mutated: entries=%v", entries)
	}
}

func TestInitRepositoryConcurrentCalls(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	const callers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- InitRepository(context.Background(), root)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent init: %v", err)
		}
	}
	if _, err := LoadRepositorySettings(root); err != nil {
		t.Fatalf("concurrent init did not publish valid settings: %v", err)
	}
	if err := InitRepository(context.Background(), root); err != nil {
		t.Fatalf("repeat init with persistent lock file: %v", err)
	}
}

func TestInitRepositoryResumesInterruptedSettingsPublication(t *testing.T) {
	root := t.TempDir()
	tmp := filepath.Join(root, ".repository.json-interrupted.tmp")
	if err := os.WriteFile(tmp, []byte(`{"partial":`), 0o600); err != nil {
		t.Fatal(err)
	}
	want := repository.Settings{DeliveryMode: repository.DeliveryModeRemote, RemoteURI: "gs://resumed"}
	if err := InitRepositoryWithSettings(context.Background(), root, want); err != nil {
		t.Fatalf("resume interrupted settings publication: %v", err)
	}
	got, err := LoadRepositorySettings(root)
	if err != nil || got != want {
		t.Fatalf("resumed settings: got=%+v err=%v", got, err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("interrupted temp remains: %v", err)
	}
}

func TestInitRepositoryRejectsUnexpectedCoordinatorIndexWithoutMutation(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "coordination.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE resource_locks (
		resource_id TEXT NOT NULL, operation_id TEXT NOT NULL, mode TEXT NOT NULL CHECK (mode IN ('R','W')),
		expires_at INTEGER NOT NULL, PRIMARY KEY(resource_id, operation_id)); CREATE INDEX extra_lock_index ON resource_locks(mode);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := InitRepository(context.Background(), root); err == nil {
		t.Fatal("unexpected coordinator index was accepted")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "coordination.db" {
		t.Fatalf("unsupported state was mutated: %v", entries)
	}
}

func TestInitRepositoryRejectsCoordinatorSchemaContractVariants(t *testing.T) {
	tests := map[string]string{
		"wrong type":              `CREATE TABLE resource_locks (resource_id BLOB NOT NULL, operation_id TEXT NOT NULL, mode TEXT NOT NULL CHECK (mode IN ('R','W')), expires_at INTEGER NOT NULL, PRIMARY KEY(resource_id,operation_id))`,
		"wrong primary key":       `CREATE TABLE resource_locks (resource_id TEXT NOT NULL, operation_id TEXT NOT NULL, mode TEXT NOT NULL CHECK (mode IN ('R','W')), expires_at INTEGER NOT NULL, PRIMARY KEY(operation_id,resource_id))`,
		"missing mode constraint": `CREATE TABLE resource_locks (resource_id TEXT NOT NULL, operation_id TEXT NOT NULL, mode TEXT NOT NULL, expires_at INTEGER NOT NULL, PRIMARY KEY(resource_id,operation_id))`,
	}
	for name, ddl := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			db, err := sql.Open("sqlite", filepath.Join(root, "coordination.db"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(ddl); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if err := InitRepository(context.Background(), root); err == nil {
				t.Fatal("incompatible coordinator schema was accepted")
			}
			if _, err := os.Stat(filepath.Join(root, "metadata.sqlite")); !os.IsNotExist(err) {
				t.Fatalf("metadata was created for unsupported state: %v", err)
			}
		})
	}
}

func TestPublishRepositorySettingsDoesNotReplaceExistingFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "repository.json")
	original := []byte("preserve me")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	err := publishRepositorySettings(root, repository.DefaultSettings())
	if err == nil {
		t.Fatal("publish unexpectedly replaced an existing settings file")
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != string(original) {
		t.Fatalf("existing settings changed: %q err=%v", got, readErr)
	}
}

func TestInitRepositoryCompletesEmptyMetadataDatabaseFromPartialState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(root, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "temp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "metadata.sqlite"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InitRepository(context.Background(), root); err != nil {
		t.Fatalf("resume empty metadata database: %v", err)
	}
	if err := InitRepository(context.Background(), root); err != nil {
		t.Fatalf("repeat completed init: %v", err)
	}
	if _, err := LoadRepositorySettings(root); err != nil {
		t.Fatalf("completed settings missing: %v", err)
	}
}

func TestInitRepositoryResumesAfterSettingsPublicationBeforeSchemaInitialization(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	want := repository.Settings{DeliveryMode: repository.DeliveryModeRemote, RemoteURI: "gs://resume-after-settings"}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := publishRepositorySettings(root, want); err != nil {
		t.Fatalf("publish settings for interrupted initialization state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "metadata.sqlite"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := InitRepository(context.Background(), root); err != nil {
		t.Fatalf("resume initialization without changing published settings: %v", err)
	}
	got, err := LoadRepositorySettings(root)
	if err != nil || got != want {
		t.Fatalf("resumed settings: got=%+v err=%v", got, err)
	}
	store, err := sqlite.NewExistingStore(filepath.Join(root, "metadata.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CheckMigrations(context.Background()); err != nil {
		t.Fatalf("resumed schema is not valid: %v", err)
	}
}

func TestInitRepositoryRejectsUnknownMetadataSchemaWithoutMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "metadata.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE VIEW preserved_view AS SELECT 1 AS value"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := InitRepository(context.Background(), root); err == nil {
		t.Fatal("init accepted a user_version=0 database with a schema view")
	}
	if got, err := os.ReadFile(dbPath); err != nil || string(got) != string(before) {
		t.Fatalf("unknown metadata schema changed: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "repository.json")); !os.IsNotExist(err) {
		t.Fatalf("settings were published for unknown metadata: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "objects")); !os.IsNotExist(err) {
		t.Fatalf("objects directory was created for unknown metadata: %v", err)
	}
}

func TestInitRepositoryRejectsUnknownPartialStateWithoutMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(root, "mystery")
	if err := os.WriteFile(unknown, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InitRepository(context.Background(), root); err == nil {
		t.Fatal("unknown repository state was accepted")
	}
	bytes, err := os.ReadFile(unknown)
	if err != nil || string(bytes) != "preserve" {
		t.Fatalf("unknown state changed: %q err=%v", bytes, err)
	}
}

func TestInitRepositoryRejectsObjectsWithoutMetadataWithoutMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	object := filepath.Join(root, "objects", "sha256-existing")
	if err := os.MkdirAll(filepath.Dir(object), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(object, []byte("preserve committed object"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InitRepository(context.Background(), root); err == nil {
		t.Fatal("init accepted existing object data without metadata.sqlite")
	}
	if got, err := os.ReadFile(object); err != nil || string(got) != "preserve committed object" {
		t.Fatalf("existing object changed: %q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "metadata.sqlite")); !os.IsNotExist(err) {
		t.Fatalf("metadata was created despite existing object data: %v", err)
	}
}

func TestInitRepositoryRejectsMetadataWithoutSettingsWithoutMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	if err := InitRepositoryWithSettings(context.Background(), root, repository.Settings{DeliveryMode: repository.DeliveryModeRemote, RemoteURI: "gs://original"}); err != nil {
		t.Fatal(err)
	}
	metadata := filepath.Join(root, "metadata.sqlite")
	before, err := os.ReadFile(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "repository.json")); err != nil {
		t.Fatal(err)
	}
	if err := InitRepository(context.Background(), root); err == nil {
		t.Fatal("init accepted metadata with unknown repository settings")
	}
	if got, err := os.ReadFile(metadata); err != nil || string(got) != string(before) {
		t.Fatalf("metadata changed: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "repository.json")); !os.IsNotExist(err) {
		t.Fatalf("missing settings were recreated with a default: %v", err)
	}
}

func TestLoadRepositorySettingsRejectsMissingSettings(t *testing.T) {
	root := t.TempDir()
	if _, err := LoadRepositorySettings(root); err == nil {
		t.Fatal("missing pre-v1 repository settings were accepted")
	}
}

func TestLoadRepositorySettingsRejectsLegacySettingsWithoutRewriting(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "repository.json")
	legacy := `{"delivery_mode":"gcs","gcs_bucket":"existing-bucket"}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRepositorySettings(root); err == nil {
		t.Fatal("legacy repository settings were accepted")
	}
	bytes, err := os.ReadFile(path)
	if err != nil || string(bytes) != legacy {
		t.Fatalf("loading rewrote legacy settings: content=%q err=%v", bytes, err)
	}
}
