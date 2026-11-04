package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
	for _, name := range []string{"coordination.db", "objects", "temp"} {
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
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
