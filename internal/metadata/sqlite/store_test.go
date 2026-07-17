package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/metadata"
)

func TestSQLiteStore(t *testing.T) {
	ctx := context.Background()

	setupStore := func(t *testing.T) (*Store, string) {
		t.Helper()
		tempDir := t.TempDir()
		dbPath := filepath.Join(tempDir, "metadata.sqlite")
		store, err := NewStore(dbPath)
		if err != nil {
			t.Fatalf("failed to create store: %v", err)
		}

		err = store.Initialize(ctx)
		if err != nil {
			t.Fatalf("failed to initialize schema: %v", err)
		}

		return store, dbPath
	}

	t.Run("Initialize schema and Root entry successfully", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		root, err := store.Root(ctx)
		if err != nil {
			t.Fatalf("failed to get root: %v", err)
		}

		if root.ID != "root" {
			t.Errorf("expected root ID 'root', got %q", root.ID)
		}
		if root.ParentID != nil {
			t.Errorf("expected root parent ID to be nil")
		}
		if root.Kind != metadata.EntryKindDirectory {
			t.Errorf("expected root kind 'directory', got %q", root.Kind)
		}
	})

	t.Run("Create and Get child successfully", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		parentID := metadata.EntryID("root")
		childID := metadata.EntryID("child-1")
		now := time.Now().UTC().Truncate(time.Second)

		oid, err := content.ParseID("da39a3ee5e6b4b0d3255bfef95601890afd80709")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}
		entry := metadata.Entry{
			ID:          childID,
			ParentID:    &parentID,
			Name:        "report.pdf",
			Kind:        metadata.EntryKindFile,
			ObjectID:    &oid,
			Size:        100,
			Mtime:       now,
			Mode:        0644,
			ContentType: "application/pdf",
			Revision:    1,
			CreatedAt:   now,
			UpdatedAt:   now,
		}

		_, err = store.Create(ctx, entry)
		if err != nil {
			t.Fatalf("failed to create entry: %v", err)
		}

		// Retrieve by ID
		retrieved, err := store.GetByID(ctx, childID)
		if err != nil {
			t.Fatalf("failed to get by ID: %v", err)
		}
		if retrieved.Name != "report.pdf" || !retrieved.ObjectID.Equal(oid) {
			t.Errorf("mismatched retrieved entry")
		}

		// Retrieve child by parent and name
		child, err := store.GetChild(ctx, parentID, "report.pdf")
		if err != nil {
			t.Fatalf("failed to get child: %v", err)
		}
		if child.ID != childID {
			t.Errorf("expected ID %s, got %s", childID.String(), child.ID.String())
		}
	})

	t.Run("Duplicate child name conflict", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		parentID := metadata.EntryID("root")
		now := time.Now().UTC()
		oid, err := content.ParseID("da39a3ee5e6b4b0d3255bfef95601890afd80709")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		entry1 := metadata.Entry{
			ID:        "id1",
			ParentID:  &parentID,
			Name:      "dup.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oid,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		}

		entry2 := metadata.Entry{
			ID:        "id2",
			ParentID:  &parentID,
			Name:      "dup.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oid,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		}

		_, err = store.Create(ctx, entry1)
		if err != nil {
			t.Fatalf("failed to create entry1: %v", err)
		}

		_, err = store.Create(ctx, entry2)
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected conflict error, got: %v", err)
		}
	})

	t.Run("Same name under different parents is allowed", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		now := time.Now().UTC()
		rootID := metadata.EntryID("root")
		oid, err := content.ParseID("da39a3ee5e6b4b0d3255bfef95601890afd80709")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		// Create parent dir
		pID := metadata.EntryID("parent-dir")
		_, err = store.Create(ctx, metadata.Entry{
			ID:        pID,
			ParentID:  &rootID,
			Name:      "subdir",
			Kind:      metadata.EntryKindDirectory,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create parent dir: %v", err)
		}

		// Create under root
		_, err = store.Create(ctx, metadata.Entry{
			ID:        "child-under-root",
			ParentID:  &rootID,
			Name:      "test.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oid,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create under root: %v", err)
		}

		// Create under subdir
		_, err = store.Create(ctx, metadata.Entry{
			ID:        "child-under-subdir",
			ParentID:  &pID,
			Name:      "test.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oid,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create under subdir: %v", err)
		}
	})

	t.Run("Update entry increments revision and changes attributes", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		parentID := metadata.EntryID("root")
		id := metadata.EntryID("file-id")
		now := time.Now().UTC()
		oid, err := content.ParseID("da39a3ee5e6b4b0d3255bfef95601890afd80709")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		entry, err := store.Create(ctx, metadata.Entry{
			ID:        id,
			ParentID:  &parentID,
			Name:      "file.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oid,
			Size:      10,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create: %v", err)
		}

		entry.Size = 20
		updated, err := store.Update(ctx, entry)
		if err != nil {
			t.Fatalf("failed to update: %v", err)
		}

		if updated.Revision != 2 {
			t.Errorf("expected revision 2, got %d", updated.Revision)
		}
		if updated.Size != 20 {
			t.Errorf("expected size 20, got %d", updated.Size)
		}
	})

	t.Run("Update rejects non-existent parent", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		parentID := metadata.EntryID("root")
		id := metadata.EntryID("file-id")
		now := time.Now().UTC()
		oid, err := content.ParseID("da39a3ee5e6b4b0d3255bfef95601890afd80709")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		entry, err := store.Create(ctx, metadata.Entry{
			ID:        id,
			ParentID:  &parentID,
			Name:      "file.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oid,
			Size:      10,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create: %v", err)
		}

		// Update to a non-existent parent
		nonExistentParent := metadata.EntryID("non-existent")
		entry.ParentID = &nonExistentParent

		_, err = store.Update(ctx, entry)
		if !errors.Is(err, metadata.ErrEntryConflict) || !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("expected ErrEntryConflict for non-existent parent, got: %v", err)
		}
	})

	t.Run("Update rejects file as parent", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		parentID := metadata.EntryID("root")
		id1 := metadata.EntryID("file-id-1")
		id2 := metadata.EntryID("file-id-2")
		now := time.Now().UTC()
		oid, err := content.ParseID("da39a3ee5e6b4b0d3255bfef95601890afd80709")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		// Create file 1
		file1, err := store.Create(ctx, metadata.Entry{
			ID:        id1,
			ParentID:  &parentID,
			Name:      "file1.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oid,
			Size:      10,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create file 1: %v", err)
		}

		// Create file 2
		file2, err := store.Create(ctx, metadata.Entry{
			ID:        id2,
			ParentID:  &parentID,
			Name:      "file2.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oid,
			Size:      10,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create file 2: %v", err)
		}

		// Update file 2 to have file 1 as its parent
		file2.ParentID = &file1.ID

		_, err = store.Update(ctx, file2)
		if !errors.Is(err, metadata.ErrEntryConflict) || !strings.Contains(err.Error(), "must be a directory") {
			t.Errorf("expected ErrEntryConflict for file as parent, got: %v", err)
		}
	})

	t.Run("Update rejects directory hierarchy cycle", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		parentID := metadata.EntryID("root")
		dirAID := metadata.EntryID("dir-A")
		dirBID := metadata.EntryID("dir-B")
		now := time.Now().UTC()

		// Create dir-A
		dirA, err := store.Create(ctx, metadata.Entry{
			ID:        dirAID,
			ParentID:  &parentID,
			Name:      "dirA",
			Kind:      metadata.EntryKindDirectory,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create dir-A: %v", err)
		}

		// Create dir-B under dir-A
		dirB, err := store.Create(ctx, metadata.Entry{
			ID:        dirBID,
			ParentID:  &dirAID,
			Name:      "dirB",
			Kind:      metadata.EntryKindDirectory,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create dir-B: %v", err)
		}

		// Update dir-A to have dir-B as parent (forming a cycle)
		dirA.ParentID = &dirB.ID

		_, err = store.Update(ctx, dirA)
		if !errors.Is(err, metadata.ErrEntryConflict) || !strings.Contains(err.Error(), "hierarchy cycle detected") {
			t.Errorf("expected ErrEntryConflict for hierarchy cycle, got: %v", err)
		}
	})

	t.Run("Move entry updates parent and/or name", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		rootID := metadata.EntryID("root")
		now := time.Now().UTC()
		oid, err := content.ParseID("da39a3ee5e6b4b0d3255bfef95601890afd80709")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		// Create subdir
		subID := metadata.EntryID("subdir")
		_, err = store.Create(ctx, metadata.Entry{
			ID:        subID,
			ParentID:  &rootID,
			Name:      "sub",
			Kind:      metadata.EntryKindDirectory,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create subdir: %v", err)
		}

		// Create file under root
		fileID := metadata.EntryID("file-id")
		_, err = store.Create(ctx, metadata.Entry{
			ID:        fileID,
			ParentID:  &rootID,
			Name:      "file.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oid,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create file: %v", err)
		}

		// Move file under subdir with new name
		moved, err := store.Move(ctx, fileID, subID, "moved_file.txt")
		if err != nil {
			t.Fatalf("failed to move: %v", err)
		}

		if *moved.ParentID != subID {
			t.Errorf("expected parent ID %s, got %s", subID.String(), moved.ParentID.String())
		}
		if moved.Name != "moved_file.txt" {
			t.Errorf("expected name moved_file.txt, got %q", moved.Name)
		}
		if moved.Revision != 2 {
			t.Errorf("expected revision 2, got %d", moved.Revision)
		}

		// Verifying original parent has no child with the old name
		_, err = store.GetChild(ctx, rootID, "file.txt")
		if !errors.Is(err, metadata.ErrEntryNotFound) {
			t.Errorf("expected not found for old parent name, got: %v", err)
		}

		// Check new parent has the child
		child, err := store.GetChild(ctx, subID, "moved_file.txt")
		if err != nil {
			t.Fatalf("failed to get child at new parent: %v", err)
		}
		if child.ID != fileID {
			t.Errorf("mismatched child ID")
		}
	})

	t.Run("Delete entry removes it from parent", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		rootID := metadata.EntryID("root")
		id := metadata.EntryID("del-id")
		now := time.Now().UTC()
		oid, err := content.ParseID("da39a3ee5e6b4b0d3255bfef95601890afd80709")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		_, err = store.Create(ctx, metadata.Entry{
			ID:        id,
			ParentID:  &rootID,
			Name:      "delete_me.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oid,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create: %v", err)
		}

		err = store.Delete(ctx, id)
		if err != nil {
			t.Fatalf("failed to delete: %v", err)
		}

		// Ensure no longer resolvable
		_, err = store.GetByID(ctx, id)
		if !errors.Is(err, metadata.ErrEntryNotFound) {
			t.Errorf("expected not found, got %v", err)
		}

		// Ensure name is reusable
		_, err = store.Create(ctx, metadata.Entry{
			ID:        "new-id-same-name",
			ParentID:  &rootID,
			Name:      "delete_me.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oid,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Errorf("failed to reuse deleted name: %v", err)
		}
	})

	t.Run("Metadata Schema Golden Test", func(t *testing.T) {
		store, dbPath := setupStore(t)
		defer store.Close()

		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("failed to open database directly: %v", err)
		}
		defer db.Close()

		// 1. Introspect table info
		rows, err := db.Query("PRAGMA table_info(entries)")
		if err != nil {
			t.Fatalf("table info query failed: %v", err)
		}
		defer rows.Close()

		var columnsLines []string
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dfltVal sql.NullString
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltVal, &pk); err != nil {
				t.Fatalf("scan failed: %v", err)
			}
			columnsLines = append(columnsLines, fmt.Sprintf("- %s %s pk=%d null=%d", name, ctype, pk, 1-notnull))
		}

		// 2. Introspect foreign key info
		fkRows, err := db.Query("PRAGMA foreign_key_list(entries)")
		if err != nil {
			t.Fatalf("fk list query failed: %v", err)
		}
		defer fkRows.Close()

		var fkLines []string
		for fkRows.Next() {
			var id, seq int
			var table, from, to string
			var onUpdate, onDelete, match string
			if err := fkRows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
				t.Fatalf("fk scan failed: %v", err)
			}
			fkLines = append(fkLines, fmt.Sprintf("- %s -> %s(%s)", from, table, to))
		}

		// Build a stable description
		var sb strings.Builder
		sb.WriteString("Table: entries\n")
		sb.WriteString("Columns:\n")
		for _, line := range columnsLines {
			sb.WriteString(line + "\n")
		}
		sb.WriteString("Foreign Keys:\n")
		for _, line := range fkLines {
			sb.WriteString(line + "\n")
		}
		sb.WriteString("Indexes:\n")
		sb.WriteString("- UNIQUE keys: parent_id, name\n")

		actual := sb.String()

		// Read golden file
		goldenPath := filepath.Join("testdata", "metadata_schema.golden")
		expectedBytes, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("failed to read golden file: %v", err)
		}

		expected := string(expectedBytes)
		if actual != expected {
			t.Errorf("Metadata schema mismatch!\nActual:\n%s\nExpected:\n%s", actual, expected)
		}
	})

	t.Run("Metadata Tree Golden Test", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		rootID := metadata.EntryID("root")
		now := time.Now().UTC()

		// /documents
		dirDocs := metadata.Entry{
			ID:        "dir-documents",
			ParentID:  &rootID,
			Name:      "documents",
			Kind:      metadata.EntryKindDirectory,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		}
		_, err := store.Create(ctx, dirDocs)
		if err != nil {
			t.Fatalf("failed to create documents: %v", err)
		}

		// /documents/hello.txt
		oidHello, err := content.ParseID("6640ec4d43fe9d18fcfee38caab6c91e3f231e17")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}
		fileHello := metadata.Entry{
			ID:        "file-hello",
			ParentID:  &dirDocs.ID,
			Name:      "hello.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oidHello,
			Revision:  3,
			CreatedAt: now,
			UpdatedAt: now,
		}
		_, err = store.Create(ctx, fileHello)
		if err != nil {
			t.Fatalf("failed to create hello: %v", err)
		}

		// /documents/archive
		dirArchive := metadata.Entry{
			ID:        "dir-archive",
			ParentID:  &dirDocs.ID,
			Name:      "archive",
			Kind:      metadata.EntryKindDirectory,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		}
		_, err = store.Create(ctx, dirArchive)
		if err != nil {
			t.Fatalf("failed to create archive: %v", err)
		}

		// /documents/archive/old.txt
		oidOld, err := content.ParseID("da39a3ee5e6b4b0d3255bfef95601890afd80709")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}
		fileOld := metadata.Entry{
			ID:        "file-old",
			ParentID:  &dirArchive.ID,
			Name:      "old.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oidOld,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		}
		_, err = store.Create(ctx, fileOld)
		if err != nil {
			t.Fatalf("failed to create old: %v", err)
		}

		// List all and check representation
		all, err := store.ListAll(ctx)
		if err != nil {
			t.Fatalf("failed to list all entries: %v", err)
		}

		// Create deterministic sorting map based on hierarchy
		// Root -> documents -> hello, archive -> old
		allMap := make(map[metadata.EntryID]metadata.Entry)
		for _, e := range all {
			allMap[e.ID] = e
		}

		order := []metadata.EntryID{"root", "dir-documents", "file-hello", "dir-archive", "file-old"}

		var sb strings.Builder
		for _, id := range order {
			e, ok := allMap[id]
			if !ok {
				t.Fatalf("missing entry in tree: %s", id.String())
			}
			pStr := "-"
			if e.ParentID != nil {
				pStr = e.ParentID.String()
			}
			objStr := ""
			if e.ObjectID != nil {
				objStr = " Object=" + e.ObjectID.String()
			}
			fmt.Fprintf(&sb, "ID=%s Parent=%s Kind=%s Name=%s Revision=%d%s\n", e.ID.String(), pStr, string(e.Kind), e.Name, e.Revision, objStr)
		}

		actual := sb.String()

		// Compare to golden file
		goldenPath := filepath.Join("testdata", "metadata_tree.golden")
		expectedBytes, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("failed to read golden file: %v", err)
		}

		expected := string(expectedBytes)
		if actual != expected {
			t.Errorf("Metadata Tree mismatch!\nActual:\n%s\nExpected:\n%s", actual, expected)
		}
	})

	t.Run("Obsolete schema detection", func(t *testing.T) {
		tempDir := t.TempDir()
		dbPath := filepath.Join(tempDir, "metadata.sqlite")
		store, err := NewStore(dbPath)
		if err != nil {
			t.Fatalf("failed to create: %v", err)
		}
		defer store.Close()

		// Write old obsolete path-based schema manually
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("failed to open database directly: %v", err)
		}
		defer db.Close()

		_, err = db.Exec(`
			CREATE TABLE entries (
				path TEXT PRIMARY KEY,
				hash TEXT NOT NULL,
				size INTEGER NOT NULL,
				mtime TEXT NOT NULL,
				mode INTEGER NOT NULL,
				content_type TEXT NOT NULL,
				state TEXT NOT NULL,
				is_dir INTEGER NOT NULL DEFAULT 0,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			);
		`)
		if err != nil {
			t.Fatalf("failed to create old schema: %v", err)
		}

		// Schema check must fail indicating obsolete Phase 0 schema
		err = store.CheckSchemaCompatibility(ctx)
		if err == nil {
			t.Errorf("expected incompatible schema error, got nil")
		} else if !strings.Contains(err.Error(), "incompatible obsolete Phase 0 repository format") {
			t.Errorf("expected incompatible schema error, got: %v", err)
		}
	})

	t.Run("MetadataStore contract validation", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		rootID := metadata.EntryID("root")
		now := time.Now().UTC()
		oid, err := content.ParseID("da39a3ee5e6b4b0d3255bfef95601890afd80709")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		// 3. Deleting a directory that has active children must fail in Go validation
		dirID := metadata.EntryID("parent-dir")
		_, err = store.Create(ctx, metadata.Entry{
			ID:        dirID,
			ParentID:  &rootID,
			Name:      "subdir",
			Kind:      metadata.EntryKindDirectory,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create directory: %v", err)
		}

		childID := metadata.EntryID("child-of-dir")
		_, err = store.Create(ctx, metadata.Entry{
			ID:        childID,
			ParentID:  &dirID,
			Name:      "child.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oid,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create child: %v", err)
		}

		// Delete parent-dir should fail because child-of-dir is still present
		err = store.Delete(ctx, dirID)
		if err == nil {
			t.Errorf("expected Delete on non-empty directory to fail, but it succeeded")
		} else if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected ErrEntryConflict, got: %v", err)
		}

		// 4. Self move (not tested here as it is checked by ValidatingStore)

		// 5. Descendant move (not tested here as it is checked by ValidatingStore)
		childDirID := metadata.EntryID("child-dir")
		_, err = store.Create(ctx, metadata.Entry{
			ID:        childDirID,
			ParentID:  &dirID,
			Name:      "child_subdir",
			Kind:      metadata.EntryKindDirectory,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create child directory: %v", err)
		}

		// 6. Delete on non-existent entry returns metadata.ErrEntryNotFound
		fakeID := metadata.EntryID("non-existent-entry")
		err = store.Delete(ctx, fakeID)
		if !errors.Is(err, metadata.ErrEntryNotFound) {
			t.Errorf("expected ErrEntryNotFound on non-existent delete, got: %v", err)
		}

		// 7. Delete on root returns metadata.ErrEntryConflict
		err = store.Delete(ctx, "root")
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected ErrEntryConflict on root delete, got: %v", err)
		}

		// 8. Can delete file, and then it is no longer resolvable
		err = store.Delete(ctx, childID)
		if err != nil {
			t.Fatalf("failed to delete child file: %v", err)
		}
		_, err = store.GetByID(ctx, childID)
		if !errors.Is(err, metadata.ErrEntryNotFound) {
			t.Errorf("expected ErrEntryNotFound after deletion, got: %v", err)
		}

		// Delete child subdirectory as well so that dirID has no children left
		err = store.Delete(ctx, childDirID)
		if err != nil {
			t.Fatalf("failed to delete child subdirectory: %v", err)
		}

		// 9. Can delete leaf directory (since all its children are now deleted, parent-dir is a leaf)
		err = store.Delete(ctx, dirID)
		if err != nil {
			t.Fatalf("failed to delete leaf directory: %v", err)
		}
		_, err = store.GetByID(ctx, dirID)
		if !errors.Is(err, metadata.ErrEntryNotFound) {
			t.Errorf("expected ErrEntryNotFound after leaf directory deletion, got: %v", err)
		}
	})

	t.Run("Concurrent Move operations cannot create mutual cycles", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		rootID := metadata.EntryID("root")
		now := time.Now().UTC()

		// Create dir-A under root
		dirA := metadata.Entry{
			ID:        "dir-A",
			ParentID:  &rootID,
			Name:      "dirA",
			Kind:      metadata.EntryKindDirectory,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		}
		_, err := store.Create(ctx, dirA)
		if err != nil {
			t.Fatalf("failed to create dir-A: %v", err)
		}

		// Create dir-B under root
		dirB := metadata.Entry{
			ID:        "dir-B",
			ParentID:  &rootID,
			Name:      "dirB",
			Kind:      metadata.EntryKindDirectory,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		}
		_, err = store.Create(ctx, dirB)
		if err != nil {
			t.Fatalf("failed to create dir-B: %v", err)
		}

		// Try to move A under B and B under A concurrently
		var wg sync.WaitGroup
		errs := make(chan error, 2)

		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := store.Move(ctx, "dir-A", "dir-B", "dirA")
			if err != nil {
				errs <- err
			}
		}()
		go func() {
			defer wg.Done()
			_, err := store.Move(ctx, "dir-B", "dir-A", "dirB")
			if err != nil {
				errs <- err
			}
		}()

		wg.Wait()
		close(errs)

		var errorsSlice []error
		for err := range errs {
			errorsSlice = append(errorsSlice, err)
		}

		t.Logf("Concurrent Move operations finished. Observed %d failures. Errors: %v", len(errorsSlice), errorsSlice)

		successCount := 2 - len(errorsSlice)
		if successCount == 0 {
			t.Fatalf("both move operations failed completely (successCount == 0). Errors: %v", errorsSlice)
		}

		// Helper to verify that an entry can reach "root" without cycles
		canReachRoot := func(id metadata.EntryID) bool {
			current := id
			visited := make(map[metadata.EntryID]bool)
			for current != "root" {
				if visited[current] {
					return false // Cycle detected!
				}
				visited[current] = true
				e, err := store.GetByID(ctx, current)
				if err != nil {
					return false // Missing parent!
				}
				if e.ParentID == nil {
					return false // No parent and not root!
				}
				current = *e.ParentID
			}
			return true
		}

		// Verify the directory structure is still valid and has no cycle
		if !canReachRoot("dir-A") {
			t.Errorf("dir-A cannot reach root safely (cycle/unreachable parent detected)")
		}
		if !canReachRoot("dir-B") {
			t.Errorf("dir-B cannot reach root safely (cycle/unreachable parent detected)")
		}

		// Double-check direct cycle
		pA, err := store.GetByID(ctx, "dir-A")
		if err != nil {
			t.Fatalf("failed to get dir-A: %v", err)
		}
		pB, err := store.GetByID(ctx, "dir-B")
		if err != nil {
			t.Fatalf("failed to get dir-B: %v", err)
		}

		if *pA.ParentID == "dir-B" && *pB.ParentID == "dir-A" {
			t.Errorf("mutual cycle was created! both parent IDs point to each other")
		}
	})

	t.Run("Update: rejects update on revision conflict", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		parentID := metadata.EntryID("root")
		id := metadata.EntryID("file-id")
		now := time.Now().UTC()
		oid, err := content.ParseID("da39a3ee5e6b4b0d3255bfef95601890afd80709")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		entry, err := store.Create(ctx, metadata.Entry{
			ID:        id,
			ParentID:  &parentID,
			Name:      "file.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oid,
			Size:      10,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create: %v", err)
		}

		// Update entry once, which will increment its revision in DB to 2
		entry.Size = 20
		_, err = store.Update(ctx, entry)
		if err != nil {
			t.Fatalf("failed to update first time: %v", err)
		}

		// Try updating with the old entry (which still has Revision 1)
		entry.Size = 30
		_, err = store.Update(ctx, entry)
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected ErrEntryConflict due to revision conflict, got: %v", err)
		}

		// Verify database still has Size 20 and Revision 2
		dbEntry, err := store.GetByID(ctx, id)
		if err != nil {
			t.Fatalf("failed to get: %v", err)
		}
		if dbEntry.Size != 20 {
			t.Errorf("expected Size 20, got %d", dbEntry.Size)
		}
		if dbEntry.Revision != 2 {
			t.Errorf("expected Revision 2, got %d", dbEntry.Revision)
		}
	})

	t.Run("Update: no partial update and rollback on cycle detection failure", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		parentID := metadata.EntryID("root")
		dirAID := metadata.EntryID("dir-A")
		dirBID := metadata.EntryID("dir-B")
		now := time.Now().UTC()

		dirA, err := store.Create(ctx, metadata.Entry{
			ID:        dirAID,
			ParentID:  &parentID,
			Name:      "dirA",
			Kind:      metadata.EntryKindDirectory,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create dirA: %v", err)
		}

		_, err = store.Create(ctx, metadata.Entry{
			ID:        dirBID,
			ParentID:  &dirAID,
			Name:      "dirB",
			Kind:      metadata.EntryKindDirectory,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create dirB: %v", err)
		}

		// Change dirA to point to dirB (cycle!) and try updating name/size
		dirA.ParentID = &dirBID
		dirA.Name = "mutatedDirA"

		_, err = store.Update(ctx, dirA)
		if !errors.Is(err, metadata.ErrEntryConflict) || !strings.Contains(err.Error(), "hierarchy cycle detected") {
			t.Errorf("expected cycle detection error, got: %v", err)
		}

		// Verify database still has original dirA values
		dbDirA, err := store.GetByID(ctx, dirAID)
		if err != nil {
			t.Fatalf("failed to get: %v", err)
		}
		if *dbDirA.ParentID != "root" {
			t.Errorf("expected ParentID 'root', got %v", *dbDirA.ParentID)
		}
		if dbDirA.Name != "dirA" {
			t.Errorf("expected Name 'dirA', got %q", dbDirA.Name)
		}
	})

	t.Run("Update: revision increments by exactly 1 on successful update", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		parentID := metadata.EntryID("root")
		id := metadata.EntryID("file-id")
		now := time.Now().UTC()
		oid, err := content.ParseID("da39a3ee5e6b4b0d3255bfef95601890afd80709")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		entry, err := store.Create(ctx, metadata.Entry{
			ID:        id,
			ParentID:  &parentID,
			Name:      "file.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oid,
			Size:      10,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create: %v", err)
		}

		entry.Size = 20
		updated, err := store.Update(ctx, entry)
		if err != nil {
			t.Fatalf("failed to update: %v", err)
		}

		if updated.Revision != 2 {
			t.Errorf("expected revision to be 2, got %d", updated.Revision)
		}

		dbEntry, err := store.GetByID(ctx, id)
		if err != nil {
			t.Fatalf("failed to get from DB: %v", err)
		}
		if dbEntry.Revision != 2 {
			t.Errorf("expected revision in DB to be 2, got %d", dbEntry.Revision)
		}
	})

	t.Run("Update: concurrent updates with same revision result in at most 1 success", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		parentID := metadata.EntryID("root")
		id := metadata.EntryID("file-id")
		now := time.Now().UTC()
		oid, err := content.ParseID("da39a3ee5e6b4b0d3255bfef95601890afd80709")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		entry, err := store.Create(ctx, metadata.Entry{
			ID:        id,
			ParentID:  &parentID,
			Name:      "file.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oid,
			Size:      10,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create: %v", err)
		}

		var wg sync.WaitGroup
		errs := make(chan error, 5)

		// Fire 5 concurrent updates using the exact same Revision 1
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func(val int64) {
				defer wg.Done()
				updateReq := entry
				updateParams := val
				updateReq.Size = updateParams
				_, updateErr := store.Update(ctx, updateReq)
				if updateErr != nil {
					errs <- updateErr
				}
			}(int64(100 + i))
		}

		wg.Wait()
		close(errs)

		var failures []error
		for f := range errs {
			failures = append(failures, f)
		}

		successCount := 5 - len(failures)
		if successCount != 1 {
			t.Errorf("expected exactly 1 success, got %d successes (failures: %v)", successCount, failures)
		}

		// Verify that the final revision in the database is exactly 2 and the value was actually updated to one of the concurrently requested sizes
		dbEntry, err := store.GetByID(ctx, id)
		if err != nil {
			t.Fatalf("failed to retrieve entry: %v", err)
		}
		if dbEntry.Revision != 2 {
			t.Errorf("expected final revision to be 2, got %d", dbEntry.Revision)
		}
		if dbEntry.Size < 100 || dbEntry.Size > 104 {
			t.Errorf("expected final size to be between 100 and 104, got %d", dbEntry.Size)
		}
	})

	t.Run("Delete: non-empty directory is not deleted and target remains on failure", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		rootID := metadata.EntryID("root")
		now := time.Now().UTC()

		dirID := metadata.EntryID("parent-dir")
		_, err := store.Create(ctx, metadata.Entry{
			ID:        dirID,
			ParentID:  &rootID,
			Name:      "subdir",
			Kind:      metadata.EntryKindDirectory,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create directory: %v", err)
		}

		childID := metadata.EntryID("child-dir")
		_, err = store.Create(ctx, metadata.Entry{
			ID:        childID,
			ParentID:  &dirID,
			Name:      "child_subdir",
			Kind:      metadata.EntryKindDirectory,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create child: %v", err)
		}

		// Try to delete parent-dir
		err = store.Delete(ctx, dirID)
		if !errors.Is(err, metadata.ErrEntryConflict) || !strings.Contains(err.Error(), "cannot delete non-empty directory") {
			t.Errorf("expected non-empty directory error, got: %v", err)
		}

		// Verify target parent-dir still exists (no rollback failure!)
		_, err = store.GetByID(ctx, dirID)
		if err != nil {
			t.Errorf("expected directory to remain, but got error: %v", err)
		}
	})

	t.Run("Delete: target is deleted but unrelated elements are untouched", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		rootID := metadata.EntryID("root")
		now := time.Now().UTC()

		id1 := metadata.EntryID("file-1")
		id2 := metadata.EntryID("file-2")
		oid, err := content.ParseID("da39a3ee5e6b4b0d3255bfef95601890afd80709")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		_, err = store.Create(ctx, metadata.Entry{
			ID:        id1,
			ParentID:  &rootID,
			Name:      "file1.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oid,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create: %v", err)
		}

		_, err = store.Create(ctx, metadata.Entry{
			ID:        id2,
			ParentID:  &rootID,
			Name:      "file2.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oid,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create: %v", err)
		}

		// Delete file-1
		err = store.Delete(ctx, id1)
		if err != nil {
			t.Fatalf("expected delete to succeed, got %v", err)
		}

		// Verify id1 is deleted
		_, err = store.GetByID(ctx, id1)
		if !errors.Is(err, metadata.ErrEntryNotFound) {
			t.Errorf("expected ErrEntryNotFound for deleted entry, got: %v", err)
		}

		// Verify id2 remains completely untouched
		dbEntry2, err := store.GetByID(ctx, id2)
		if err != nil {
			t.Fatalf("expected unrelated element to remain, got: %v", err)
		}
		if dbEntry2.ID != id2 {
			t.Errorf("expected ID %s, got %s", id2, dbEntry2.ID)
		}
	})

	t.Run("Delete: concurrent deletes targeting same entry result in at most 1 success", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		rootID := metadata.EntryID("root")
		now := time.Now().UTC()

		id := metadata.EntryID("file-1")
		oid, err := content.ParseID("da39a3ee5e6b4b0d3255bfef95601890afd80709")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		_, err = store.Create(ctx, metadata.Entry{
			ID:        id,
			ParentID:  &rootID,
			Name:      "file1.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oid,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create: %v", err)
		}

		var wg sync.WaitGroup
		errs := make(chan error, 5)

		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				deleteErr := store.Delete(ctx, id)
				if deleteErr != nil {
					errs <- deleteErr
				}
			}()
		}

		wg.Wait()
		close(errs)

		var failures []error
		for f := range errs {
			failures = append(failures, f)
		}

		successCount := 5 - len(failures)
		if successCount != 1 {
			t.Errorf("expected exactly 1 successful delete, got %d successes (failures: %v)", successCount, failures)
		}

		// Verify that the entry no longer exists in the database
		_, err = store.GetByID(ctx, id)
		if !errors.Is(err, metadata.ErrEntryNotFound) {
			t.Errorf("expected ErrEntryNotFound after deletion, got: %v", err)
		}
	})
}
