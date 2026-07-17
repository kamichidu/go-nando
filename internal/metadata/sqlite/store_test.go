package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

		// 4. Self move must fail
		_, err = store.Move(ctx, dirID, dirID, "subdir")
		if err == nil {
			t.Errorf("expected self-move of directory to fail, but it succeeded")
		} else if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected ErrEntryConflict on self-move, got: %v", err)
		}

		// 5. Descendant move must fail
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

		// Moving dirID ("parent-dir") to childDirID ("child-dir" which is its descendant) must fail
		_, err = store.Move(ctx, dirID, childDirID, "parent-moved")
		if err == nil {
			t.Errorf("expected descendant move of directory to fail, but it succeeded")
		} else if !errors.Is(err, metadata.ErrEntryConflict) || !strings.Contains(err.Error(), "hierarchy cycle") {
			t.Errorf("expected hierarchy cycle ErrEntryConflict, got: %v", err)
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
}
