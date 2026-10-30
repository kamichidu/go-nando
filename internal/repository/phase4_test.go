package repository_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/metadata"
	metasqlite "github.com/kamichidu/go-nando/internal/metadata/sqlite"
	"github.com/kamichidu/go-nando/internal/objectstore"
	"github.com/kamichidu/go-nando/internal/objectstore/local"
	"github.com/kamichidu/go-nando/internal/repository"
)

func TestGetAtomicDestinationAndRejectedTargets(t *testing.T) {
	ctx := context.Background()
	repo, _, _, root := newPhase4Repository(t, ctx)
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("repository bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	storedMtime := time.Date(2024, time.March, 4, 5, 6, 7, 0, time.UTC)
	if err := os.Chtimes(source, storedMtime, storedMtime); err != nil {
		t.Fatal(err)
	}
	if err := repo.Put(ctx, source, "/file"); err != nil {
		t.Fatal(err)
	}
	entry, err := repo.Stat(ctx, "/file")
	if err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(root, "destination")
	if err := os.WriteFile(destination, []byte("old bytes"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := repo.Get(ctx, "/file", destination); err != nil {
		t.Fatalf("replace existing file: %v", err)
	}
	assertFileBytes(t, destination, "repository bytes")
	destinationInfo, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if destinationInfo.Mode().Perm() != os.FileMode(entry.Mode).Perm() {
		t.Fatalf("exported mode = %o, want %o", destinationInfo.Mode().Perm(), os.FileMode(entry.Mode).Perm())
	}
	if !destinationInfo.ModTime().Equal(entry.Mtime.Time()) {
		t.Fatalf("exported mtime = %s, want %s", destinationInfo.ModTime(), entry.Mtime.Time())
	}

	absent := filepath.Join(root, "new", "nested", "destination")
	if err := repo.Get(ctx, "/file", absent); err != nil {
		t.Fatalf("create absent destination: %v", err)
	}
	assertFileBytes(t, absent, "repository bytes")

	directoryTarget := filepath.Join(root, "directory-target")
	if err := os.Mkdir(directoryTarget, 0755); err != nil {
		t.Fatal(err)
	}
	if err := repo.Get(ctx, "/file", directoryTarget); err == nil {
		t.Fatal("Get accepted a directory destination")
	}
	if info, err := os.Stat(directoryTarget); err != nil || !info.IsDir() {
		t.Fatalf("directory destination changed: info=%v err=%v", info, err)
	}

	target := filepath.Join(root, "symlink-target")
	if err := os.WriteFile(target, []byte("symlink target"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "symlink-destination")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlink on this host: %v", err)
	}
	if err := repo.Get(ctx, "/file", link); err == nil {
		t.Fatal("Get accepted a symlink destination")
	}
	linkTarget, err := os.Readlink(link)
	if err != nil || linkTarget != target {
		t.Fatalf("symlink changed: target=%q err=%v", linkTarget, err)
	}
	assertFileBytes(t, target, "symlink target")
	assertNoGetTemporaryFiles(t, root)
}

func TestGetReadFailurePreservesDestinationAndCleansTemporaryFile(t *testing.T) {
	ctx := context.Background()
	_, metaStore, objects, root := newPhase4Repository(t, ctx)
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("new repository bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	baseRepo := openRepositoryInstance(t, root, metaStore, objects, content.NewHasher())
	if err := baseRepo.Put(ctx, source, "/file"); err != nil {
		t.Fatal(err)
	}

	readErr := errors.New("injected object read failure")
	failingObjects := &readFailureObjectStore{ObjectStore: objects, err: readErr}
	failingRepo := openRepositoryInstance(t, root, metaStore, failingObjects, content.NewHasher())
	destination := filepath.Join(root, "destination")
	if err := os.WriteFile(destination, []byte("old destination bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := failingRepo.Get(ctx, "/file", destination); !errors.Is(err, readErr) {
		t.Fatalf("expected read failure, got %v", err)
	}
	assertFileBytes(t, destination, "old destination bytes")
	assertNoGetTemporaryFiles(t, root)
}

func TestDoctorDoesNotMutateRepositoryAndIgnoresValidOrphanObjects(t *testing.T) {
	ctx := context.Background()
	repo, metaStore, objects, root := newPhase4Repository(t, ctx)
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("referenced"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := repo.Put(ctx, source, "/file"); err != nil {
		t.Fatal(err)
	}
	if _, err := objects.Commit(ctx, strings.NewReader("valid orphan")); err != nil {
		t.Fatal(err)
	}

	beforeEntries, err := metaStore.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeObjects, err := objects.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Doctor(ctx); err != nil {
		t.Fatalf("Doctor reported valid orphan as unhealthy: %v", err)
	}
	afterEntries, err := metaStore.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	afterObjects, err := objects.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(beforeEntries) != len(afterEntries) || len(beforeObjects) != len(afterObjects) {
		t.Fatalf("Doctor mutated repository counts: entries %d->%d objects %d->%d", len(beforeEntries), len(afterEntries), len(beforeObjects), len(afterObjects))
	}
	for i := range beforeEntries {
		if !reflect.DeepEqual(beforeEntries[i], afterEntries[i]) {
			t.Fatalf("Doctor changed metadata entry %d: before=%+v after=%+v", i, beforeEntries[i], afterEntries[i])
		}
	}
	for i := range beforeObjects {
		if beforeObjects[i] != afterObjects[i] {
			t.Fatalf("Doctor changed object listing: before=%v after=%v", beforeObjects, afterObjects)
		}
	}
}

func TestGCPartialFailureCanBeRetried(t *testing.T) {
	ctx := context.Background()
	repo, metaStore, objects, root := newPhase4Repository(t, ctx)
	referencedSource := filepath.Join(root, "referenced-source")
	if err := os.WriteFile(referencedSource, []byte("reachable bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := repo.Put(ctx, referencedSource, "/reachable"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"orphan-one", "orphan-two", "orphan-three"} {
		if _, err := objects.Commit(ctx, strings.NewReader(value)); err != nil {
			t.Fatal(err)
		}
	}

	failingObjects := &failDeleteOnceObjectStore{ObjectStore: objects, failOnCall: 2, err: errors.New("injected delete failure")}
	failingRepo := openRepositoryInstance(t, root, metaStore, failingObjects, content.NewHasher())
	deleted, err := failingRepo.GC(ctx)
	if !errors.Is(err, failingObjects.err) || deleted != 1 {
		t.Fatalf("expected one deletion before injected failure, deleted=%d err=%v", deleted, err)
	}
	deleted, err = failingRepo.GC(ctx)
	if err != nil || deleted != 2 {
		t.Fatalf("retry GC: deleted=%d err=%v", deleted, err)
	}
	entry, err := failingRepo.Stat(ctx, "/reachable")
	if err != nil || entry.ObjectID == nil {
		t.Fatalf("reachable entry missing after retry: entry=%+v err=%v", entry, err)
	}
	exists, err := objects.Exists(ctx, *entry.ObjectID)
	if err != nil || !exists {
		t.Fatalf("GC deleted reachable object: exists=%v err=%v", exists, err)
	}
}

func TestPutObjectAndMetadataCommitFailureBoundaries(t *testing.T) {
	ctx := context.Background()
	commitErr := errors.New("injected Object Commit failure")
	metadataErr := errors.New("injected Metadata Commit failure")

	t.Run("Object Commit fails before publication", func(t *testing.T) {
		_, metaStore, objects, root := newPhase4Repository(t, ctx)
		source := writePhase4Source(t, root)
		failingRepo := openRepositoryInstance(t, root, metaStore, &commitFailureObjectStore{ObjectStore: objects, err: commitErr}, content.NewHasher())
		if err := failingRepo.Put(ctx, source, "/file"); !errors.Is(err, commitErr) {
			t.Fatalf("expected Object Commit failure, got %v", err)
		}
		assertEntryMissing(t, failingRepo, ctx, "/file")
		ids, err := objects.List(ctx)
		if err != nil || len(ids) != 0 {
			t.Fatalf("Object published before failed Commit: ids=%v err=%v", ids, err)
		}
	})

	t.Run("Object published but Commit reports failure", func(t *testing.T) {
		_, metaStore, objects, root := newPhase4Repository(t, ctx)
		source := writePhase4Source(t, root)
		failingRepo := openRepositoryInstance(t, root, metaStore, &commitFailureObjectStore{ObjectStore: objects, publishBeforeError: true, err: commitErr}, content.NewHasher())
		if err := failingRepo.Put(ctx, source, "/file"); !errors.Is(err, commitErr) {
			t.Fatalf("expected post-publication Object Commit failure, got %v", err)
		}
		assertEntryMissing(t, failingRepo, ctx, "/file")
		ids, err := objects.List(ctx)
		if err != nil || len(ids) != 1 {
			t.Fatalf("expected one orphan published Object: ids=%v err=%v", ids, err)
		}
		deleted, err := failingRepo.GC(ctx)
		if err != nil || deleted != 1 {
			t.Fatalf("GC post-Commit orphan: deleted=%d err=%v", deleted, err)
		}
	})

	t.Run("Metadata Commit fails before persistence", func(t *testing.T) {
		_, metaStore, objects, root := newPhase4Repository(t, ctx)
		source := writePhase4Source(t, root)
		failingMeta := &metadataCreateFailureStore{MetadataStore: metaStore, err: metadataErr}
		failingRepo := openRepositoryInstance(t, root, failingMeta, objects, content.NewHasher())
		if err := failingRepo.Put(ctx, source, "/file"); !errors.Is(err, metadataErr) {
			t.Fatalf("expected Metadata Create failure, got %v", err)
		}
		assertEntryMissing(t, failingRepo, ctx, "/file")
		deleted, err := failingRepo.GC(ctx)
		if err != nil || deleted != 1 {
			t.Fatalf("GC Object orphan after failed Metadata Commit: deleted=%d err=%v", deleted, err)
		}
	})

	t.Run("Metadata persisted but reports failure", func(t *testing.T) {
		_, metaStore, objects, root := newPhase4Repository(t, ctx)
		source := writePhase4Source(t, root)
		failingMeta := &metadataCreateFailureStore{MetadataStore: metaStore, persistBeforeError: true, err: metadataErr}
		failingRepo := openRepositoryInstance(t, root, failingMeta, objects, content.NewHasher())
		if err := failingRepo.Put(ctx, source, "/file"); !errors.Is(err, metadataErr) {
			t.Fatalf("expected post-persistence Metadata failure, got %v", err)
		}
		entry, err := failingRepo.Stat(ctx, "/file")
		if err != nil || entry.ObjectID == nil {
			t.Fatalf("successful Metadata commit was not visible: entry=%+v err=%v", entry, err)
		}
		if err := objects.Verify(ctx, *entry.ObjectID); err != nil {
			t.Fatalf("published Entry references invalid Object: %v", err)
		}
		if deleted, err := failingRepo.GC(ctx); err != nil || deleted != 0 {
			t.Fatalf("GC removed committed referenced Object: deleted=%d err=%v", deleted, err)
		}
	})

	t.Run("replacement Metadata committed but reports failure", func(t *testing.T) {
		repo, metaStore, objects, root := newPhase4Repository(t, ctx)
		oldSource := filepath.Join(root, "old-source")
		newSource := filepath.Join(root, "new-source")
		if err := os.WriteFile(oldSource, []byte("old replacement bytes"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(newSource, []byte("new replacement bytes"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := repo.Put(ctx, oldSource, "/replace"); err != nil {
			t.Fatal(err)
		}
		oldEntry, err := repo.Stat(ctx, "/replace")
		if err != nil || oldEntry.ObjectID == nil {
			t.Fatalf("read old Entry: entry=%+v err=%v", oldEntry, err)
		}

		updateErr := errors.New("injected post-commit Metadata Update error")
		failingMeta := &metadataUpdateAfterErrorStore{MetadataStore: metaStore, err: updateErr}
		failingRepo := openRepositoryInstance(t, root, failingMeta, objects, content.NewHasher())
		if err := failingRepo.Put(ctx, newSource, "/replace"); !errors.Is(err, updateErr) {
			t.Fatalf("expected post-commit Metadata Update error, got %v", err)
		}
		newEntry, err := failingRepo.Stat(ctx, "/replace")
		if err != nil || newEntry.ObjectID == nil || *newEntry.ObjectID == *oldEntry.ObjectID {
			t.Fatalf("replacement Metadata commit was not visible: old=%+v new=%+v err=%v", oldEntry, newEntry, err)
		}
		if err := objects.Verify(ctx, *newEntry.ObjectID); err != nil {
			t.Fatalf("replacement Entry references invalid Object: %v", err)
		}
		if deleted, err := failingRepo.GC(ctx); err != nil || deleted != 1 {
			t.Fatalf("GC old Object after committed replacement: deleted=%d err=%v", deleted, err)
		}
		if err := objects.Verify(ctx, *newEntry.ObjectID); err != nil {
			t.Fatalf("GC removed replacement Object: %v", err)
		}
	})
}

func newPhase4Repository(t *testing.T, ctx context.Context) (*repository.Repository, *metasqlite.Store, *local.Store, string) {
	t.Helper()
	root := t.TempDir()
	metaStore := openInitializedMetadataStore(t, filepath.Join(root, "metadata.sqlite"), ctx)
	objects, err := local.NewStore(filepath.Join(root, "objects"), filepath.Join(root, "temp"), content.NewHasher())
	if err != nil {
		t.Fatal(err)
	}
	repo := openRepositoryInstance(t, root, metaStore, objects, content.NewHasher())
	return repo, metaStore, objects, root
}

func writePhase4Source(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "source")
	if err := os.WriteFile(path, []byte("phase4 content"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertEntryMissing(t *testing.T, repo *repository.Repository, ctx context.Context, path string) {
	t.Helper()
	if _, err := repo.Stat(ctx, path); !errors.Is(err, metadata.ErrEntryNotFound) {
		t.Fatalf("Entry %q unexpectedly visible: %v", path, err)
	}
}

func assertFileBytes(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	if string(data) != expected {
		t.Fatalf("%q contents = %q, want %q", path, data, expected)
	}
}

func assertNoGetTemporaryFiles(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".nando-get-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary Get files remain: %v", matches)
	}
}

type readFailureObjectStore struct {
	objectstore.ObjectStore
	err error
}

func (s *readFailureObjectStore) Open(context.Context, content.ID) (io.ReadCloser, error) {
	return &readFailureCloser{err: s.err}, nil
}

type readFailureCloser struct {
	err  error
	done bool
}

func (r *readFailureCloser) Read(p []byte) (int, error) {
	if r.done {
		return 0, r.err
	}
	r.done = true
	copy(p, []byte("partial"))
	return len("partial"), nil
}

func (r *readFailureCloser) Close() error { return nil }

type failDeleteOnceObjectStore struct {
	objectstore.ObjectStore
	failOnCall int
	err        error
	callCount  int
}

type commitFailureObjectStore struct {
	objectstore.ObjectStore
	err                error
	publishBeforeError bool
}

func (s *commitFailureObjectStore) Publish(ctx context.Context, staged objectstore.StagedObject) (objectstore.CommitResult, error) {
	if !s.publishBeforeError {
		return objectstore.CommitResult{}, s.err
	}
	result, err := s.ObjectStore.Publish(ctx, staged)
	if err != nil {
		return result, err
	}
	return result, s.err
}

type metadataCreateFailureStore struct {
	metadata.MetadataStore
	err                error
	persistBeforeError bool
}

type metadataUpdateAfterErrorStore struct {
	metadata.MetadataStore
	err error
}

func (s *metadataUpdateAfterErrorStore) Update(ctx context.Context, entry metadata.Entry) (metadata.Entry, error) {
	updated, err := s.MetadataStore.Update(ctx, entry)
	if err != nil {
		return updated, err
	}
	return updated, s.err
}

func (s *metadataCreateFailureStore) Create(ctx context.Context, entry metadata.Entry) (metadata.Entry, error) {
	if !s.persistBeforeError {
		return metadata.Entry{}, s.err
	}
	created, err := s.MetadataStore.Create(ctx, entry)
	if err != nil {
		return created, err
	}
	return created, s.err
}

func (s *failDeleteOnceObjectStore) Delete(ctx context.Context, id content.ID) error {
	s.callCount++
	if s.callCount == s.failOnCall {
		return s.err
	}
	return s.ObjectStore.Delete(ctx, id)
}
