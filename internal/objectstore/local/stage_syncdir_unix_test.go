//go:build unix

package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kamichidu/go-nando/internal/content"
)

func TestPublishSyncsObjectDirectoryAndRetainsStageOnSyncFailure(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "objects"), filepath.Join(root, "temp"), content.NewHasher())
	if err != nil {
		t.Fatal(err)
	}
	const operationID = "83ce94d1-086b-44d1-a061-e4451b7d020a"
	session, err := store.BeginStage(ctx, operationID)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := session.Stage(ctx, strings.NewReader("durable publication"))
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("injected directory sync failure")
	store.syncDir = func(string) error { return wantErr }
	if _, err := store.Publish(ctx, staged); !errors.Is(err, wantErr) {
		t.Fatalf("Publish error=%v, want injected sync failure", err)
	}
	if _, err := os.Stat(filepath.Join(store.objectsDir, staged.ID().String())); err != nil {
		t.Fatalf("published link should remain visible after ambiguous sync failure: %v", err)
	}
	if _, err := os.Stat(staged.(*stagedObject).path); err != nil {
		t.Fatalf("staging link was removed after ambiguous sync failure: %v", err)
	}
	syncCalls := 0
	store.syncDir = func(path string) error {
		syncCalls++
		if path != store.objectsDir {
			t.Fatalf("synced unexpected directory %q", path)
		}
		return nil
	}
	if _, err := store.Publish(ctx, staged); err != nil {
		t.Fatalf("retry after ambiguous publication: %v", err)
	}
	if syncCalls != 1 {
		t.Fatalf("directory sync calls=%d, want 1", syncCalls)
	}
	if _, err := os.Stat(staged.(*stagedObject).path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging link remains after successful retry: %v", err)
	}
	if err := store.Verify(ctx, staged.ID()); err != nil {
		t.Fatalf("published object failed content-ID verification after retry: %v", err)
	}
	objects, err := os.ReadDir(store.objectsDir)
	if err != nil || len(objects) != 1 || objects[0].Name() != staged.ID().String() {
		t.Fatalf("retry changed publication set: objects=%v err=%v", objects, err)
	}
}
