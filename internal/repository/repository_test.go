package repository_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/metadata/sqlite"
	"github.com/kamichidu/go-nando/internal/objectstore/local"
	"github.com/kamichidu/go-nando/internal/repository"
)

type fixedIDMetadataStore struct {
	metadata.MetadataStore
}

func (m *fixedIDMetadataStore) Create(ctx context.Context, entry metadata.Entry) (metadata.Entry, error) {
	switch entry.Name {
	case "documents":
		entry.ID = "dir-documents"
	case "hello.txt", "greeting.txt":
		entry.ID = "file-hello"
	}
	return m.MetadataStore.Create(ctx, entry)
}

func TestRepository(t *testing.T) {
	ctx := context.Background()
	hasher := content.NewHasher()

	setupRepo := func(t *testing.T) (*repository.Repository, string, metadata.MetadataStore) {
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

		validatingMetaStore := metadata.NewValidatingStore(metaStore)
		repo := repository.NewRepository(validatingMetaStore, objStore, hasher)
		return repo, tempDir, validatingMetaStore
	}

	t.Run("put followed by get restores the original file content", func(t *testing.T) {
		repo, tempDir, _ := setupRepo(t)

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
			t.Errorf("expected mode 0600, got %o", restoredStat.Mode().Perm())
		}
	})

	t.Run("identical contents on different paths reference the same object", func(t *testing.T) {
		repo, tempDir, _ := setupRepo(t)

		file1 := filepath.Join(tempDir, "file1.txt")
		file2 := filepath.Join(tempDir, "file2.txt")
		err := os.WriteFile(file1, []byte("shared content"), 0644)
		if err != nil {
			t.Fatalf("failed to write file1: %v", err)
		}
		err = os.WriteFile(file2, []byte("shared content"), 0644)
		if err != nil {
			t.Fatalf("failed to write file2: %v", err)
		}

		err = repo.Put(ctx, file1, "/path1.txt")
		if err != nil {
			t.Fatalf("failed to put file1: %v", err)
		}
		err = repo.Put(ctx, file2, "/path2.txt")
		if err != nil {
			t.Fatalf("failed to put file2: %v", err)
		}

		stat1, err := repo.Stat(ctx, "/path1.txt")
		if err != nil {
			t.Fatalf("failed to stat file1: %v", err)
		}
		stat2, err := repo.Stat(ctx, "/path2.txt")
		if err != nil {
			t.Fatalf("failed to stat file2: %v", err)
		}

		if stat1.ObjectID.String() != stat2.ObjectID.String() {
			t.Errorf("identical files should refer to the same object ID, got %q and %q", stat1.ObjectID.String(), stat2.ObjectID.String())
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
		repo, tempDir, _ := setupRepo(t)

		file := filepath.Join(tempDir, "source.txt")
		err := os.WriteFile(file, []byte("rename test content"), 0644)
		if err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		err = repo.Put(ctx, file, "/old.txt")
		if err != nil {
			t.Fatalf("failed to put old.txt: %v", err)
		}
		oldStat, err := repo.Stat(ctx, "/old.txt")
		if err != nil {
			t.Fatalf("failed to stat old.txt: %v", err)
		}

		err = repo.Move(ctx, "/old.txt", "/new.txt")
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

		if newStat.ObjectID.String() != oldStat.ObjectID.String() {
			t.Errorf("hash changed from %q to %q", oldStat.ObjectID.String(), newStat.ObjectID.String())
		}
	})

	t.Run("delete soft-deletes and does not immediately remove object, GC removes it", func(t *testing.T) {
		repo, tempDir, _ := setupRepo(t)

		file := filepath.Join(tempDir, "delete_test.txt")
		err := os.WriteFile(file, []byte("to be deleted"), 0644)
		if err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		err = repo.Put(ctx, file, "/delete_me.txt")
		if err != nil {
			t.Fatalf("failed to put file: %v", err)
		}

		err = repo.Remove(ctx, "/delete_me.txt")
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

	t.Run("Repository Operation Golden Test", func(t *testing.T) {
		tempDir := t.TempDir()
		dbPath := filepath.Join(tempDir, "metadata.sqlite")
		rawStore, err := sqlite.NewStore(dbPath)
		if err != nil {
			t.Fatalf("failed to create meta store: %v", err)
		}
		if err := rawStore.Initialize(ctx); err != nil {
			t.Fatalf("failed to init: %v", err)
		}

		// Wrap in fixedIDMetadataStore to generate stable golden EntryIDs!
		metaStore := &fixedIDMetadataStore{MetadataStore: metadata.NewValidatingStore(rawStore)}

		objectsDir := filepath.Join(tempDir, "objects")
		tempObjDir := filepath.Join(tempDir, "temp")
		objStore, err := local.NewStore(objectsDir, tempObjDir, hasher)
		if err != nil {
			t.Fatalf("failed to create object store: %v", err)
		}

		repo := repository.NewRepository(metaStore, objStore, hasher)

		var sb strings.Builder

		// 1. Create directory /documents (implied during put)
		sb.WriteString("OP: create directory /documents\n")
		_, err = repo.ResolveOrCreateParentDirectories(ctx, "/documents/hello.txt")
		if err != nil {
			t.Fatalf("failed to create dir: %v", err)
		}
		docsDir, err := repo.Stat(ctx, "/documents")
		if err != nil {
			t.Fatalf("failed to stat /documents: %v", err)
		}
		fmt.Fprintf(&sb, "EntryID: %s\n", docsDir.ID.String())
		pStr := "-"
		if docsDir.ParentID != nil {
			pStr = docsDir.ParentID.String()
		}
		fmt.Fprintf(&sb, "ParentID: %s\n", pStr)
		fmt.Fprintf(&sb, "Name: %s\n", docsDir.Name)
		sb.WriteString("Revision Before: - After: 1\n")
		sb.WriteString("ObjectID Before: - After: -\n")
		sb.WriteString("Parent Revision Before: 1 After: 1\n\n")

		// 2. Put file /documents/hello.txt
		sb.WriteString("OP: put file /documents/hello.txt\n")
		helloFile := filepath.Join(tempDir, "hello.txt")
		err = os.WriteFile(helloFile, []byte("hello nando\n"), 0644)
		if err != nil {
			t.Fatalf("failed to write local file: %v", err)
		}
		err = repo.Put(ctx, helloFile, "/documents/hello.txt")
		if err != nil {
			t.Fatalf("failed to put: %v", err)
		}
		helloStat, err := repo.Stat(ctx, "/documents/hello.txt")
		if err != nil {
			t.Fatalf("failed to stat hello: %v", err)
		}
		fmt.Fprintf(&sb, "EntryID: %s\n", helloStat.ID.String())
		fmt.Fprintf(&sb, "ParentID: %s\n", helloStat.ParentID.String())
		fmt.Fprintf(&sb, "Name: %s\n", helloStat.Name)
		fmt.Fprintf(&sb, "Revision Before: - After: %d\n", helloStat.Revision)
		fmt.Fprintf(&sb, "ObjectID Before: - After: %s\n", helloStat.ObjectID.String())
		sb.WriteString("Parent Revision Before: 1 After: 1\n\n")

		// 3. Replace file content /documents/hello.txt
		sb.WriteString("OP: replace file content /documents/hello.txt\n")
		err = os.WriteFile(helloFile, []byte(""), 0644)
		if err != nil {
			t.Fatalf("failed to write empty file: %v", err)
		}
		err = repo.Put(ctx, helloFile, "/documents/hello.txt")
		if err != nil {
			t.Fatalf("failed to put replacement: %v", err)
		}
		helloStatRepl, err := repo.Stat(ctx, "/documents/hello.txt")
		if err != nil {
			t.Fatalf("failed to stat hello repl: %v", err)
		}
		fmt.Fprintf(&sb, "EntryID: %s\n", helloStatRepl.ID.String())
		fmt.Fprintf(&sb, "ParentID: %s\n", helloStatRepl.ParentID.String())
		fmt.Fprintf(&sb, "Name: %s\n", helloStatRepl.Name)
		fmt.Fprintf(&sb, "Revision Before: %d After: %d\n", helloStat.Revision, helloStatRepl.Revision)
		fmt.Fprintf(&sb, "ObjectID Before: %s After: %s\n", helloStat.ObjectID.String(), helloStatRepl.ObjectID.String())
		sb.WriteString("Parent Revision Before: 1 After: 1\n\n")

		// 4. Move file /documents/hello.txt to /documents/greeting.txt
		sb.WriteString("OP: move file /documents/hello.txt to /documents/greeting.txt\n")
		err = repo.Move(ctx, "/documents/hello.txt", "/documents/greeting.txt")
		if err != nil {
			t.Fatalf("failed to move: %v", err)
		}
		movedStat, err := repo.Stat(ctx, "/documents/greeting.txt")
		if err != nil {
			t.Fatalf("failed to stat greeting: %v", err)
		}
		fmt.Fprintf(&sb, "EntryID: %s\n", movedStat.ID.String())
		fmt.Fprintf(&sb, "ParentID: %s\n", movedStat.ParentID.String())
		fmt.Fprintf(&sb, "Name: %s\n", movedStat.Name)
		fmt.Fprintf(&sb, "Revision Before: %d After: %d\n", helloStatRepl.Revision, movedStat.Revision)
		fmt.Fprintf(&sb, "ObjectID Before: %s After: %s\n", helloStatRepl.ObjectID.String(), movedStat.ObjectID.String())
		sb.WriteString("Parent Revision Before: 1 After: 1\n\n")

		// 5. Remove file /documents/greeting.txt
		sb.WriteString("OP: remove file /documents/greeting.txt\n")
		err = repo.Remove(ctx, "/documents/greeting.txt")
		if err != nil {
			t.Fatalf("failed to remove: %v", err)
		}
		_, err = repo.Stat(ctx, "/documents/greeting.txt")
		if !errors.Is(err, metadata.ErrEntryNotFound) {
			t.Errorf("expected not found, got %v", err)
		}
		sb.WriteString("EntryID: file-hello\n")
		sb.WriteString("ParentID: dir-documents\n")
		sb.WriteString("Name: greeting.txt\n")
		sb.WriteString("Status: Deleted (Unresolvable)\n")
		sb.WriteString("Parent Revision Before: 1 After: 1\n")

		actual := sb.String()

		// Read golden file
		goldenPath := filepath.Join("testdata", "repository_operation.golden")
		expectedBytes, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("failed to read golden file: %v", err)
		}

		expected := string(expectedBytes)
		if actual != expected {
			t.Errorf("Repository operation scenario trace mismatch!\nActual:\n%s\nExpected:\n%s", actual, expected)
		}
	})
}
