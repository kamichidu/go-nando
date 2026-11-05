//go:build linux

package local_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/objectstore/local"
)

func TestCommitCrossFilesystemFailsAndCleansStaging(t *testing.T) {
	objectsParent := "/dev/shm"
	if _, err := os.Stat(objectsParent); err != nil {
		t.Skip("/dev/shm is unavailable")
	}
	root := t.TempDir()
	objectsDir := filepath.Join(objectsParent, "go-nando-test-"+filepath.Base(root))
	if err := os.Mkdir(objectsDir, 0o700); err != nil {
		t.Skipf("cannot create object test directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(objectsDir); err != nil {
			t.Errorf("failed to remove cross-filesystem test objects: %v", err)
		}
	})
	tempDir := filepath.Join(root, "temp")
	if err := os.Mkdir(tempDir, 0o700); err != nil {
		t.Fatal(err)
	}

	objectInfo, err := os.Stat(objectsDir)
	if err != nil {
		t.Fatal(err)
	}
	tempInfo, err := os.Stat(tempDir)
	if err != nil {
		t.Fatal(err)
	}
	objectStat, ok := objectInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("object directory does not expose Unix device metadata")
	}
	tempStat, ok := tempInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("staging directory does not expose Unix device metadata")
	}
	objectDev := objectStat.Dev
	tempDev := tempStat.Dev
	if objectDev == tempDev {
		t.Skip("test directories are on the same filesystem")
	}

	store, err := local.NewStore(objectsDir, tempDir, content.NewHasher())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit(context.Background(), strings.NewReader("content")); !errors.Is(err, local.ErrCrossFilesystem) {
		t.Fatalf("expected cross-filesystem publication error, got %v", err)
	}
	objects, err := os.ReadDir(objectsDir)
	if err != nil || len(objects) != 0 {
		t.Fatalf("cross-filesystem failure must not publish an object, entries=%v err=%v", objects, err)
	}
	staging, err := os.ReadDir(tempDir)
	if err != nil || len(staging) != 0 {
		t.Fatalf("cross-filesystem failure must clean staging, entries=%v err=%v", staging, err)
	}
}
