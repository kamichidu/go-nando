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
	want := repository.Settings{DeliveryMode: repository.DeliveryModeGCS, GCSBucket: "bucket-a"}
	if err := InitRepositoryWithSettings(context.Background(), root, want); err != nil {
		t.Fatalf("init GCS repository: %v", err)
	}
	got, err := LoadRepositorySettings(root)
	if err != nil || got.DeliveryMode != want.DeliveryMode || got.GCSBucket != want.GCSBucket || got.MetadataIdentity == "" {
		t.Fatalf("load settings: got=%+v err=%v", got, err)
	}
	changed := repository.Settings{DeliveryMode: repository.DeliveryModeGCS, GCSBucket: "bucket-b"}
	if err := InitRepositoryWithSettings(context.Background(), root, changed); err == nil {
		t.Fatal("init unexpectedly reconfigured an existing repository")
	}
	got, err = LoadRepositorySettings(root)
	if err != nil || got.DeliveryMode != want.DeliveryMode || got.GCSBucket != want.GCSBucket || got.MetadataIdentity == "" {
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
