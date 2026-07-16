package repository_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/metadata/sqlite"
	"github.com/kamichidu/go-nando/internal/objectstore/local"
	"github.com/kamichidu/go-nando/internal/repository"
)

func TestRepository(t *testing.T) {
	ctx := context.Background()
	hasher := content.NewHasher()

	setupRepo := func(t *testing.T) (*repository.Repository, string) {
		t.Helper()
		tempDir := t.TempDir()

		dbPath := filepath.Join(tempDir, "metadata.sqlite")
		metaStore, err := sqlite.NewStore(dbPath)
		if err != nil {
			t.Fatalf("failed to create meta store: %v", err)
		}
		if err := metaStore.Initialize(ctx); err != nil {
			t.Fatalf("failed to init meta store: %v", err)
		}

		objectsDir := filepath.Join(tempDir, "objects")
		tempObjDir := filepath.Join(tempDir, "temp")
		objStore, err := local.NewStore(objectsDir, tempObjDir, hasher)
		if err != nil {
			t.Fatalf("failed to create object store: %v", err)
		}

		repo := repository.NewRepository(metaStore, objStore, hasher)
		return repo, tempDir
	}

	t.Run("put followed by get restores the original file content", func(t *testing.T) {
		repo, tempDir := setupRepo(t)

		// Create a local file to put
		localFile := filepath.Join(tempDir, "hello.txt")
		contentBytes := []byte("hello nando integration test")
		if err := os.WriteFile(localFile, contentBytes, 0600); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		repoPath := "/documents/hello.txt"
		if err := repo.Put(ctx, localFile, repoPath); err != nil {
			t.Fatalf("failed to put: %v", err)
		}

		// Restore to a new location
		restoredFile := filepath.Join(tempDir, "restored.txt")
		if err := repo.Get(ctx, repoPath, restoredFile); err != nil {
			t.Fatalf("failed to get: %v", err)
		}

		restoredBytes, err := os.ReadFile(restoredFile)
		if err != nil {
			t.Fatalf("failed to read restored file: %v", err)
		}

		if string(restoredBytes) != string(contentBytes) {
			t.Errorf("expected %q, got %q", string(contentBytes), string(restoredBytes))
		}

		// Check mode and timestamps are restored (approximately)
		restoredStat, err := os.Stat(restoredFile)
		if err != nil {
			t.Fatalf("failed to stat restored file: %v", err)
		}
		if restoredStat.Mode().Perm() != 0600 {
			t.Errorf("expected permission 0600, got %v", restoredStat.Mode())
		}
	})

	t.Run("identical contents on different paths reference the same object", func(t *testing.T) {
		repo, tempDir := setupRepo(t)

		file1 := filepath.Join(tempDir, "file1.txt")
		file2 := filepath.Join(tempDir, "file2.txt")
		_ = os.WriteFile(file1, []byte("shared content"), 0644)
		_ = os.WriteFile(file2, []byte("shared content"), 0644)

		_ = repo.Put(ctx, file1, "/path1.txt")
		_ = repo.Put(ctx, file2, "/path2.txt")

		stat1, _ := repo.Stat(ctx, "/path1.txt")
		stat2, _ := repo.Stat(ctx, "/path2.txt")

		if stat1.Hash != stat2.Hash {
			t.Errorf("identical files should refer to the same hash, got %q and %q", stat1.Hash, stat2.Hash)
		}

		// Run GC, neither should be deleted since both paths reference it
		deleted, err := repo.GC(ctx)
		if err != nil {
			t.Fatalf("gc failed: %v", err)
		}
		if deleted != 0 {
			t.Errorf("expected 0 deleted objects, got %d", deleted)
		}
	})

	t.Run("rename updates metadata path but keeps object hash", func(t *testing.T) {
		repo, tempDir := setupRepo(t)

		file := filepath.Join(tempDir, "source.txt")
		_ = os.WriteFile(file, []byte("rename test content"), 0644)

		_ = repo.Put(ctx, file, "/old.txt")
		oldStat, _ := repo.Stat(ctx, "/old.txt")

		err := repo.Move(ctx, "/old.txt", "/new.txt")
		if err != nil {
			t.Fatalf("rename failed: %v", err)
		}

		_, err = repo.Stat(ctx, "/old.txt")
		if !errors.Is(err, metadata.ErrEntryNotFound) {
			t.Errorf("expected old path to be deleted/not found, got %v", err)
		}

		newStat, err := repo.Stat(ctx, "/new.txt")
		if err != nil {
			t.Fatalf("new path stat failed: %v", err)
		}

		if newStat.Hash != oldStat.Hash {
			t.Errorf("hash changed from %q to %q", oldStat.Hash, newStat.Hash)
		}
	})

	t.Run("delete soft-deletes and does not immediately remove object, GC removes it", func(t *testing.T) {
		repo, tempDir := setupRepo(t)

		file := filepath.Join(tempDir, "delete_test.txt")
		_ = os.WriteFile(file, []byte("to be deleted"), 0644)

		_ = repo.Put(ctx, file, "/delete_me.txt")

		err := repo.Remove(ctx, "/delete_me.txt")
		if err != nil {
			t.Fatalf("delete failed: %v", err)
		}

		// Entry is gone from view
		_, err = repo.Stat(ctx, "/delete_me.txt")
		if !errors.Is(err, metadata.ErrEntryNotFound) {
			t.Fatalf("expected entry to be gone, got %v", err)
		}

		// Running GC now should clean up the orphan object
		deletedCount, err := repo.GC(ctx)
		if err != nil {
			t.Fatalf("GC failed: %v", err)
		}
		if deletedCount != 1 {
			t.Errorf("expected 1 deleted object in GC, got %d", deletedCount)
		}

		// Running Doctor should pass successfully!
		if err := repo.Doctor(ctx); err != nil {
			t.Errorf("Doctor failed: %v", err)
		}
	})
}
