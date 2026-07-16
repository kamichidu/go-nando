package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/metadata/sqlite"
)

func TestSQLiteStore(t *testing.T) {
	ctx := context.Background()

	setupStore := func(t *testing.T) (*sqlite.Store, string) {
		t.Helper()
		tempDir := t.TempDir()
		dbPath := filepath.Join(tempDir, "metadata.sqlite")
		store, err := sqlite.NewStore(dbPath)
		if err != nil {
			t.Fatalf("failed to create store: %v", err)
		}

		err = store.Initialize(ctx)
		if err != nil {
			t.Fatalf("failed to initialize schema: %v", err)
		}

		return store, dbPath
	}

	t.Run("Initialize schema and put/get entry successfully", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		entry := metadata.Entry{
			Path:        "/docs/report.pdf",
			Hash:        "abcde1234567890fghij",
			Size:        12345,
			Mtime:       time.Now().UTC().Truncate(time.Second),
			Mode:        0644,
			ContentType: "application/pdf",
			State:       metadata.StateReady,
			IsDir:       false,
			CreatedAt:   time.Now().UTC().Truncate(time.Second),
			UpdatedAt:   time.Now().UTC().Truncate(time.Second),
		}

		err := store.PutEntry(ctx, entry)
		if err != nil {
			t.Fatalf("failed to put entry: %v", err)
		}

		retrieved, err := store.GetEntry(ctx, "/docs/report.pdf")
		if err != nil {
			t.Fatalf("failed to get entry: %v", err)
		}

		if retrieved.Path != entry.Path || retrieved.Hash != entry.Hash || retrieved.Size != entry.Size {
			t.Errorf("mismatched entry field values")
		}
		if !retrieved.Mtime.Equal(entry.Mtime) {
			t.Errorf("expected mtime %v, got %v", entry.Mtime, retrieved.Mtime)
		}
	})

	t.Run("Updating an existing path updates metadata", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		entry1 := metadata.Entry{
			Path:      "/docs/report.pdf",
			Hash:      "hash1",
			Size:      100,
			Mtime:     time.Now().UTC().Truncate(time.Second),
			State:     metadata.StateReady,
			CreatedAt: time.Now().UTC().Truncate(time.Second),
			UpdatedAt: time.Now().UTC().Truncate(time.Second),
		}
		_ = store.PutEntry(ctx, entry1)

		entry2 := entry1
		entry2.Hash = "hash2"
		entry2.Size = 200

		err := store.PutEntry(ctx, entry2)
		if err != nil {
			t.Fatalf("failed to update entry: %v", err)
		}

		retrieved, _ := store.GetEntry(ctx, "/docs/report.pdf")
		if retrieved.Hash != "hash2" || retrieved.Size != 200 {
			t.Errorf("entry was not updated correctly, got hash %q, size %d", retrieved.Hash, retrieved.Size)
		}
	})

	t.Run("Rename/Move entry updates path but not hash", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		entry := metadata.Entry{
			Path:      "/docs/report.pdf",
			Hash:      "myhash",
			State:     metadata.StateReady,
			Mtime:     time.Now().UTC().Truncate(time.Second),
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		_ = store.PutEntry(ctx, entry)

		err := store.MoveEntry(ctx, "/docs/report.pdf", "/docs/final_report.pdf")
		if err != nil {
			t.Fatalf("failed to move entry: %v", err)
		}

		// Old path should not exist (since state is ready, old path was completely updated)
		_, err = store.GetEntry(ctx, "/docs/report.pdf")
		if !errors.Is(err, metadata.ErrEntryNotFound) {
			t.Errorf("expected %v for old path, got %v", metadata.ErrEntryNotFound, err)
		}

		// New path should have the same hash
		retrieved, err := store.GetEntry(ctx, "/docs/final_report.pdf")
		if err != nil {
			t.Fatalf("failed to get entry at new path: %v", err)
		}
		if retrieved.Hash != "myhash" {
			t.Errorf("expected hash 'myhash', got %q", retrieved.Hash)
		}
	})

	t.Run("Delete entry removes hash from ReferencedHashes", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		entry := metadata.Entry{
			Path:      "/docs/report.pdf",
			Hash:      "unique_hash",
			State:     metadata.StateReady,
			Mtime:     time.Now().UTC().Truncate(time.Second),
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		_ = store.PutEntry(ctx, entry)

		hashes, _ := store.ReferencedHashes(ctx)
		if len(hashes) != 1 || hashes[0] != "unique_hash" {
			t.Fatalf("expected 1 referenced hash, got %v", hashes)
		}

		err := store.DeleteEntry(ctx, "/docs/report.pdf")
		if err != nil {
			t.Fatalf("failed to delete entry: %v", err)
		}

		// Should not be retrievable
		_, err = store.GetEntry(ctx, "/docs/report.pdf")
		if !errors.Is(err, metadata.ErrEntryNotFound) {
			t.Errorf("expected %v, got %v", metadata.ErrEntryNotFound, err)
		}

		// Hash should be removed from ReferencedHashes
		hashes, _ = store.ReferencedHashes(ctx)
		if len(hashes) != 0 {
			t.Errorf("expected 0 referenced hashes after delete, got %v", hashes)
		}
	})

	t.Run("Move directory renames children recursively", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		// Create a directory entry and two child entries
		now := time.Now().UTC().Truncate(time.Second)
		_ = store.PutEntry(ctx, metadata.Entry{Path: "/docs", IsDir: true, State: metadata.StateReady, Mtime: now, CreatedAt: now, UpdatedAt: now})
		_ = store.PutEntry(ctx, metadata.Entry{Path: "/docs/a.txt", Hash: "hash_a", State: metadata.StateReady, Mtime: now, CreatedAt: now, UpdatedAt: now})
		_ = store.PutEntry(ctx, metadata.Entry{Path: "/docs/b.txt", Hash: "hash_b", State: metadata.StateReady, Mtime: now, CreatedAt: now, UpdatedAt: now})

		err := store.MoveEntry(ctx, "/docs", "/documents")
		if err != nil {
			t.Fatalf("failed to move directory: %v", err)
		}

		// Check new paths exist
		_, err = store.GetEntry(ctx, "/documents")
		if err != nil {
			t.Errorf("new dir path not found: %v", err)
		}
		entryA, err := store.GetEntry(ctx, "/documents/a.txt")
		if err != nil || entryA.Hash != "hash_a" {
			t.Errorf("child A not moved correctly: %v", err)
		}
		entryB, err := store.GetEntry(ctx, "/documents/b.txt")
		if err != nil || entryB.Hash != "hash_b" {
			t.Errorf("child B not moved correctly: %v", err)
		}

		// Check old paths do not exist
		_, err = store.GetEntry(ctx, "/docs/a.txt")
		if !errors.Is(err, metadata.ErrEntryNotFound) {
			t.Errorf("old child path should not exist")
		}
	})

	t.Run("ListEntries returns immediate children only", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		now := time.Now().UTC().Truncate(time.Second)
		_ = store.PutEntry(ctx, metadata.Entry{Path: "/docs/report.pdf", State: metadata.StateReady, Mtime: now, CreatedAt: now, UpdatedAt: now})
		_ = store.PutEntry(ctx, metadata.Entry{Path: "/docs/image.png", State: metadata.StateReady, Mtime: now, CreatedAt: now, UpdatedAt: now})
		_ = store.PutEntry(ctx, metadata.Entry{Path: "/docs/sub/nested.txt", State: metadata.StateReady, Mtime: now, CreatedAt: now, UpdatedAt: now})

		entries, err := store.ListEntries(ctx, "/docs")
		if err != nil {
			t.Fatalf("failed to list entries: %v", err)
		}

		if len(entries) != 2 {
			t.Fatalf("expected 2 immediate children, got %d", len(entries))
		}

		for _, e := range entries {
			if e.Path == "/docs/sub/nested.txt" {
				t.Errorf("listed nested child which is not immediate")
			}
		}
	})
}
