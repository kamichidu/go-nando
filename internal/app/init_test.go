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
	want := repository.Settings{DeliveryMode: repository.DeliveryModeRemote, RemoteBucket: "bucket-a"}
	if err := InitRepositoryWithSettings(context.Background(), root, want); err != nil {
		t.Fatalf("init remote repository: %v", err)
	}
	got, err := LoadRepositorySettings(root)
	if err != nil || got.DeliveryMode != want.DeliveryMode || got.RemoteBucket != want.RemoteBucket {
		t.Fatalf("load settings: got=%+v err=%v", got, err)
	}
	changed := repository.Settings{DeliveryMode: repository.DeliveryModeRemote, RemoteBucket: "bucket-b"}
	if err := InitRepositoryWithSettings(context.Background(), root, changed); err == nil {
		t.Fatal("init unexpectedly reconfigured an existing repository")
	}
	got, err = LoadRepositorySettings(root)
	if err != nil || got.DeliveryMode != want.DeliveryMode || got.RemoteBucket != want.RemoteBucket {
		t.Fatalf("existing settings changed: got=%+v err=%v", got, err)
	}
}

func TestLoadRepositorySettingsDefaultsLegacyRepositoryToLocal(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "objects"), 0755); err != nil {
		t.Fatal(err)
	}
	got, err := LoadRepositorySettings(root)
	if err != nil || got != repository.DefaultSettings() {
		t.Fatalf("legacy default: got=%+v err=%v", got, err)
	}
}

func TestLoadLegacyGCSRepositorySettingsWithoutRewriting(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "repository.json")
	legacy := `{"delivery_mode":"gcs","gcs_bucket":"existing-bucket"}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadRepositorySettings(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeliveryMode != repository.DeliveryModeRemote || got.RemoteBackend != "gcs" || got.RemoteBucket != "existing-bucket" {
		t.Fatalf("legacy repository settings=%+v", got)
	}
	bytes, err := os.ReadFile(path)
	if err != nil || string(bytes) != legacy {
		t.Fatalf("loading rewrote legacy settings: content=%q err=%v", bytes, err)
	}
}
