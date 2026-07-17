package local_test

import (
	"bytes"
	"context"
	"crypto/sha1"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/objectstore"
	"github.com/kamichidu/go-nando/internal/objectstore/local"
)

func TestLocalStore(t *testing.T) {
	hasher := content.NewHasher()
	ctx := context.Background()

	setupStore := func(t *testing.T) (*local.Store, string) {
		t.Helper()
		tempDir := t.TempDir()
		objectsDir := filepath.Join(tempDir, "objects")
		objsTempDir := filepath.Join(tempDir, "temp")

		store, err := local.NewStore(objectsDir, objsTempDir, hasher)
		if err != nil {
			t.Fatalf("failed to create store: %v", err)
		}
		return store, tempDir
	}

	t.Run("Commit and Open successful", func(t *testing.T) {
		store, _ := setupStore(t)
		contentStr := "test object content"

		res, err := store.Commit(ctx, strings.NewReader(contentStr))
		if err != nil {
			t.Fatalf("failed to commit: %v", err)
		}

		expectedID, err := content.ParseID("7dbfef13e56908a5df3983234269d73489e97e52")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}
		if !res.ID.Equal(expectedID) {
			t.Errorf("expected ID %s, got %s", expectedID.String(), res.ID.String())
		}
		if res.Size != int64(len(contentStr)) {
			t.Errorf("expected size %d, got %d", len(contentStr), res.Size)
		}

		rc, err := store.Open(ctx, res.ID)
		if err != nil {
			t.Fatalf("failed to open object: %v", err)
		}
		defer rc.Close()

		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("failed to read: %v", err)
		}
		if string(data) != contentStr {
			t.Errorf("expected content %q, got %q", contentStr, string(data))
		}
	})

	t.Run("Commit identical content multiple times does not break and reuses", func(t *testing.T) {
		store, _ := setupStore(t)
		contentStr := "reused content"

		res1, err := store.Commit(ctx, strings.NewReader(contentStr))
		if err != nil {
			t.Fatalf("first commit failed: %v", err)
		}

		res2, err := store.Commit(ctx, strings.NewReader(contentStr))
		if err != nil {
			t.Fatalf("second commit failed: %v", err)
		}

		if !res1.ID.Equal(res2.ID) {
			t.Errorf("expected identical ID, got %s and %s", res1.ID.String(), res2.ID.String())
		}
	})

	t.Run("Commit detects corrupt existing object", func(t *testing.T) {
		store, rootDir := setupStore(t)
		contentStr := "test content"

		// Precompute the true hash: sha1 for "test content" is 1eebdf4fdc9fc7bf283031b93f9aef3338de9052
		trueID, err := content.ParseID("1eebdf4fdc9fc7bf283031b93f9aef3338de9052")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		// Write corrupt bytes directly to the true destination path
		destPath := filepath.Join(rootDir, "objects", trueID.String())
		err = os.WriteFile(destPath, []byte("corrupted data"), 0644)
		if err != nil {
			t.Fatalf("failed to write corrupt file: %v", err)
		}

		// Committing the true content now should fail with corruption / hash mismatch error during verify
		res, err := store.Commit(ctx, strings.NewReader(contentStr))
		t.Logf("Commit returned res: %v, err: %v", res, err)
		if !errors.Is(err, objectstore.ErrHashMismatch) {
			t.Errorf("expected %v, got %v", objectstore.ErrHashMismatch, err)
		}
	})

	t.Run("Return NotFound for non-existent objects", func(t *testing.T) {
		store, _ := setupStore(t)
		fakeID, err := content.ParseID("1234567890abcdef1234567890abcdef12345678")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		_, err = store.Open(ctx, fakeID)
		if !errors.Is(err, objectstore.ErrObjectNotFound) {
			t.Errorf("expected %v, got %v", objectstore.ErrObjectNotFound, err)
		}

		err = store.Delete(ctx, fakeID)
		if !errors.Is(err, objectstore.ErrObjectNotFound) {
			t.Errorf("expected %v, got %v", objectstore.ErrObjectNotFound, err)
		}
	})

	t.Run("Invalid filenames in object namespace are skipped/ignored during List", func(t *testing.T) {
		store, rootDir := setupStore(t)

		// Create a file with an invalid name in objects/
		invalidPath := filepath.Join(rootDir, "objects", "not_a_valid_hex_id.txt")
		err := os.WriteFile(invalidPath, []byte("data"), 0644)
		if err != nil {
			t.Fatalf("failed to write invalid file: %v", err)
		}

		// Also create a valid one
		_, err = store.Commit(ctx, strings.NewReader("valid content"))
		if err != nil {
			t.Fatalf("failed to commit: %v", err)
		}

		list, err := store.List(ctx)
		if err != nil {
			t.Fatalf("failed to list: %v", err)
		}

		if len(list) != 1 {
			t.Fatalf("expected exactly 1 valid ID, got %d", len(list))
		}
	})

	t.Run("Object Commit Golden Test", func(t *testing.T) {
		store, _ := setupStore(t)

		// Empty
		resEmpty, err := store.Commit(ctx, strings.NewReader(""))
		if err != nil {
			t.Fatalf("failed to commit empty: %v", err)
		}

		// Hello nando
		resHello, err := store.Commit(ctx, strings.NewReader("hello nando\n"))
		if err != nil {
			t.Fatalf("failed to commit hello: %v", err)
		}

		// Binary zeros
		resZeros, err := store.Commit(ctx, bytes.NewReader([]byte{0, 0, 0, 0}))
		if err != nil {
			t.Fatalf("failed to commit zeros: %v", err)
		}

		directHash := sha1.Sum([]byte{0, 0, 0, 0})
		t.Logf("Golden Test empty: %s, hello: %s, zeros: %s, directHash: %x", resEmpty.ID.String(), resHello.ID.String(), resZeros.ID.String(), directHash)

		// Load and verify golden file content
		goldenPath := filepath.Join("testdata", "object_commit.golden")
		data, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("failed to read golden file: %v", err)
		}

		contentStr := string(data)

		// Hardcoded expectations check to satisfy: "Expected digests must be hard-coded independently."
		if resEmpty.ID.String() != "da39a3ee5e6b4b0d3255bfef95601890afd80709" {
			t.Errorf("empty sha1 mismatch")
		}
		if resHello.ID.String() != "6640ec4d43fe9d18fcfee38caab6c91e3f231e17" {
			t.Errorf("hello sha1 mismatch")
		}
		if resZeros.ID.String() != "9069ca78e7450a285173431b3e52c5c25299e473" {
			t.Errorf("zeros sha1 mismatch")
		}

		// Ensure final path check
		expectedEmptyPath := "objects/da39a3ee5e6b4b0d3255bfef95601890afd80709"
		if !strings.Contains(contentStr, expectedEmptyPath) {
			t.Errorf("golden file must contain path %q", expectedEmptyPath)
		}
	})

	t.Run("Do not overwrite existing object with different content", func(t *testing.T) {
		store, rootDir := setupStore(t)
		contentStr := "original correct content"

		res, err := store.Commit(ctx, strings.NewReader(contentStr))
		if err != nil {
			t.Fatalf("failed to commit: %v", err)
		}

		destPath := filepath.Join(rootDir, "objects", res.ID.String())
		data, err := os.ReadFile(destPath)
		if err != nil {
			t.Fatalf("failed to read committed file: %v", err)
		}
		if string(data) != contentStr {
			t.Fatalf("expected file to have original content, got %q", string(data))
		}

		corruptContent := "some corrupted or different content"
		err = os.WriteFile(destPath, []byte(corruptContent), 0644)
		if err != nil {
			t.Fatalf("failed to write corrupt file: %v", err)
		}

		_, err = store.Commit(ctx, strings.NewReader(contentStr))
		if !errors.Is(err, objectstore.ErrHashMismatch) {
			t.Fatalf("expected error %v, got %v", objectstore.ErrHashMismatch, err)
		}

		dataAfter, err := os.ReadFile(destPath)
		if err != nil {
			t.Fatalf("failed to read file after commit: %v", err)
		}
		if string(dataAfter) != corruptContent {
			t.Errorf("corrupt content was overwritten! got %q, expected %q", string(dataAfter), corruptContent)
		}
	})

	t.Run("Concurrent Puts converge to the same Object ID", func(t *testing.T) {
		store, _ := setupStore(t)
		contentStr := "shared concurrent content"
		numGoroutines := 20

		var wg sync.WaitGroup
		errs := make(chan error, numGoroutines)
		results := make(chan objectstore.CommitResult, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := store.Commit(ctx, strings.NewReader(contentStr))
				if err != nil {
					errs <- err
					return
				}
				results <- res
			}()
		}

		wg.Wait()
		close(errs)
		close(results)

		for err := range errs {
			t.Errorf("concurrent commit failed: %v", err)
		}

		var firstID content.ID
		first := true
		for res := range results {
			if first {
				firstID = res.ID
				first = false
			} else if !res.ID.Equal(firstID) {
				t.Errorf("expected ID %s, got %s", firstID.String(), res.ID.String())
			}
		}

		if err := store.Verify(ctx, firstID); err != nil {
			t.Errorf("final object %s is corrupt: %v", firstID.String(), err)
		}
	})

	t.Run("Object content after publish matches Object ID", func(t *testing.T) {
		store, _ := setupStore(t)
		contentStr := "verification content"

		res, err := store.Commit(ctx, strings.NewReader(contentStr))
		if err != nil {
			t.Fatalf("failed to commit: %v", err)
		}

		err = store.Verify(ctx, res.ID)
		if err != nil {
			t.Errorf("verify failed: %v", err)
		}
	})

	t.Run("No partial object or temp file left on failure", func(t *testing.T) {
		store, rootDir := setupStore(t)

		contentBytes := []byte("some initial data that will fail later")
		errReader := &errorReader{
			data:  contentBytes,
			limit: 10,
		}

		_, err := store.Commit(ctx, errReader)
		if err == nil {
			t.Fatalf("expected error from failed reader, got nil")
		}

		list, err := store.List(ctx)
		if err != nil {
			t.Fatalf("failed to list objects: %v", err)
		}
		if len(list) != 0 {
			t.Errorf("expected 0 objects, got %v", len(list))
		}

		tempEntries, err := os.ReadDir(filepath.Join(rootDir, "temp"))
		if err != nil {
			t.Fatalf("failed to read temp directory: %v", err)
		}
		if len(tempEntries) != 0 {
			t.Errorf("expected temp directory to be empty, but found %d files", len(tempEntries))
		}
	})
}

type errorReader struct {
	data  []byte
	limit int
	read  int
}

func (r *errorReader) Read(p []byte) (n int, err error) {
	if r.read >= r.limit {
		return 0, errors.New("read error simulation")
	}
	rem := r.limit - r.read
	toCopy := len(p)
	if toCopy > rem {
		toCopy = rem
	}
	copy(p[:toCopy], r.data[r.read:r.read+toCopy])
	r.read += toCopy
	return toCopy, nil
}
