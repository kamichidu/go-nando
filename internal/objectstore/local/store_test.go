package local_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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

	t.Run("Put and Open successful", func(t *testing.T) {
		store, _ := setupStore(t)
		contentStr := "test object content"
		digest, _ := hasher.Sum(ctx, strings.NewReader(contentStr))

		err := store.Put(ctx, digest.String(), strings.NewReader(contentStr))
		if err != nil {
			t.Fatalf("failed to put object: %v", err)
		}

		// Open and verify content
		rc, err := store.Open(ctx, digest.String())
		if err != nil {
			t.Fatalf("failed to open object: %v", err)
		}
		defer rc.Close()

		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("failed to read object content: %v", err)
		}

		if string(data) != contentStr {
			t.Errorf("expected %q, got %q", contentStr, string(data))
		}
	})

	t.Run("Put multiple times does not break", func(t *testing.T) {
		store, _ := setupStore(t)
		contentStr := "stable content"
		digest, _ := hasher.Sum(ctx, strings.NewReader(contentStr))

		err := store.Put(ctx, digest.String(), strings.NewReader(contentStr))
		if err != nil {
			t.Fatalf("first put failed: %v", err)
		}

		// Second put (same content) should return nil and succeed gracefully
		err = store.Put(ctx, digest.String(), strings.NewReader(contentStr))
		if err != nil {
			t.Fatalf("second put failed: %v", err)
		}

		// Content is still there
		rc, err := store.Open(ctx, digest.String())
		if err != nil {
			t.Fatalf("open after second put failed: %v", err)
		}
		defer rc.Close()
	})

	t.Run("Detect mismatching hash", func(t *testing.T) {
		store, _ := setupStore(t)
		contentStr := "actual content"
		wrongHash := "da39a3ee5e6b4b0d3255bfef95601890afd80709" // hash of empty string

		// Put with wrong hash
		err := store.Put(ctx, wrongHash, strings.NewReader(contentStr))
		if err != nil {
			t.Fatalf("failed to put: %v", err)
		}

		// Verify should fail
		err = store.Verify(ctx, wrongHash)
		if !errors.Is(err, objectstore.ErrHashMismatch) {
			t.Errorf("expected %v, got %v", objectstore.ErrHashMismatch, err)
		}
	})

	t.Run("Return NotFound for non-existent objects", func(t *testing.T) {
		store, _ := setupStore(t)
		fakeHash := "1234567890abcdef1234567890abcdef12345678"

		_, err := store.Open(ctx, fakeHash)
		if !errors.Is(err, objectstore.ErrObjectNotFound) {
			t.Errorf("expected %v, got %v", objectstore.ErrObjectNotFound, err)
		}

		err = store.Delete(ctx, fakeHash)
		if !errors.Is(err, objectstore.ErrObjectNotFound) {
			t.Errorf("expected %v on delete, got %v", objectstore.ErrObjectNotFound, err)
		}
	})

	t.Run("Do not treat temporary files as completed objects", func(t *testing.T) {
		store, rootDir := setupStore(t)

		// Create a fake temporary file in the temp directory
		tempFile, err := os.CreateTemp(filepath.Join(rootDir, "temp"), "nando-obj-*")
		if err != nil {
			t.Fatalf("failed to create dummy temp file: %v", err)
		}
		_ = tempFile.Close()

		// Attempting to Open it via store with its filename should fail as NotFound
		_, err = store.Open(ctx, filepath.Base(tempFile.Name()))
		if !errors.Is(err, objectstore.ErrObjectNotFound) {
			t.Errorf("expected %v when opening temp file, got %v", objectstore.ErrObjectNotFound, err)
		}
	})
}
