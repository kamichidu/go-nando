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
	repositoryformat "github.com/kamichidu/go-nando/internal/repository/format"
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

		err = store.Initialize(ctx, repositoryformat.Current())
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
		if root.Mode != 0o755 {
			t.Errorf("expected root mode 0o755 (%d), got %d", 0o755, root.Mode)
		}
	})

	t.Run("Create and Get child successfully", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		parentID := metadata.EntryID("root")
		childID := metadata.EntryID("child-1")
		now := metadata.TimestampFromTime(time.Now().UTC())

		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
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
			Mode:        0o644,
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
		now := metadata.TimestampFromTime(time.Now().UTC())
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
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

		now := metadata.TimestampFromTime(time.Now().UTC())
		rootID := metadata.EntryID("root")
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
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
		now := metadata.TimestampFromTime(time.Now().UTC())
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
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
		now := metadata.TimestampFromTime(time.Now().UTC())
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
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
		now := metadata.TimestampFromTime(time.Now().UTC())
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
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
		now := metadata.TimestampFromTime(time.Now().UTC())

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
		now := metadata.TimestampFromTime(time.Now().UTC())
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
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
		moved, err := store.Move(ctx, metadata.Entry{
			ID:       fileID,
			ParentID: &subID,
			Name:     "moved_file.txt",
			Revision: 1,
		})
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
		now := metadata.TimestampFromTime(time.Now().UTC())
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
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
		now := metadata.TimestampFromTime(time.Now().UTC())

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
		oidHello, err := content.ParseID("sha256-d497750a791fc4366c7f6e7021c9c8a4aef022e81786f57d7ec16f54a03586c2")
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
		oidOld, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
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
		err = store.CheckMigrations(ctx)
		if !errors.Is(err, ErrSchemaVersionUnsupported) {
			t.Errorf("expected incompatible migration history error, got: %v", err)
		}
	})

	t.Run("Format metadata and schema version are persisted and enforced", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		var formatVersion int
		var algorithm, idFormat string
		if err := store.db.QueryRowContext(ctx, `SELECT format_version, content_algorithm, content_id_format FROM repository_format WHERE singleton=1`).Scan(&formatVersion, &algorithm, &idFormat); err != nil {
			t.Fatalf("failed to read format metadata: %v", err)
		}
		if formatVersion != repositoryformat.CurrentVersion || algorithm != repositoryformat.ContentAlgorithmSHA256 || idFormat != repositoryformat.ContentIDFormatSHA256 {
			t.Fatalf("unexpected repository format metadata: %d %q %q", formatVersion, algorithm, idFormat)
		}
		var schemaVersion int
		if err := store.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schemaVersion); err != nil || schemaVersion != 1 {
			t.Fatalf("expected user_version=1, version=%d err=%v", schemaVersion, err)
		}
		var ledgerCount int
		if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'").Scan(&ledgerCount); err != nil || ledgerCount != 0 {
			t.Fatalf("schema_migrations ledger remains: count=%d err=%v", ledgerCount, err)
		}

		if _, err := store.db.ExecContext(ctx, "UPDATE repository_format SET format_version=99 WHERE singleton=1"); err != nil {
			t.Fatalf("failed to inject unsupported format: %v", err)
		}
		if err := store.CheckMigrations(ctx); err != nil {
			t.Fatalf("format policy must not be checked by SQLite migration validation: %v", err)
		}
		persisted, found, err := store.RepositoryFormat(ctx)
		if err != nil || !found {
			t.Fatalf("failed to read unsupported format metadata: found=%t err=%v", found, err)
		}
		if err := persisted.Validate(); !errors.Is(err, repositoryformat.ErrUnsupported) {
			t.Fatalf("Repository owner should reject unsupported format: %v", err)
		}
	})

	t.Run("Newer SQLite user_version is rejected without mutation", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()
		if _, err := store.db.ExecContext(ctx, "PRAGMA user_version=2"); err != nil {
			t.Fatal(err)
		}
		if err := store.CheckMigrations(ctx); !errors.Is(err, ErrSchemaVersionUnsupported) {
			t.Fatalf("unsupported schema version accepted: %v", err)
		}
	})

	t.Run("Empty SQLite database is not an openable repository until initialized", func(t *testing.T) {
		store, err := NewStore(filepath.Join(t.TempDir(), "metadata.sqlite"))
		if err != nil {
			t.Fatalf("failed to create empty database: %v", err)
		}
		defer store.Close()
		if err := store.CheckMigrations(ctx); !errors.Is(err, ErrSchemaUninitialized) {
			t.Fatalf("expected empty database to have no migration history, got %v", err)
		}
		if err := store.Initialize(ctx, repositoryformat.Current()); err != nil {
			t.Fatalf("fresh initialization should accept an empty database: %v", err)
		}
		if err := store.CheckMigrations(ctx); err != nil {
			t.Fatalf("initialized schema should be accepted: %v", err)
		}
	})

	t.Run("Version-zero database with a view is not pristine", func(t *testing.T) {
		store, err := NewStore(filepath.Join(t.TempDir(), "metadata.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if _, err := store.db.ExecContext(ctx, "CREATE VIEW existing_view AS SELECT 1 AS value"); err != nil {
			t.Fatal(err)
		}
		if err := store.CheckMigrations(ctx); !errors.Is(err, ErrSchemaVersionUnsupported) {
			t.Fatalf("database with a user schema object reported pristine: %v", err)
		}
		if err := store.Initialize(ctx, repositoryformat.Current()); !errors.Is(err, ErrSchemaVersionUnsupported) {
			t.Fatalf("initialization accepted a database with a user schema object: %v", err)
		}
		var views int
		if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='view' AND name='existing_view'").Scan(&views); err != nil || views != 1 {
			t.Fatalf("existing view changed: count=%d err=%v", views, err)
		}
	})

	t.Run("Nonempty legacy ledger with user_version zero is rejected", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()
		if _, err := store.db.ExecContext(ctx, "PRAGMA user_version=0; CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL); INSERT INTO schema_migrations VALUES(1,0)"); err != nil {
			t.Fatal(err)
		}
		if err := store.CheckMigrations(ctx); !errors.Is(err, ErrSchemaVersionUnsupported) {
			t.Fatalf("legacy ledger accepted: %v", err)
		}
		if err := store.Initialize(ctx, repositoryformat.Current()); !errors.Is(err, ErrSchemaVersionUnsupported) {
			t.Fatalf("init accepted legacy ledger: %v", err)
		}
	})

	t.Run("Corrupt or missing v1 tables are rejected", func(t *testing.T) {
		for _, table := range []string{"entries", "repository_format", "transfer_queue", "content_metadata"} {
			t.Run(table, func(t *testing.T) {
				store, _ := setupStore(t)
				defer store.Close()
				if _, err := store.db.ExecContext(ctx, "DROP TABLE "+table); err != nil {
					t.Fatal(err)
				}
				if err := store.CheckMigrations(ctx); !errors.Is(err, ErrSchemaVersionUnsupported) {
					t.Fatalf("missing v1 table accepted: %v", err)
				}
			})
		}
		t.Run("malformed entries columns", func(t *testing.T) {
			store, _ := setupStore(t)
			defer store.Close()
			if _, err := store.db.ExecContext(ctx, "DROP TABLE entries; CREATE TABLE entries(entry_id TEXT PRIMARY KEY)"); err != nil {
				t.Fatal(err)
			}
			if err := store.CheckMigrations(ctx); !errors.Is(err, ErrSchemaVersionUnsupported) {
				t.Fatalf("malformed v1 table accepted: %v", err)
			}
		})
	})

	t.Run("Migrate is current-version no-op and rejects unknown versions", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()
		version, err := store.Migrate(ctx)
		if err != nil || version != CurrentSchemaVersion {
			t.Fatalf("current migrate: version=%d err=%v", version, err)
		}
		if _, err := store.db.ExecContext(ctx, "PRAGMA user_version=2"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Migrate(ctx); !errors.Is(err, ErrSchemaVersionUnsupported) {
			t.Fatalf("newer version migrate accepted: %v", err)
		}
		if _, err := store.db.ExecContext(ctx, "PRAGMA user_version=-1"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Migrate(ctx); !errors.Is(err, ErrSchemaVersionUnsupported) {
			t.Fatalf("older schema version migrate accepted: %v", err)
		}
	})

	t.Run("Legacy SHA-1 repository is rejected without migration", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "metadata.sqlite")
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("failed to create legacy database: %v", err)
		}
		_, err = db.Exec(`CREATE TABLE entries (
			entry_id TEXT PRIMARY KEY, parent_id TEXT NULL, name TEXT NOT NULL, kind TEXT NOT NULL,
			object_id TEXT NULL, size INTEGER NOT NULL, mtime INTEGER NOT NULL, mode INTEGER NOT NULL,
			content_type TEXT NOT NULL, revision INTEGER NOT NULL, created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL, UNIQUE(parent_id, name));
			INSERT INTO entries VALUES ('root', NULL, '', 'directory', NULL, 0, 0, 493, '', 1, 0, 0);
			INSERT INTO entries VALUES ('legacy-file', 'root', 'old.txt', 'file', 'da39a3ee5e6b4b0d3255bfef95601890afd80709', 1, 0, 420, '', 1, 0, 0);`)
		if err != nil {
			t.Fatalf("failed to write legacy repository fixture: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("failed to close legacy fixture: %v", err)
		}

		store, err := NewStore(dbPath)
		if err != nil {
			t.Fatalf("failed to open legacy repository: %v", err)
		}
		defer store.Close()
		if err := store.Initialize(ctx, repositoryformat.Current()); !errors.Is(err, ErrSchemaVersionUnsupported) {
			t.Fatalf("expected legacy repository refusal, got %v", err)
		}
		var objectID string
		if err := store.db.QueryRowContext(ctx, "SELECT object_id FROM entries WHERE entry_id='legacy-file'").Scan(&objectID); err != nil {
			t.Fatalf("legacy entry was not preserved: %v", err)
		}
		if objectID != "da39a3ee5e6b4b0d3255bfef95601890afd80709" {
			t.Fatalf("legacy object ID was altered: %q", objectID)
		}
	})

	t.Run("MetadataStore contract validation", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		rootID := metadata.EntryID("root")
		now := metadata.TimestampFromTime(time.Now().UTC())
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
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

	t.Run("Zero Object ID is rejected before SQLite persistence", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()
		var zero content.ID
		rootID := metadata.EntryID("root")
		now := metadata.TimestampFromTime(time.Now().UTC())
		entry := metadata.Entry{
			ID: "zero-object", ParentID: &rootID, Name: "invalid.bin", Kind: metadata.EntryKindFile,
			ObjectID: &zero, Revision: 1, CreatedAt: now, UpdatedAt: now,
		}
		if _, err := store.Create(ctx, entry); !errors.Is(err, content.ErrInvalidID) {
			t.Fatalf("Create should reject zero Object ID, got %v", err)
		}
		if _, err := store.Update(ctx, entry); !errors.Is(err, content.ErrInvalidID) {
			t.Fatalf("Update should reject zero Object ID, got %v", err)
		}
	})

	t.Run("Invalid Object ID persisted outside the adapter is rejected on read", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()
		_, err := store.db.ExecContext(ctx, `INSERT INTO entries
			(entry_id, parent_id, name, kind, object_id, size, mtime, mode, content_type, revision, created_at, updated_at)
			VALUES ('bad-object', 'root', 'bad.bin', 'file', 'not-an-object-id', 1, 0, 420, '', 1, 0, 0)`)
		if err != nil {
			t.Fatalf("failed to inject invalid persisted ID: %v", err)
		}
		if _, err := store.GetByID(ctx, "bad-object"); !errors.Is(err, content.ErrInvalidID) {
			t.Fatalf("read should reject invalid persisted Object ID, got %v", err)
		}
	})

	t.Run("Concurrent Move operations cannot create mutual cycles", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		rootID := metadata.EntryID("root")
		now := metadata.TimestampFromTime(time.Now().UTC())

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
			destB := metadata.EntryID("dir-B")
			_, err := store.Move(ctx, metadata.Entry{
				ID:       "dir-A",
				ParentID: &destB,
				Name:     "dirA",
				Revision: 1,
			})
			if err != nil {
				errs <- err
			}
		}()
		go func() {
			defer wg.Done()
			destA := metadata.EntryID("dir-A")
			_, err := store.Move(ctx, metadata.Entry{
				ID:       "dir-B",
				ParentID: &destA,
				Name:     "dirB",
				Revision: 1,
			})
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
		now := metadata.TimestampFromTime(time.Now().UTC())
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
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
		now := metadata.TimestampFromTime(time.Now().UTC())

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
		now := metadata.TimestampFromTime(time.Now().UTC())
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
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
		now := metadata.TimestampFromTime(time.Now().UTC())
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
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
		now := metadata.TimestampFromTime(time.Now().UTC())

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
		now := metadata.TimestampFromTime(time.Now().UTC())

		id1 := metadata.EntryID("file-1")
		id2 := metadata.EntryID("file-2")
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
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
		now := metadata.TimestampFromTime(time.Now().UTC())

		id := metadata.EntryID("file-1")
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
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

	t.Run("Create: non-existent parent rejects entry creation", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		nonExistentParent := metadata.EntryID("non-existent")
		id := metadata.EntryID("file-id")
		now := metadata.TimestampFromTime(time.Now().UTC())
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		_, err = store.Create(ctx, metadata.Entry{
			ID:        id,
			ParentID:  &nonExistentParent,
			Name:      "file.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oid,
			Size:      10,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if !errors.Is(err, metadata.ErrEntryConflict) || !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("expected ErrEntryConflict for non-existent parent, got: %v", err)
		}

		// Verify entry was not saved
		_, err = store.GetByID(ctx, id)
		if !errors.Is(err, metadata.ErrEntryNotFound) {
			t.Errorf("expected ErrEntryNotFound after failed creation, got: %v", err)
		}
	})

	t.Run("Create: file parent rejects entry creation", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		parentID := metadata.EntryID("root")
		fileID1 := metadata.EntryID("file-id-1")
		fileID2 := metadata.EntryID("file-id-2")
		now := metadata.TimestampFromTime(time.Now().UTC())
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		// Create first file to act as the invalid parent
		_, err = store.Create(ctx, metadata.Entry{
			ID:        fileID1,
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
			t.Fatalf("failed to create first file: %v", err)
		}

		// Try creating second file under the first file as its parent
		_, err = store.Create(ctx, metadata.Entry{
			ID:        fileID2,
			ParentID:  &fileID1,
			Name:      "file2.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oid,
			Size:      10,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if !errors.Is(err, metadata.ErrEntryConflict) || !strings.Contains(err.Error(), "must be a directory") {
			t.Errorf("expected ErrEntryConflict for file as parent, got: %v", err)
		}

		// Verify second entry was not saved
		_, err = store.GetByID(ctx, fileID2)
		if !errors.Is(err, metadata.ErrEntryNotFound) {
			t.Errorf("expected ErrEntryNotFound after failed creation, got: %v", err)
		}
	})

	t.Run("Create: duplicate parent + name rejects entry creation", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		parentID := metadata.EntryID("root")
		fileID1 := metadata.EntryID("file-id-1")
		fileID2 := metadata.EntryID("file-id-2")
		now := metadata.TimestampFromTime(time.Now().UTC())
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		// Create first file under root
		_, err = store.Create(ctx, metadata.Entry{
			ID:        fileID1,
			ParentID:  &parentID,
			Name:      "duplicate.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oid,
			Size:      10,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create first file: %v", err)
		}

		// Try creating second file under root with the exact same name "duplicate.txt"
		_, err = store.Create(ctx, metadata.Entry{
			ID:        fileID2,
			ParentID:  &parentID,
			Name:      "duplicate.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oid,
			Size:      10,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if !errors.Is(err, metadata.ErrEntryConflict) || !strings.Contains(err.Error(), "already exists under parent") {
			t.Errorf("expected ErrEntryConflict for duplicate parent + name, got: %v", err)
		}

		// Verify second entry was not saved
		_, err = store.GetByID(ctx, fileID2)
		if !errors.Is(err, metadata.ErrEntryNotFound) {
			t.Errorf("expected ErrEntryNotFound for fileID2, got: %v", err)
		}
	})

	t.Run("Create: successful creation saves expected attributes", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		parentID := metadata.EntryID("root")
		id := metadata.EntryID("file-id")
		now := metadata.TimestampFromTime(time.Now().UTC())
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		expected := metadata.Entry{
			ID:          id,
			ParentID:    &parentID,
			Name:        "file.txt",
			Kind:        metadata.EntryKindFile,
			ObjectID:    &oid,
			Size:        12345,
			Mtime:       now,
			Mode:        0o644,
			ContentType: "text/plain",
			Revision:    1,
			CreatedAt:   now,
			UpdatedAt:   now,
		}

		created, err := store.Create(ctx, expected)
		if err != nil {
			t.Fatalf("failed to create entry: %v", err)
		}

		if created.ID != expected.ID {
			t.Errorf("expected ID %s, got %s", expected.ID, created.ID)
		}

		dbEntry, err := store.GetByID(ctx, id)
		if err != nil {
			t.Fatalf("failed to retrieve entry: %v", err)
		}

		if dbEntry.ID != expected.ID ||
			*dbEntry.ParentID != *expected.ParentID ||
			dbEntry.Name != expected.Name ||
			dbEntry.Kind != expected.Kind ||
			*dbEntry.ObjectID != *expected.ObjectID ||
			dbEntry.Size != expected.Size ||
			dbEntry.Mtime != expected.Mtime ||
			dbEntry.Mode != expected.Mode ||
			dbEntry.ContentType != expected.ContentType ||
			dbEntry.Revision != expected.Revision {
			t.Errorf("retrieved entry attributes do not match expected: %+v vs %+v", dbEntry, expected)
		}
	})

	t.Run("Create: concurrent creations with same parent + name result in at most 1 success", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		parentID := metadata.EntryID("root")
		now := metadata.TimestampFromTime(time.Now().UTC())
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		targetName := "concurrent_file.txt"

		var wg sync.WaitGroup
		errs := make(chan error, 5)

		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				id := metadata.EntryID(fmt.Sprintf("file-id-%d", idx))
				_, createErr := store.Create(ctx, metadata.Entry{
					ID:        id,
					ParentID:  &parentID,
					Name:      targetName,
					Kind:      metadata.EntryKindFile,
					ObjectID:  &oid,
					Size:      10,
					Revision:  1,
					CreatedAt: now,
					UpdatedAt: now,
				})
				if createErr != nil {
					errs <- createErr
				}
			}(i)
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

		// Verify there is exactly 1 child with name targetName under parentID
		children, err := store.ListChildren(ctx, parentID)
		if err != nil {
			t.Fatalf("failed to list children: %v", err)
		}

		matchCount := 0
		for _, child := range children {
			if child.Name == targetName {
				matchCount++
			}
		}
		if matchCount != 1 {
			t.Errorf("expected exactly 1 child matching name %q under parent, got %d", targetName, matchCount)
		}
	})

	t.Run("Move: non-existent destination parent rejects move", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		parentID := metadata.EntryID("root")
		fileID := metadata.EntryID("file-id")
		now := metadata.TimestampFromTime(time.Now().UTC())
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		_, err = store.Create(ctx, metadata.Entry{
			ID:        fileID,
			ParentID:  &parentID,
			Name:      "file.txt",
			Kind:      metadata.EntryKindFile,
			ObjectID:  &oid,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create: %v", err)
		}

		nonExistentParent := metadata.EntryID("non-existent")
		_, err = store.Move(ctx, metadata.Entry{
			ID:       fileID,
			ParentID: &nonExistentParent,
			Name:     "moved.txt",
			Revision: 1,
		})
		if !errors.Is(err, metadata.ErrEntryConflict) || !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("expected ErrEntryConflict for non-existent parent, got: %v", err)
		}

		// Verify state is unmodified
		dbEntry, err := store.GetByID(ctx, fileID)
		if err != nil {
			t.Fatalf("failed to get: %v", err)
		}
		if *dbEntry.ParentID != parentID {
			t.Errorf("expected ParentID %s, got %s", parentID, *dbEntry.ParentID)
		}
		if dbEntry.Name != "file.txt" {
			t.Errorf("expected Name 'file.txt', got %q", dbEntry.Name)
		}
	})

	t.Run("Move: file destination parent rejects move", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		parentID := metadata.EntryID("root")
		fileID1 := metadata.EntryID("file-1")
		fileID2 := metadata.EntryID("file-2")
		now := metadata.TimestampFromTime(time.Now().UTC())
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		_, err = store.Create(ctx, metadata.Entry{
			ID:        fileID1,
			ParentID:  &parentID,
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
			ID:        fileID2,
			ParentID:  &parentID,
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

		// Try to move file-2 under file-1 as its parent
		_, err = store.Move(ctx, metadata.Entry{
			ID:       fileID2,
			ParentID: &fileID1,
			Name:     "moved.txt",
			Revision: 1,
		})
		if !errors.Is(err, metadata.ErrEntryConflict) || !strings.Contains(err.Error(), "must be a directory") {
			t.Errorf("expected ErrEntryConflict for file as parent, got: %v", err)
		}

		// Verify state is unmodified
		dbEntry, err := store.GetByID(ctx, fileID2)
		if err != nil {
			t.Fatalf("failed to get: %v", err)
		}
		if *dbEntry.ParentID != parentID {
			t.Errorf("expected ParentID %s, got %s", parentID, *dbEntry.ParentID)
		}
	})

	t.Run("Move: cycle-creating move rejects move and original state remains", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		parentID := metadata.EntryID("root")
		dirAID := metadata.EntryID("dir-A")
		dirBID := metadata.EntryID("dir-B")
		now := metadata.TimestampFromTime(time.Now().UTC())

		_, err := store.Create(ctx, metadata.Entry{
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

		// Try to move dir-A under dir-B (its own child/descendant!)
		_, err = store.Move(ctx, metadata.Entry{
			ID:       dirAID,
			ParentID: &dirBID,
			Name:     "dirA",
			Revision: 1,
		})
		if !errors.Is(err, metadata.ErrEntryConflict) || !strings.Contains(err.Error(), "hierarchy cycle") {
			t.Errorf("expected cycle detection ErrEntryConflict, got: %v", err)
		}

		// Verify original states are maintained
		dbDirA, err := store.GetByID(ctx, dirAID)
		if err != nil {
			t.Fatalf("failed to get dirA: %v", err)
		}
		if *dbDirA.ParentID != parentID {
			t.Errorf("expected dirA parent %s, got %s", parentID, *dbDirA.ParentID)
		}
	})

	t.Run("Move: duplicate destination namespace rejects move", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		parentID := metadata.EntryID("root")
		fileID1 := metadata.EntryID("file-1")
		fileID2 := metadata.EntryID("file-2")
		now := metadata.TimestampFromTime(time.Now().UTC())
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		_, err = store.Create(ctx, metadata.Entry{
			ID:        fileID1,
			ParentID:  &parentID,
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
			ID:        fileID2,
			ParentID:  &parentID,
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

		// Try to rename file-2 to "file1.txt" (name of file-1 under root!)
		_, err = store.Move(ctx, metadata.Entry{
			ID:       fileID2,
			ParentID: &parentID,
			Name:     "file1.txt",
			Revision: 1,
		})
		if !errors.Is(err, metadata.ErrEntryConflict) || !strings.Contains(err.Error(), "already exists under parent") {
			t.Errorf("expected ErrEntryConflict due to name collision, got: %v", err)
		}

		// Verify state is unmodified
		dbEntry, err := store.GetByID(ctx, fileID2)
		if err != nil {
			t.Fatalf("failed to get: %v", err)
		}
		if dbEntry.Name != "file2.txt" {
			t.Errorf("expected Name 'file2.txt', got %q", dbEntry.Name)
		}
	})

	t.Run("Move: successful, mismatched and stale moves, revision increments, and other fields ignored", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		parentID := metadata.EntryID("root")
		fileID := metadata.EntryID("file-id")
		now := metadata.TimestampFromTime(time.Now().UTC())
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		_, err = store.Create(ctx, metadata.Entry{
			ID:        fileID,
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

		// 1. mismatched revision should return ErrEntryConflict and maintain original state
		invalidOid, err := content.ParseID("sha256-468d019ea81224aeca7ee270b11959d8a187f6f0b6a3febff1c34dc1d66c8d85")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}
		_, err = store.Move(ctx, metadata.Entry{
			ID:       fileID,
			ParentID: &parentID,
			Name:     "renamed_mismatched.txt",
			Revision: 999,         // mismatched revision!
			Size:     9999,        // try to alter Size
			ObjectID: &invalidOid, // try to alter ObjectID
		})
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected ErrEntryConflict for mismatched revision, got: %v", err)
		}

		// Verify state is unmodified
		dbEntry, err := store.GetByID(ctx, fileID)
		if err != nil {
			t.Fatalf("failed to get: %v", err)
		}
		if dbEntry.Name != "file.txt" {
			t.Errorf("expected Name 'file.txt', got %q", dbEntry.Name)
		}
		if dbEntry.Size != 10 {
			t.Errorf("expected Size 10, got %d", dbEntry.Size)
		}
		if *dbEntry.ObjectID != oid {
			t.Errorf("expected original ObjectID, got %s", dbEntry.ObjectID)
		}

		// 2. successful Move increments revision by 1, renames, and ignores other fields
		moved, err := store.Move(ctx, metadata.Entry{
			ID:       fileID,
			ParentID: &parentID,
			Name:     "renamed.txt",
			Revision: 1,           // correct revision
			Size:     9999,        // attempt to alter size during move
			ObjectID: &invalidOid, // attempt to alter object ID during move
		})
		if err != nil {
			t.Fatalf("expected Move to succeed, got %v", err)
		}

		if moved.Name != "renamed.txt" {
			t.Errorf("expected renamed.txt, got %q", moved.Name)
		}
		if moved.Revision != 2 {
			t.Errorf("expected Revision 2, got %d", moved.Revision)
		}

		// Verify database state: fields like Size and ObjectID remain unchanged
		dbEntry, err = store.GetByID(ctx, fileID)
		if err != nil {
			t.Fatalf("failed to get: %v", err)
		}
		if dbEntry.Name != "renamed.txt" {
			t.Errorf("expected renamed.txt in DB, got %q", dbEntry.Name)
		}
		if dbEntry.Revision != 2 {
			t.Errorf("expected Revision 2 in DB, got %d", dbEntry.Revision)
		}
		if dbEntry.Size != 10 {
			t.Errorf("expected Size to remain 10, got %d", dbEntry.Size)
		}
		if *dbEntry.ObjectID != oid {
			t.Errorf("expected ObjectID to remain original, got %s", dbEntry.ObjectID)
		}

		// 3. stale revision (Revision 1 after we already advanced it to 2) should return ErrEntryConflict
		_, err = store.Move(ctx, metadata.Entry{
			ID:       fileID,
			ParentID: &parentID,
			Name:     "renamed_stale.txt",
			Revision: 1, // stale revision! (the DB has Revision 2 now)
		})
		if !errors.Is(err, metadata.ErrEntryConflict) {
			t.Errorf("expected ErrEntryConflict for stale revision, got: %v", err)
		}

		// Verify state is unmodified (still has values from the successful move)
		dbEntryStale, err := store.GetByID(ctx, fileID)
		if err != nil {
			t.Fatalf("failed to get: %v", err)
		}
		if dbEntryStale.Name != "renamed.txt" {
			t.Errorf("expected Name to remain 'renamed.txt', got %q", dbEntryStale.Name)
		}
		if dbEntryStale.Revision != 2 {
			t.Errorf("expected Revision to remain 2, got %d", dbEntryStale.Revision)
		}
	})

	t.Run("Move: concurrent moves on same entry result in exactly 1 success", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		// Temporarily allow more connections to simulate parallel transactions
		store.db.SetMaxOpenConns(10)
		defer store.db.SetMaxOpenConns(1)

		rootID := metadata.EntryID("root")
		now := metadata.TimestampFromTime(time.Now().UTC())
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		// Create dest subdirectories
		subIDs := []metadata.EntryID{"sub-1", "sub-2", "sub-3", "sub-4", "sub-5"}
		for _, subID := range subIDs {
			_, err = store.Create(ctx, metadata.Entry{
				ID:        subID,
				ParentID:  &rootID,
				Name:      string(subID),
				Kind:      metadata.EntryKindDirectory,
				Revision:  1,
				CreatedAt: now,
				UpdatedAt: now,
			})
			if err != nil {
				t.Fatalf("failed to create subdirectory: %v", err)
			}
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
			t.Fatalf("failed to create: %v", err)
		}

		var wg sync.WaitGroup
		errs := make(chan error, 5)

		// Fire 5 concurrent Moves of the same file to different subdirectories
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				destSub := subIDs[idx]
				_, moveErr := store.Move(ctx, metadata.Entry{
					ID:       fileID,
					ParentID: &destSub,
					Name:     fmt.Sprintf("renamed-%d.txt", idx),
					Revision: 1, // all of them use expected original revision 1
				})
				if moveErr != nil {
					errs <- moveErr
				}
			}(i)
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

		// Verify the entry exists in exactly one location
		dbEntry, err := store.GetByID(ctx, fileID)
		if err != nil {
			t.Fatalf("failed to retrieve entry: %v", err)
		}

		foundLocation := false
		for idx, subID := range subIDs {
			if *dbEntry.ParentID == subID && dbEntry.Name == fmt.Sprintf("renamed-%d.txt", idx) {
				foundLocation = true
				break
			}
		}
		if !foundLocation {
			t.Errorf("entry was not found in any of the expected destination locations: %+v", dbEntry)
		}
	})

	t.Run("Move: concurrent moves on different entries to same destination result in at most 1 success", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		// Temporarily allow more connections to simulate parallel transactions
		store.db.SetMaxOpenConns(10)
		defer store.db.SetMaxOpenConns(1)

		rootID := metadata.EntryID("root")
		now := metadata.TimestampFromTime(time.Now().UTC())
		oid, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		// Create files under root
		fileIDs := []metadata.EntryID{"file-1", "file-2", "file-3", "file-4", "file-5"}
		for _, fileID := range fileIDs {
			_, err = store.Create(ctx, metadata.Entry{
				ID:        fileID,
				ParentID:  &rootID,
				Name:      fmt.Sprintf("%s.txt", fileID),
				Kind:      metadata.EntryKindFile,
				ObjectID:  &oid,
				Revision:  1,
				CreatedAt: now,
				UpdatedAt: now,
			})
			if err != nil {
				t.Fatalf("failed to create: %v", err)
			}
		}

		// Create target subdirectory
		destID := metadata.EntryID("dest-sub")
		_, err = store.Create(ctx, metadata.Entry{
			ID:        destID,
			ParentID:  &rootID,
			Name:      "destination",
			Kind:      metadata.EntryKindDirectory,
			Revision:  1,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create: %v", err)
		}

		targetName := "same_name.txt"

		var wg sync.WaitGroup
		errs := make(chan error, 5)

		// Fire 5 concurrent Moves of different files to the exact same dest ID and Name
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				_, moveErr := store.Move(ctx, metadata.Entry{
					ID:       fileIDs[idx],
					ParentID: &destID,
					Name:     targetName,
					Revision: 1, // all of them use expected original revision 1
				})
				if moveErr != nil {
					errs <- moveErr
				}
			}(i)
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

		// Verify there is exactly 1 entry with targetName under destID
		children, err := store.ListChildren(ctx, destID)
		if err != nil {
			t.Fatalf("failed to list children: %v", err)
		}

		matchCount := 0
		for _, child := range children {
			if child.Name == targetName {
				matchCount++
			}
		}
		if matchCount != 1 {
			t.Errorf("expected exactly 1 matching entry under destination directory, got %d", matchCount)
		}
	})

	t.Run("Move: waiting for writer lock respects context cancellation", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()
		store.db.SetMaxOpenConns(2)

		rootID := metadata.EntryID("root")
		now := metadata.TimestampFromTime(time.Now().UTC())
		entryID := metadata.EntryID("move-context")
		_, err := store.Create(ctx, metadata.Entry{
			ID: entryID, ParentID: &rootID, Name: "move-context.txt",
			Kind: metadata.EntryKindFile, Revision: 1, CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create entry: %v", err)
		}

		writer, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin lock holder transaction: %v", err)
		}
		if _, err := writer.ExecContext(ctx, "UPDATE entries SET updated_at = updated_at WHERE entry_id = ?", entryID.String()); err != nil {
			if rollbackErr := writer.Rollback(); rollbackErr != nil {
				t.Errorf("failed to release lock holder transaction after setup error: %v", rollbackErr)
			}
			t.Fatalf("failed to acquire writer lock: %v", err)
		}

		deadlineCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		parentID := rootID
		_, err = store.Move(deadlineCtx, metadata.Entry{
			ID: entryID, ParentID: &parentID, Name: "moved.txt", Revision: 1,
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("expected context deadline while waiting for SQLite writer lock, got %v", err)
		}
		if errors.Is(err, context.Canceled) {
			t.Errorf("deadline cancellation was reported as explicit cancellation: %v", err)
		}
		if rollbackErr := writer.Rollback(); rollbackErr != nil {
			t.Fatalf("failed to release lock holder transaction: %v", rollbackErr)
		}
		stored, err := store.GetByID(ctx, entryID)
		if err != nil || stored.Name != "move-context.txt" || stored.Revision != 1 {
			t.Fatalf("canceled Move changed persisted entry: entry=%+v err=%v", stored, err)
		}

		if _, err := store.Move(ctx, metadata.Entry{
			ID: entryID, ParentID: &parentID, Name: "moved.txt", Revision: 1,
		}); err != nil {
			t.Errorf("Move did not recover after canceled lock wait: %v", err)
		}
	})

	t.Run("Move: non-constraint SQL errors are not entry conflicts", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		rootID := metadata.EntryID("root")
		now := metadata.TimestampFromTime(time.Now().UTC())
		entryID := metadata.EntryID("move-sql-error")
		_, err := store.Create(ctx, metadata.Entry{
			ID: entryID, ParentID: &rootID, Name: "move-sql-error.txt",
			Kind: metadata.EntryKindFile, Revision: 1, CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("failed to create entry: %v", err)
		}
		if _, err := store.db.Exec(`CREATE TRIGGER reject_move BEFORE UPDATE ON entries
			WHEN OLD.entry_id = 'move-sql-error'
			BEGIN SELECT RAISE(ABORT, 'injected move failure'); END`); err != nil {
			t.Fatalf("failed to install failure trigger: %v", err)
		}

		parentID := rootID
		_, err = store.Move(ctx, metadata.Entry{
			ID: entryID, ParentID: &parentID, Name: "moved.txt", Revision: 1,
		})
		if err == nil || errors.Is(err, metadata.ErrEntryConflict) || !strings.Contains(err.Error(), "injected move failure") {
			t.Errorf("expected unclassified SQLite error, got %v", err)
		}
	})

	t.Run("Timestamp: round-trip nanosecond precision", func(t *testing.T) {
		now := time.Now().UTC()
		ts := metadata.TimestampFromTime(now)
		parsed := ts.Time()

		if !now.Equal(parsed) {
			t.Errorf("expected %v, got %v", now, parsed)
		}
	})

	t.Run("Timestamp: distinguish distinct sub-second timestamps", func(t *testing.T) {
		now := time.Now().UTC()
		t1 := now
		t2 := now.Add(time.Nanosecond)

		ts1 := metadata.TimestampFromTime(t1)
		ts2 := metadata.TimestampFromTime(t2)

		if ts1 == ts2 {
			t.Errorf("expected distinct timestamps, but ts1 == ts2 (%v)", ts1)
		}
	})

	t.Run("Timestamp: SQLite storage preserves nanosecond precision and matches on Create/Update/Move", func(t *testing.T) {
		store, _ := setupStore(t)
		defer store.Close()

		parentID := metadata.EntryID("root")
		id := metadata.EntryID("time-test-id")
		now := time.Now().UTC()
		tsNow := metadata.TimestampFromTime(now)

		// Create
		entry := metadata.Entry{
			ID:          id,
			ParentID:    &parentID,
			Name:        "file.txt",
			Kind:        metadata.EntryKindFile,
			Size:        10,
			Mtime:       tsNow,
			Mode:        0o644,
			ContentType: "text/plain",
			Revision:    1,
			CreatedAt:   tsNow,
			UpdatedAt:   tsNow,
		}

		created, err := store.Create(ctx, entry)
		if err != nil {
			t.Fatalf("failed to create: %v", err)
		}

		if created.Mtime != tsNow || created.CreatedAt != tsNow || created.UpdatedAt != tsNow {
			t.Errorf("created Entry timestamps do not match original tsNow")
		}

		dbEntry, err := store.GetByID(ctx, id)
		if err != nil {
			t.Fatalf("failed to get: %v", err)
		}

		if dbEntry.Mtime != tsNow || dbEntry.CreatedAt != tsNow || dbEntry.UpdatedAt != tsNow {
			t.Errorf("database Entry timestamps do not match original tsNow")
		}

		// Update
		time.Sleep(10 * time.Millisecond) // ensure time passes
		updatedNow := time.Now().UTC()
		tsUpdatedNow := metadata.TimestampFromTime(updatedNow)

		dbEntry.Mtime = tsUpdatedNow
		updated, err := store.Update(ctx, dbEntry)
		if err != nil {
			t.Fatalf("failed to update: %v", err)
		}

		if updated.Mtime != tsUpdatedNow {
			t.Errorf("updated Entry Mtime does not match tsUpdatedNow")
		}

		dbEntryAfterUpdate, err := store.GetByID(ctx, id)
		if err != nil {
			t.Fatalf("failed to get: %v", err)
		}

		if dbEntryAfterUpdate.Mtime != tsUpdatedNow {
			t.Errorf("database Entry after update does not match tsUpdatedNow")
		}

		if dbEntryAfterUpdate.UpdatedAt != updated.UpdatedAt {
			t.Errorf("database Entry UpdatedAt (%v) does not match returned updated UpdatedAt (%v)", dbEntryAfterUpdate.UpdatedAt, updated.UpdatedAt)
		}

		// Move
		destParent := metadata.EntryID("root")
		moved, err := store.Move(ctx, metadata.Entry{
			ID:       id,
			ParentID: &destParent,
			Name:     "moved_file.txt",
			Revision: updated.Revision,
		})
		if err != nil {
			t.Fatalf("failed to move: %v", err)
		}

		dbEntryAfterMove, err := store.GetByID(ctx, id)
		if err != nil {
			t.Fatalf("failed to get: %v", err)
		}

		if dbEntryAfterMove.UpdatedAt != moved.UpdatedAt {
			t.Errorf("database Entry UpdatedAt (%v) does not match returned moved UpdatedAt (%v)", dbEntryAfterMove.UpdatedAt, moved.UpdatedAt)
		}
	})
}

