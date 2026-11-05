package local_test

import (
	"bytes"
	"context"
	"crypto/sha256"
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

type invalidIDHasher struct{}

func (invalidIDHasher) Sum(context.Context, io.Reader) (content.ID, error) {
	return content.ID{}, nil
}

type errorHasher struct{ err error }

func (h errorHasher) Sum(context.Context, io.Reader) (content.ID, error) {
	return content.ID{}, h.err
}

type readerThenError struct {
	read bool
	err  error
}

func (r *readerThenError) Read(p []byte) (int, error) {
	if r.read {
		return 0, r.err
	}
	r.read = true
	return copy(p, []byte("partial object")), nil
}

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

	t.Run("Zero ID is rejected at object store boundaries", func(t *testing.T) {
		store, _ := setupStore(t)
		var zero content.ID
		if _, err := store.Open(ctx, zero); !errors.Is(err, content.ErrInvalidID) {
			t.Fatalf("Open should reject zero ID, got %v", err)
		}
		if _, err := store.Exists(ctx, zero); !errors.Is(err, content.ErrInvalidID) {
			t.Fatalf("Exists should reject zero ID, got %v", err)
		}
		if err := store.Delete(ctx, zero); !errors.Is(err, content.ErrInvalidID) {
			t.Fatalf("Delete should reject zero ID, got %v", err)
		}
		if err := store.Verify(ctx, zero); !errors.Is(err, content.ErrInvalidID) {
			t.Fatalf("Verify should reject zero ID, got %v", err)
		}
	})

	t.Run("Commit rejects an invalid ID returned by its hasher", func(t *testing.T) {
		root := t.TempDir()
		store, err := local.NewStore(filepath.Join(root, "objects"), filepath.Join(root, "temp"), invalidIDHasher{})
		if err != nil {
			t.Fatalf("failed to create store: %v", err)
		}
		if _, err := store.Commit(ctx, strings.NewReader("data")); !errors.Is(err, content.ErrInvalidID) {
			t.Fatalf("expected invalid hasher ID to be rejected, got %v", err)
		}
		raw, err := store.ListRaw(ctx)
		if err != nil || len(raw) != 0 {
			t.Fatalf("invalid ID must not publish an object path, raw=%v err=%v", raw, err)
		}
	})

	t.Run("Commit and Open successful", func(t *testing.T) {
		store, _ := setupStore(t)
		contentStr := "test object content"

		res, err := store.Commit(ctx, strings.NewReader(contentStr))
		if err != nil {
			t.Fatalf("failed to commit: %v", err)
		}

		expectedID, err := content.ParseID("sha256-5d8b5e6fda5d36d6c59cc1cab7e31a59b9d135f109a6a3964624f3d65d9f0844")
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
		exists, err := store.Exists(ctx, res.ID)
		if err != nil || !exists {
			t.Fatalf("successful Commit must be immediately visible to Exists, exists=%t err=%v", exists, err)
		}

		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("failed to read: %v", err)
		}
		if string(data) != contentStr {
			t.Errorf("expected content %q, got %q", contentStr, string(data))
		}
	})

	t.Run("Failed writes and hashing remove private staging files", func(t *testing.T) {
		store, root := setupStore(t)
		readErr := errors.New("injected source read failure")
		if _, err := store.Commit(ctx, &readerThenError{err: readErr}); !errors.Is(err, readErr) {
			t.Fatalf("expected injected read failure, got %v", err)
		}
		assertStoreDirsEmpty(t, root)

		hashErr := errors.New("injected hash failure")
		objectsDir := filepath.Join(root, "objects")
		tempDir := filepath.Join(root, "temp")
		failingStore, err := local.NewStore(objectsDir, tempDir, errorHasher{err: hashErr})
		if err != nil {
			t.Fatalf("failed to create store: %v", err)
		}
		if _, err := failingStore.Commit(ctx, strings.NewReader("complete temporary bytes")); !errors.Is(err, hashErr) {
			t.Fatalf("expected injected hash failure, got %v", err)
		}
		assertStoreDirsEmpty(t, root)
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

		// SHA-256 of "test content" is declared independently from the store.
		trueID, err := content.ParseID("sha256-6ae8a75555209fd6c44157c0aed8016e763ff435a19cf186f76863140143ff72")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		// Write corrupt bytes directly to the true destination path
		destPath := filepath.Join(rootDir, "objects", trueID.String())
		err = os.WriteFile(destPath, []byte("corrupted data"), 0o644)
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
		fakeID, err := content.ParseID("sha256-4c09422b949a71723f72af315de016346cc6abdc208ffecde026dc00bd506674")
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
		err := os.WriteFile(invalidPath, []byte("data"), 0o644)
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

		directHash := sha256.Sum256([]byte{0, 0, 0, 0})
		t.Logf("Golden Test empty: %s, hello: %s, zeros: %s, directHash: %x", resEmpty.ID.String(), resHello.ID.String(), resZeros.ID.String(), directHash)

		// Load and verify golden file content
		goldenPath := filepath.Join("testdata", "object_commit.golden")
		data, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("failed to read golden file: %v", err)
		}

		contentStr := string(data)

		// Hardcoded expectations check to satisfy: "Expected digests must be hard-coded independently."
		if resEmpty.ID.String() != "sha256-e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
			t.Errorf("empty sha256 mismatch")
		}
		if resHello.ID.String() != "sha256-4b234e4b38341d10a8135b4a0af020a2c4db56b805272fd0fde62c568c8dd77e" {
			t.Errorf("hello sha256 mismatch")
		}
		if resZeros.ID.String() != "sha256-df3f619804a92fdb4057192dc43dd748ea778adc52bc498ce80524c014b81119" {
			t.Errorf("zeros sha256 mismatch")
		}

		// Ensure final path check
		expectedEmptyPath := "objects/sha256-e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
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
		err = os.WriteFile(destPath, []byte(corruptContent), 0o644)
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

		hasErrors := false
		for err := range errs {
			t.Errorf("concurrent commit failed: %v", err)
			hasErrors = true
		}
		if hasErrors {
			t.Fatalf("at least one concurrent commit failed")
		}

		var commits []objectstore.CommitResult
		for res := range results {
			commits = append(commits, res)
		}

		if len(commits) != numGoroutines {
			t.Fatalf("expected %d successful commits, got %d", numGoroutines, len(commits))
		}

		if len(commits) == 0 {
			t.Fatalf("no successful commits were returned")
		}

		firstID := commits[0].ID
		for i, res := range commits {
			if !res.ID.Equal(firstID) {
				t.Errorf("commit %d ID mismatch: expected %s, got %s", i, firstID.String(), res.ID.String())
			}
		}

		if err := store.Verify(ctx, firstID); err != nil {
			t.Errorf("final object %s is corrupt: %v", firstID.String(), err)
		}
	})

	t.Run("Existing valid object is reused on subsequent commit", func(t *testing.T) {
		store, rootDir := setupStore(t)
		contentStr := "reusable object content"

		// 1. First commit
		res1, err := store.Commit(ctx, strings.NewReader(contentStr))
		if err != nil {
			t.Fatalf("first commit failed: %v", err)
		}

		destPath := filepath.Join(rootDir, "objects", res1.ID.String())

		// Read content to establish baseline
		dataBefore, err := os.ReadFile(destPath)
		if err != nil {
			t.Fatalf("failed to read baseline: %v", err)
		}

		// 2. Second commit of identical content
		res2, err := store.Commit(ctx, strings.NewReader(contentStr))
		if err != nil {
			t.Fatalf("second commit failed: %v", err)
		}

		// 3. Both succeed and return matching Object IDs
		if !res1.ID.Equal(res2.ID) {
			t.Errorf("expected matching Object IDs, got %s and %s", res1.ID.String(), res2.ID.String())
		}

		// 4. Final object content has not changed
		dataAfter, err := os.ReadFile(destPath)
		if err != nil {
			t.Fatalf("failed to read content after second commit: %v", err)
		}
		if string(dataAfter) != string(dataBefore) {
			t.Errorf("final object content changed! got %q, expected %q", string(dataAfter), string(dataBefore))
		}

		// 5. Can successfully verify final object
		if err := store.Verify(ctx, res1.ID); err != nil {
			t.Errorf("verification of final object failed: %v", err)
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

	t.Run("Reader failure leaves no object or temp file", func(t *testing.T) {
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

func assertStoreDirsEmpty(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{"objects", "temp"} {
		entries, err := os.ReadDir(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("failed to read %s directory: %v", name, err)
		}
		if len(entries) != 0 {
			t.Errorf("%s directory should be empty after failed Commit, entries=%v", name, entries)
		}
	}
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