func TestCommitPutTransferQueue(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(filepath.Join(t.TempDir(), "metadata.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Initialize(ctx, repositoryformat.Current()); err != nil {
		t.Fatal(err)
	}
	objectID, err := content.ParseID("sha256-07e23ede2756aa3f5f7cc9759117c4910875e032c27b8556a1e20626224f10ec")
	if err != nil {
		t.Fatal(err)
	}
	parentID := metadata.EntryID("root")
	now := metadata.TimestampFromTime(time.Now().UTC())
	entry := metadata.Entry{ID: "queued-file", ParentID: &parentID, Name: "file.txt", Kind: metadata.EntryKindFile, ObjectID: &objectID, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := store.CommitPut(ctx, entry, true); err != nil {
		t.Fatal(err)
	}
	ids, err := store.PendingTransfers(ctx)
	if err != nil || len(ids) != 1 || ids[0] != objectID {
		t.Fatalf("pending transfers = %v, err %v", ids, err)
	}
	entry.Name = "renamed.txt"
	updated, err := store.CommitPut(ctx, entry, true)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 {
		t.Fatalf("updated revision = %d, want 2", updated.Revision)
	}
	ids, err = store.PendingTransfers(ctx)
	if err != nil || len(ids) != 1 {
		t.Fatalf("idempotent enqueue = %v, err %v", ids, err)
	}
	if err := store.CompleteTransfer(ctx, objectID); err != nil {
		t.Fatal(err)
	}
	ids, err = store.PendingTransfers(ctx)
	if err != nil || len(ids) != 0 {
		t.Fatalf("completed queue = %v, err %v", ids, err)
	}

	badParent := metadata.EntryID("missing-parent")
	invalid := entry
	invalid.ID = "invalid-put"
	invalid.ParentID = &badParent
	invalid.Name = "invalid.txt"
	invalid.Revision = 1
	if _, err := store.CommitPut(ctx, invalid, true); !errors.Is(err, metadata.ErrEntryConflict) {
		t.Fatalf("expected invalid Put to fail before enqueue, got %v", err)
	}
	ids, err = store.PendingTransfers(ctx)
	if err != nil || len(ids) != 0 {
		t.Fatalf("failed Put leaked transfer queue state: %v, err %v", ids, err)
	}
}
