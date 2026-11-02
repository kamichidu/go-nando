package repository_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kamichidu/go-nando/internal/content"
	coordinatorsqlite "github.com/kamichidu/go-nando/internal/coordinator/sqlite"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/metadata/sqlite"
	"github.com/kamichidu/go-nando/internal/objectstore"
	"github.com/kamichidu/go-nando/internal/objectstore/local"
	"github.com/kamichidu/go-nando/internal/repository"
	repositoryformat "github.com/kamichidu/go-nando/internal/repository/format"
)

type fixedIDMetadataStore struct {
	metadata.MetadataStore
}

type fakeReplicaStore struct {
	fail       bool
	calls      int
	stored     map[content.ID][]byte
	listed     []objectstore.ReplicaInfo
	attrsErr   error
	attrsMD5   []byte
	generation int64
}

func (s *fakeReplicaStore) Upload(_ context.Context, id content.ID, size int64, expectedMD5 []byte, src io.Reader) (objectstore.ReplicaInfo, error) {
	s.calls++
	data, err := io.ReadAll(src)
	if err != nil {
		return objectstore.ReplicaInfo{}, err
	}
	if s.fail {
		return objectstore.ReplicaInfo{}, errors.New("simulated GCS unavailable")
	}
	if int64(len(data)) != size {
		return objectstore.ReplicaInfo{}, errors.New("unexpected upload size")
	}
	if s.stored == nil {
		s.stored = make(map[content.ID][]byte)
	}
	s.stored[id] = data
	return objectstore.ReplicaInfo{ID: id, Size: size, Generation: 1, MD5: append([]byte(nil), expectedMD5...)}, nil
}
func (s *fakeReplicaStore) OpenReplica(_ context.Context, id content.ID) (io.ReadCloser, error) {
	data, ok := s.stored[id]
	if !ok {
		return nil, objectstore.ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (s *fakeReplicaStore) ReplicaExists(_ context.Context, id content.ID) (bool, error) {
	_, ok := s.stored[id]
	return ok, nil
}
func (s *fakeReplicaStore) ReplicaAttrs(_ context.Context, id content.ID) (objectstore.ReplicaInfo, error) {
	data, ok := s.stored[id]
	if !ok {
		return objectstore.ReplicaInfo{}, objectstore.ErrObjectNotFound
	}
	sum := md5.Sum(data)
	if s.attrsErr != nil {
		return objectstore.ReplicaInfo{}, s.attrsErr
	}
	generation := s.generation
	if generation == 0 {
		generation = 1
	}
	if s.attrsMD5 != nil {
		return objectstore.ReplicaInfo{ID: id, Size: int64(len(data)), Generation: generation, MD5: s.attrsMD5}, nil
	}
	return objectstore.ReplicaInfo{ID: id, Size: int64(len(data)), Generation: generation, MD5: append([]byte{}, sum[:]...)}, nil
}
func (s *fakeReplicaStore) ListReplicas(context.Context) ([]objectstore.ReplicaInfo, error) {
	return append([]objectstore.ReplicaInfo(nil), s.listed...), nil
}
func TestResourceLeasesProtectPutAndReadsFromGC(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	hasher := content.NewHasher()
	metaStore := openInitializedMetadataStore(t, filepath.Join(tempDir, "metadata.sqlite"), ctx)
	objectsDir := filepath.Join(tempDir, "objects")
	tempObjectDir := filepath.Join(tempDir, "temp")
	baseObjects, err := local.NewStore(objectsDir, tempObjectDir, hasher)
	if err != nil {
		t.Fatalf("open object store: %v", err)
	}

	putEntered := make(chan struct{})
	putRelease := make(chan struct{})
	putEntryReady := make(chan metadata.Entry, 1)
	putMetaStore := &blockingCreateMetadataStore{MetadataStore: metaStore, entered: putEntered, release: putRelease, entryReady: putEntryReady}
	putRepo := openRepositoryInstance(t, tempDir, putMetaStore, baseObjects, hasher)
	gcRepo := openRepositoryInstance(t, tempDir, openInitializedMetadataStore(t, filepath.Join(tempDir, "metadata.sqlite"), ctx), baseObjects, hasher)
	sourcePath := filepath.Join(tempDir, "new.txt")
	if err := os.WriteFile(sourcePath, []byte("new object"), 0600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	putDone := make(chan error, 1)
	go func() { putDone <- putRepo.Put(ctx, sourcePath, "/new.txt") }()
	<-putEntered
	committingEntry := <-putEntryReady
	if committingEntry.ObjectID == nil {
		t.Fatal("Put reached metadata create without a committed Object ID")
	}
	exists, err := baseObjects.Exists(ctx, *committingEntry.ObjectID)
	if err != nil || !exists {
		t.Fatalf("Object must be committed before Metadata Create: exists=%v err=%v", exists, err)
	}
	rc, err := baseObjects.Open(ctx, *committingEntry.ObjectID)
	if err != nil {
		t.Fatalf("open committed object before metadata commit: %v", err)
	}
	committedBytes, readErr := io.ReadAll(rc)
	closeErr := rc.Close()
	if readErr != nil || closeErr != nil || string(committedBytes) != "new object" {
		t.Fatalf("pre-metadata object was not fully readable: bytes=%q read=%v close=%v", committedBytes, readErr, closeErr)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if _, err := gcRepo.GC(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("GC did not wait for Put coordination: %v", err)
	}
	close(putRelease)
	if err := <-putDone; err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if deleted, err := gcRepo.GC(ctx); err != nil || deleted != 0 {
		t.Fatalf("GC after Put should preserve the newly referenced object; deleted=%d err=%v", deleted, err)
	}

	readEntered := make(chan struct{})
	readRelease := make(chan struct{})
	catRepo := openRepositoryInstance(t, tempDir, openInitializedMetadataStore(t, filepath.Join(tempDir, "metadata.sqlite"), ctx), &blockingRepositoryObjectStore{
		ObjectStore: baseObjects,
		readEntered: readEntered,
		readRelease: readRelease,
	}, hasher)
	catDone := make(chan error, 1)
	go func() { catDone <- catRepo.Cat(ctx, "/new.txt", io.Discard) }()
	<-readEntered
	if deleted, err := gcRepo.GC(ctx); err != nil || deleted != 0 {
		t.Fatalf("GC should preserve the referenced Object during Cat: deleted=%d err=%v", deleted, err)
	}
	close(readRelease)
	if err := <-catDone; err != nil {
		t.Fatalf("Cat failed: %v", err)
	}
	if deleted, err := gcRepo.GC(ctx); err != nil || deleted != 0 {
		t.Fatalf("GC after Cat should preserve the referenced object; deleted=%d err=%v", deleted, err)
	}

	getEntered := make(chan struct{})
	getRelease := make(chan struct{})
	getRepo := openRepositoryInstance(t, tempDir, openInitializedMetadataStore(t, filepath.Join(tempDir, "metadata.sqlite"), ctx), &blockingRepositoryObjectStore{
		ObjectStore: baseObjects,
		readEntered: getEntered,
		readRelease: getRelease,
	}, hasher)
	getDone := make(chan error, 1)
	go func() { getDone <- getRepo.Get(ctx, "/new.txt", filepath.Join(tempDir, "exported.txt")) }()
	<-getEntered
	if deleted, err := gcRepo.GC(ctx); err != nil || deleted != 0 {
		t.Fatalf("GC should preserve the referenced Object during Get: deleted=%d err=%v", deleted, err)
	}
	close(getRelease)
	if err := <-getDone; err != nil {
		t.Fatalf("Get failed: %v", err)
	}
}

func TestGCDeletesInvalidLocalObjectNames(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	hasher := content.NewHasher()
	metaStore := openInitializedMetadataStore(t, filepath.Join(root, "metadata.sqlite"), ctx)
	objectsDir := filepath.Join(root, "objects")
	objects, err := local.NewStore(objectsDir, filepath.Join(root, "temp"), hasher)
	if err != nil {
		t.Fatal(err)
	}
	invalidPath := filepath.Join(objectsDir, "partial-upload")
	if err := os.WriteFile(invalidPath, []byte("incomplete"), 0600); err != nil {
		t.Fatal(err)
	}
	repo := openRepositoryInstance(t, root, metaStore, objects, hasher)
	deleted, err := repo.GC(ctx)
	if err != nil || deleted != 1 {
		t.Fatalf("GC did not remove invalid Local object: deleted=%d err=%v", deleted, err)
	}
	if _, err := os.Stat(invalidPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid Local object remains after GC: %v", err)
	}
}

func TestGCLeavesUnreferencedGCSReplicaUntouched(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	hasher := content.NewHasher()
	metaStore := openInitializedMetadataStore(t, filepath.Join(root, "metadata.sqlite"), ctx)
	objects, err := local.NewStore(filepath.Join(root, "objects"), filepath.Join(root, "temp"), hasher)
	if err != nil {
		t.Fatal(err)
	}
	id, err := content.ParseID("sha256-ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	if err != nil {
		t.Fatal(err)
	}
	replica := &fakeReplicaStore{listed: []objectstore.ReplicaInfo{{ID: id, Size: 3, Generation: 73}}}
	coordinator, err := coordinatorsqlite.Open(filepath.Join(root, "coordination.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := coordinator.Close(); err != nil {
			t.Errorf("close coordinator: %v", err)
		}
	})
	settings := repository.Settings{DeliveryMode: repository.DeliveryModeGCS, GCSBucket: "unit-test"}
	repo := repository.NewRepositoryWithReplica(metadata.NewValidatingStore(metaStore), objects, replica, hasher, coordinator, settings)
	deleted, err := repo.GC(ctx)
	if err != nil || deleted != 0 {
		t.Fatalf("GC unexpectedly removed an unreferenced GCS replica: deleted=%d err=%v", deleted, err)
	}
	if err := metaStore.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "metadata.sqlite")); err != nil {
		t.Fatal(err)
	}
	lostMetadata, err := sqlite.NewStore(filepath.Join(root, "metadata.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer lostMetadata.Close()
	repo = repository.NewRepositoryWithReplica(metadata.NewValidatingStore(lostMetadata), objects, replica, hasher, coordinator, settings)
	if _, err := repo.GC(ctx); err == nil {
		t.Fatal("GC unexpectedly succeeded after Metadata loss")
	}
	if _, err := repo.RestoreGCSOrphans(ctx); err == nil {
		t.Fatal("GCS recovery unexpectedly used an uninitialized Metadata database")
	}
}

func TestGCSOrphanRecoveryRestoresRecyclebinEntryIdempotently(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	hasher := content.NewHasher()
	metaStore := openInitializedMetadataStore(t, filepath.Join(root, "metadata.sqlite"), ctx)
	objects, err := local.NewStore(filepath.Join(root, "objects"), filepath.Join(root, "temp"), hasher)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := coordinatorsqlite.Open(filepath.Join(root, "coordination.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := coordinator.Close(); err != nil {
			t.Errorf("close coordinator: %v", err)
		}
	}()
	id, err := content.ParseID("sha256-ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	if err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum([]byte("abc"))
	remote := &fakeReplicaStore{listed: []objectstore.ReplicaInfo{{ID: id, Size: 3, Generation: 41, MD5: sum[:]}}, stored: map[content.ID][]byte{id: []byte("abc")}}
	repo := repository.NewRepositoryWithReplica(metadata.NewValidatingStore(metaStore), objects, remote, hasher, coordinator, repository.Settings{DeliveryMode: repository.DeliveryModeGCS, GCSBucket: "test"})
	if recovered, err := repo.RestoreGCSOrphans(ctx); err != nil || recovered != 1 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
	entry, err := repo.ResolvePath(ctx, "/recyclebin/"+id.String())
	if err != nil || entry.Kind != metadata.EntryKindFile || entry.ObjectID == nil || *entry.ObjectID != id || entry.Size != 3 || !bytes.Equal(entry.MD5, remote.listed[0].MD5) {
		t.Fatalf("restored Entry=%+v err=%v", entry, err)
	}
	if recovered, err := repo.RestoreGCSOrphans(ctx); err != nil || recovered != 0 {
		t.Fatalf("second recovery recovered=%d err=%v", recovered, err)
	}
	if string(remote.stored[id]) != "abc" {
		t.Fatalf("recovery changed GCS object bytes: %q", remote.stored[id])
	}
}

func TestGCSOrphanRecoveryPreservesConflictingRecyclebinPath(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	hasher := content.NewHasher()
	metaStore := openInitializedMetadataStore(t, filepath.Join(root, "metadata.sqlite"), ctx)
	objects, err := local.NewStore(filepath.Join(root, "objects"), filepath.Join(root, "temp"), hasher)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := coordinatorsqlite.Open(filepath.Join(root, "coordination.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := coordinator.Close(); err != nil {
			t.Errorf("close coordinator: %v", err)
		}
	}()
	id, err := content.ParseID("sha256-ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := content.ParseID("sha256-2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824")
	if err != nil {
		t.Fatal(err)
	}
	repo := repository.NewRepositoryWithReplica(metadata.NewValidatingStore(metaStore), objects, &fakeReplicaStore{listed: []objectstore.ReplicaInfo{{ID: id, Size: 3, Generation: 41}}}, hasher, coordinator, repository.Settings{DeliveryMode: repository.DeliveryModeGCS, GCSBucket: "test"})
	if err := repo.Mkdir(ctx, "/recyclebin", 0755); err != nil {
		t.Fatal(err)
	}
	parent, err := repo.ResolvePath(ctx, "/recyclebin")
	if err != nil {
		t.Fatal(err)
	}
	rootID := parent.ID
	otherMD5 := md5.Sum([]byte("hello"))
	now := metadata.TimestampFromTime(time.Now().UTC())
	if _, err := metaStore.Create(ctx, metadata.Entry{ID: "collision", ParentID: &rootID, Name: id.String(), Kind: metadata.EntryKindFile, ObjectID: &otherID, MD5: otherMD5[:], Size: 5, Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	before, err := repo.ResolvePath(ctx, "/recyclebin/"+id.String())
	if err != nil || before.ObjectID == nil || *before.ObjectID != otherID {
		t.Fatalf("setup conflicting Entry=%+v err=%v", before, err)
	}
	if recovered, err := repo.RestoreGCSOrphans(ctx); err == nil || recovered != 0 {
		t.Fatalf("conflicting path recovery count=%d err=%v", recovered, err)
	}
	after, err := repo.ResolvePath(ctx, "/recyclebin/"+id.String())
	if err != nil || after.ID != before.ID || after.ObjectID == nil || *after.ObjectID != otherID {
		t.Fatalf("conflicting Entry changed: before=%+v after=%+v err=%v", before, after, err)
	}
}

func TestGCSModePutAtomicallyEnqueuesContentID(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	hasher := content.NewHasher()
	store := openInitializedMetadataStore(t, filepath.Join(root, "metadata.sqlite"), ctx)
	objects, err := local.NewStore(filepath.Join(root, "objects"), filepath.Join(root, "temp"), hasher)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := coordinatorsqlite.Open(filepath.Join(root, "coordination.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := coordinator.Close(); err != nil {
			t.Error(err)
		}
	})
	settings := repository.Settings{DeliveryMode: repository.DeliveryModeGCS, GCSBucket: "test-bucket"}
	remote := &fakeReplicaStore{fail: true}
	repo := repository.NewRepositoryWithReplica(metadata.NewValidatingStore(store), objects, remote, hasher, coordinator, settings)
	source := filepath.Join(root, "input")
	if err := os.WriteFile(source, []byte("queued gcs content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := repo.Put(ctx, source, "/item"); err != nil {
		t.Fatalf("GCS-mode Put: %v", err)
	}
	entry, err := repo.Stat(ctx, "/item")
	if err != nil || entry.ObjectID == nil {
		t.Fatalf("Put Entry was not visible: entry=%+v err=%v", entry, err)
	}
	ids, err := store.PendingTransfers(ctx)
	if err != nil || len(ids) != 1 || ids[0] != *entry.ObjectID {
		t.Fatalf("transfer queue does not contain committed content ID: ids=%v err=%v", ids, err)
	}
	oldest, found, err := store.OldestPendingTransfer(ctx)
	if err != nil || !found || time.Since(oldest) > time.Minute {
		t.Fatalf("durable transfer enqueue time unavailable: oldest=%s found=%t err=%v", oldest, found, err)
	}
	if err := objects.Verify(ctx, *entry.ObjectID); err != nil {
		t.Fatalf("Local authoritative copy missing after Put: %v", err)
	}
	// A second logical Entry with identical bytes is one content-ID obligation.
	if err := repo.Put(ctx, source, "/second"); err != nil {
		t.Fatalf("second GCS-mode Put: %v", err)
	}
	ids, err = store.PendingTransfers(ctx)
	if err != nil || len(ids) != 1 || ids[0] != *entry.ObjectID {
		t.Fatalf("content-ID queue was not idempotent: ids=%v err=%v", ids, err)
	}
	if err := repo.ProcessTransferQueue(ctx); err == nil {
		t.Fatal("simulated transfer failure unexpectedly succeeded")
	}
	ids, err = store.PendingTransfers(ctx)
	if err != nil || len(ids) != 1 {
		t.Fatalf("failed transfer removed its retry obligation: ids=%v err=%v", ids, err)
	}
	if err := repo.Remove(ctx, "/item"); err != nil {
		t.Fatalf("remove first reference while transfer is pending: %v", err)
	}
	if err := repo.Remove(ctx, "/second"); err != nil {
		t.Fatalf("remove last reference while transfer is pending: %v", err)
	}
	if deleted, err := repo.GC(ctx); err != nil || deleted != 0 {
		t.Fatalf("GC must retain a local orphan with a pending transfer: deleted=%d err=%v", deleted, err)
	}
	if err := objects.Verify(ctx, *entry.ObjectID); err != nil {
		t.Fatalf("pending transfer source was deleted by GC: %v", err)
	}
	ids, err = store.PendingTransfers(ctx)
	if err != nil || len(ids) != 1 || ids[0] != *entry.ObjectID {
		t.Fatalf("GC changed pending transfer obligation: ids=%v err=%v", ids, err)
	}
	remote.fail = false
	if err := repo.ProcessTransferQueue(ctx); err != nil {
		t.Fatalf("retry transfer: %v", err)
	}
	ids, err = store.PendingTransfers(ctx)
	if err != nil || len(ids) != 0 {
		t.Fatalf("successful transfer did not clear its queue row: ids=%v err=%v", ids, err)
	}
	if !bytes.Equal(remote.stored[*entry.ObjectID], []byte("queued gcs content")) || remote.calls != 2 {
		t.Fatalf("unexpected fake replica result: calls=%d data=%q", remote.calls, remote.stored[*entry.ObjectID])
	}
}

func TestGCSReadThroughPublishesOnlyHashVerifiedLocalObject(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	hasher := content.NewHasher()
	metaStore := openInitializedMetadataStore(t, filepath.Join(root, "metadata.sqlite"), ctx)
	objects, err := local.NewStore(filepath.Join(root, "objects"), filepath.Join(root, "temp"), hasher)
	if err != nil {
		t.Fatal(err)
	}
	localRepo := openRepositoryInstance(t, root, metaStore, objects, hasher)
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("read-through bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := localRepo.Put(ctx, source, "/file"); err != nil {
		t.Fatal(err)
	}
	entry, err := localRepo.Stat(ctx, "/file")
	if err != nil || entry.ObjectID == nil {
		t.Fatalf("get entry: %+v %v", entry, err)
	}
	id := *entry.ObjectID
	if err := objects.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	remote := &fakeReplicaStore{stored: map[content.ID][]byte{id: []byte("read-through bytes")}}
	coordinator, err := coordinatorsqlite.Open(filepath.Join(root, "coordination.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := coordinator.Close(); err != nil {
			t.Error(err)
		}
	})
	settings := repository.Settings{DeliveryMode: repository.DeliveryModeGCS, GCSBucket: "bucket"}
	repo := repository.NewRepositoryWithReplica(metadata.NewValidatingStore(metaStore), objects, remote, hasher, coordinator, settings)
	var out strings.Builder
	if err := repo.Cat(ctx, "/file", &out); err != nil || out.String() != "read-through bytes" {
		t.Fatalf("Cat read-through: output=%q err=%v", out.String(), err)
	}
	if err := objects.Verify(ctx, id); err != nil {
		t.Fatalf("download was not published as a verified Local Object: %v", err)
	}
	if err := objects.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	remote.stored[id] = []byte("wrong content")
	destination := filepath.Join(root, "export")
	if err := os.WriteFile(destination, []byte("keep old file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := repo.Get(ctx, "/file", destination); !errors.Is(err, objectstore.ErrHashMismatch) {
		t.Fatalf("hash-invalid GCS download was accepted: %v", err)
	}
	if exists, err := objects.Exists(ctx, id); err != nil || exists {
		t.Fatalf("partial/invalid download became a committed Local object: exists=%v err=%v", exists, err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != "keep old file" {
		t.Fatalf("failed read-through changed Get destination: %q err=%v", got, err)
	}
}

func TestCapacityGCOnlyEvictsConfirmedReferencedLocalCache(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	hasher := content.NewHasher()
	store := openInitializedMetadataStore(t, filepath.Join(root, "metadata.sqlite"), ctx)
	objects, err := local.NewStore(filepath.Join(root, "objects"), filepath.Join(root, "temp"), hasher)
	if err != nil {
		t.Fatal(err)
	}
	localRepo := openRepositoryInstance(t, root, store, objects, hasher)
	source := filepath.Join(root, "source")
	data := []byte("cache bytes")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := localRepo.Put(ctx, source, "/cached"); err != nil {
		t.Fatal(err)
	}
	entry, err := localRepo.Stat(ctx, "/cached")
	if err != nil || entry.ObjectID == nil {
		t.Fatalf("entry: %+v %v", entry, err)
	}
	id := *entry.ObjectID
	remote := &fakeReplicaStore{stored: map[content.ID][]byte{id: data}}
	coordinator, err := coordinatorsqlite.Open(filepath.Join(root, "coordination.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := coordinator.Close(); err != nil {
			t.Error(err)
		}
	})
	settings := repository.Settings{DeliveryMode: repository.DeliveryModeGCS, GCSBucket: "bucket", MaxLocalBytes: 1}
	repo := repository.NewRepositoryWithReplica(metadata.NewValidatingStore(store), objects, remote, hasher, coordinator, settings)
	deleted, err := repo.CapacityGC(ctx)
	if err != nil || deleted != 1 {
		t.Fatalf("confirmed cache eviction: deleted=%d err=%v", deleted, err)
	}
	if exists, err := objects.Exists(ctx, id); err != nil || exists {
		t.Fatalf("confirmed Local cache was not evicted: exists=%v err=%v", exists, err)
	}
	if _, err := repo.Stat(ctx, "/cached"); err != nil {
		t.Fatalf("capacity eviction changed Metadata: %v", err)
	}
}

func TestCapacityGCPreservesCacheWhenGCSMD5IsMissingOrDifferent(t *testing.T) {
	for _, tc := range []struct {
		name      string
		remoteMD5 []byte
	}{
		{name: "missing", remoteMD5: []byte{}}, {name: "different", remoteMD5: []byte("not an md5")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			hasher := content.NewHasher()
			store := openInitializedMetadataStore(t, filepath.Join(root, "metadata.sqlite"), ctx)
			objects, err := local.NewStore(filepath.Join(root, "objects"), filepath.Join(root, "temp"), hasher)
			if err != nil {
				t.Fatal(err)
			}
			repoLocal := openRepositoryInstance(t, root, store, objects, hasher)
			source := filepath.Join(root, "source")
			data := []byte("md5 protected cache")
			if err := os.WriteFile(source, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := repoLocal.Put(ctx, source, "/cached"); err != nil {
				t.Fatal(err)
			}
			entry, err := repoLocal.Stat(ctx, "/cached")
			if err != nil {
				t.Fatal(err)
			}
			id := *entry.ObjectID
			coordinator, err := coordinatorsqlite.Open(filepath.Join(root, "coordination.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := coordinator.Close(); err != nil {
					t.Error(err)
				}
			})
			remote := &fakeReplicaStore{stored: map[content.ID][]byte{id: data}, attrsMD5: tc.remoteMD5}
			settings := repository.Settings{DeliveryMode: repository.DeliveryModeGCS, GCSBucket: "bucket", MaxLocalBytes: 1}
			repo := repository.NewRepositoryWithReplica(metadata.NewValidatingStore(store), objects, remote, hasher, coordinator, settings)
			deleted, err := repo.CapacityGC(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if deleted != 0 {
				t.Fatalf("deleted cache despite unusable MD5: %d", deleted)
			}
			if exists, err := objects.Exists(ctx, id); err != nil || !exists {
				t.Fatalf("cache removed: exists=%v err=%v", exists, err)
			}
		})
	}
}

func TestCapacityGCPreservesUntransferredContent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	hasher := content.NewHasher()
	store := openInitializedMetadataStore(t, filepath.Join(root, "metadata.sqlite"), ctx)
	objects, err := local.NewStore(filepath.Join(root, "objects"), filepath.Join(root, "temp"), hasher)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := coordinatorsqlite.Open(filepath.Join(root, "coordination.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := coordinator.Close(); err != nil {
			t.Error(err)
		}
	})
	remote := &fakeReplicaStore{}
	settings := repository.Settings{DeliveryMode: repository.DeliveryModeGCS, GCSBucket: "bucket", MaxLocalBytes: 1}
	repo := repository.NewRepositoryWithReplica(metadata.NewValidatingStore(store), objects, remote, hasher, coordinator, settings)
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("not transferred"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := repo.Put(ctx, source, "/pending"); err != nil {
		t.Fatal(err)
	}
	entry, err := repo.Stat(ctx, "/pending")
	if err != nil || entry.ObjectID == nil {
		t.Fatalf("entry: %+v %v", entry, err)
	}
	deleted, err := repo.CapacityGC(ctx)
	if err != nil || deleted != 0 {
		t.Fatalf("capacity GC removed untransferred data: deleted=%d err=%v", deleted, err)
	}
	if exists, err := objects.Exists(ctx, *entry.ObjectID); err != nil || !exists {
		t.Fatalf("untransferred Local copy missing: exists=%v err=%v", exists, err)
	}
}

func TestExpiredPutStageCannotRenewOrPublishAfterCleanup(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	coordinator, err := coordinatorsqlite.Open(filepath.Join(tempDir, "coordination.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := coordinator.Close(); err != nil {
			t.Errorf("close coordinator: %v", err)
		}
	}()
	objects, err := local.NewStore(filepath.Join(tempDir, "objects"), filepath.Join(tempDir, "temp"), content.NewHasher())
	if err != nil {
		t.Fatal(err)
	}
	const operationID = "b6b70b74-7a9f-4c92-8862-d73437644e7f"
	if err := coordinator.AcquireLease(ctx, operationID, 40*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	session, err := objects.BeginStage(ctx, operationID)
	if err != nil {
		t.Fatal(err)
	}
	if active, err := coordinator.RenewLease(ctx, operationID, 40*time.Millisecond); err != nil || !active {
		t.Fatalf("renew active Stage Lease: active=%v err=%v", active, err)
	}
	staged, err := session.Stage(ctx, strings.NewReader("stale staged bytes"))
	if err != nil {
		t.Fatalf("stage input: %v", err)
	}
	time.Sleep(55 * time.Millisecond)
	ids, err := coordinator.InvalidateExpiredLeases(ctx)
	if err != nil || len(ids) != 1 || ids[0] != operationID {
		t.Fatalf("invalidate expired Lease: ids=%v err=%v", ids, err)
	}
	if err := objects.CleanupStaging(ctx, operationID); err != nil {
		t.Fatalf("cleanup failed to remove disposable Stage: %v", err)
	}
	if active, err := coordinator.RenewLease(ctx, operationID, time.Second); err != nil || active {
		t.Fatalf("expired Lease was resurrected: active=%v err=%v", active, err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.ForgetExpiredLease(ctx, operationID); err != nil {
		t.Fatal(err)
	}
	called := false
	err = coordinator.ExclusiveWithLease(ctx, operationID, func() error {
		called = true
		_, err := objects.Publish(ctx, staged)
		return err
	})
	if !errors.Is(err, coordinatorsqlite.ErrLeaseExpired) || called {
		t.Fatalf("stale Stage entered Commit: callback=%v err=%v", called, err)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "temp", operationID+".content")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleaned expired Stage path reappeared: %v", err)
	}
}

func TestPutStageRunsOutsideRepositoryWideExclusion(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	hasher := content.NewHasher()
	metaStore := openInitializedMetadataStore(t, filepath.Join(tempDir, "metadata.sqlite"), ctx)
	baseObjects, err := local.NewStore(filepath.Join(tempDir, "objects"), filepath.Join(tempDir, "temp"), hasher)
	if err != nil {
		t.Fatal(err)
	}
	stageEntered := make(chan struct{})
	stageRelease := make(chan struct{})
	leaseChecker, err := coordinatorsqlite.Open(filepath.Join(tempDir, "coordination.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := leaseChecker.Close(); err != nil {
			t.Errorf("close Resource Lease checker: %v", err)
		}
	})
	putRepo := openRepositoryInstance(t, tempDir, metaStore, &blockingStageObjectStore{
		ObjectStore: &assertStageLeaseObjectStore{ObjectStore: baseObjects, t: t, coordinator: leaseChecker},
		entered:     stageEntered,
		release:     stageRelease,
	}, hasher)
	gcRepo := openRepositoryInstance(t, tempDir, openInitializedMetadataStore(t, filepath.Join(tempDir, "metadata.sqlite"), ctx), baseObjects, hasher)
	source := filepath.Join(tempDir, "stage-outside-lock.txt")
	if err := os.WriteFile(source, []byte("stage does not hold repository lock"), 0600); err != nil {
		t.Fatal(err)
	}
	putDone := make(chan error, 1)
	go func() { putDone <- putRepo.Put(ctx, source, "/staged.txt") }()
	<-stageEntered
	gcDone := make(chan error, 1)
	go func() {
		_, err := gcRepo.GC(ctx)
		gcDone <- err
	}()
	select {
	case err := <-gcDone:
		if err != nil {
			t.Fatalf("GC during Stage: %v", err)
		}
	case <-time.After(time.Second):
		close(stageRelease)
		if err := <-putDone; err != nil {
			t.Logf("Put completed after timeout cleanup: %v", err)
		}
		if err := <-gcDone; err != nil {
			t.Logf("GC completed after timeout cleanup: %v", err)
		}
		t.Fatal("GC blocked while Put was only staging input")
	}
	close(stageRelease)
	if err := <-putDone; err != nil {
		t.Fatalf("Put after concurrent Stage and GC: %v", err)
	}
	entry, err := putRepo.Stat(ctx, "/staged.txt")
	if err != nil || entry.ObjectID == nil {
		t.Fatalf("Put result missing after Stage: entry=%+v err=%v", entry, err)
	}
}

func TestGCRetriesExpiredStageCleanupAfterFailure(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	hasher := content.NewHasher()
	metaStore := openInitializedMetadataStore(t, filepath.Join(tempDir, "metadata.sqlite"), ctx)
	baseObjects, err := local.NewStore(filepath.Join(tempDir, "objects"), filepath.Join(tempDir, "temp"), hasher)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := coordinatorsqlite.Open(filepath.Join(tempDir, "coordination.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := coordinator.Close(); err != nil {
			t.Errorf("close coordinator: %v", err)
		}
	}()
	const operationID = "cccf55dc-19db-4df1-bc32-9f774ffea271"
	if err := coordinator.AcquireLease(ctx, operationID, 30*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	session, err := baseObjects.BeginStage(ctx, operationID)
	if err != nil {
		t.Fatal(err)
	}
	if active, err := coordinator.RenewLease(ctx, operationID, 30*time.Millisecond); err != nil || !active {
		t.Fatalf("renew test Lease: active=%v err=%v", active, err)
	}
	if _, err := session.Stage(ctx, strings.NewReader("staging to clean")); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(45 * time.Millisecond)
	failure := errors.New("injected staging cleanup failure")
	objects := &failStagingCleanupOnceObjectStore{ObjectStore: baseObjects, err: failure}
	repo := openRepositoryInstance(t, tempDir, metaStore, objects, hasher)
	if _, err := repo.GC(ctx); !errors.Is(err, failure) {
		t.Fatalf("expected staging cleanup error before GC, got %v", err)
	}
	if active, err := coordinator.RenewLease(ctx, operationID, time.Second); err != nil || active {
		t.Fatalf("cleanup failure left an active Lease: active=%v err=%v", active, err)
	}
	if _, err := repo.GC(ctx); err != nil {
		t.Fatalf("GC did not retry invalidated staging cleanup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "temp", operationID+".content")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging content remains after cleanup retry: %v", err)
	}
}

func TestGCCleansStagingWithoutLease(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	hasher := content.NewHasher()
	metaStore := openInitializedMetadataStore(t, filepath.Join(tempDir, "metadata.sqlite"), ctx)
	objects, err := local.NewStore(filepath.Join(tempDir, "objects"), filepath.Join(tempDir, "temp"), hasher)
	if err != nil {
		t.Fatal(err)
	}
	const operationID = "e2441842-c0eb-4a95-ae50-946902331c34"
	session, err := objects.BeginStage(ctx, operationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	repo := openRepositoryInstance(t, tempDir, metaStore, objects, hasher)
	if _, err := repo.GC(ctx); err != nil {
		t.Fatalf("GC cleanup of stage without a Lease: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "temp", operationID+".content")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging without a Lease remains after cleanup: %v", err)
	}
}

type blockingStageObjectStore struct {
	objectstore.ObjectStore
	entered chan struct{}
	release chan struct{}
}

type assertStageLeaseObjectStore struct {
	objectstore.ObjectStore
	t           *testing.T
	coordinator *coordinatorsqlite.Coordinator
}

func (s *assertStageLeaseObjectStore) BeginStage(ctx context.Context, operationID string) (objectstore.StageSession, error) {
	s.t.Helper()
	active, err := s.coordinator.ValidateResourceLocks(ctx, operationID)
	if err != nil || !active {
		return nil, fmt.Errorf("Stage began without active temp Resource RLock: active=%v err=%v", active, err)
	}
	return s.ObjectStore.BeginStage(ctx, operationID)
}

func (s *blockingStageObjectStore) BeginStage(ctx context.Context, operationID string) (objectstore.StageSession, error) {
	session, err := s.ObjectStore.BeginStage(ctx, operationID)
	if err != nil {
		return nil, err
	}
	return blockingStageSession{StageSession: session, entered: s.entered, release: s.release}, nil
}

type blockingStageSession struct {
	objectstore.StageSession
	entered chan struct{}
	release chan struct{}
}

type failStagingCleanupOnceObjectStore struct {
	objectstore.ObjectStore
	err    error
	failed bool
}

func (s *failStagingCleanupOnceObjectStore) CleanupStaging(ctx context.Context, operationID string) error {
	if !s.failed {
		s.failed = true
		return s.err
	}
	return s.ObjectStore.CleanupStaging(ctx, operationID)
}

func (s *failStagingCleanupOnceObjectStore) ListStaging(ctx context.Context) ([]string, error) {
	return s.ObjectStore.ListStaging(ctx)
}

func (s blockingStageSession) Stage(ctx context.Context, src io.Reader) (objectstore.StagedObject, error) {
	close(s.entered)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.release:
		return s.StageSession.Stage(ctx, src)
	}
}

func TestDoctorDoesNotLockMetadataDBOrBlockGC(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	hasher := content.NewHasher()
	metaStore := openInitializedMetadataStore(t, filepath.Join(tempDir, "metadata.sqlite"), ctx)
	baseObjects, err := local.NewStore(filepath.Join(tempDir, "objects"), filepath.Join(tempDir, "temp"), hasher)
	if err != nil {
		t.Fatalf("open object store: %v", err)
	}
	listEntered := make(chan struct{})
	listRelease := make(chan struct{})
	doctorRepo := openRepositoryInstance(t, tempDir, metaStore, &blockingRepositoryObjectStore{
		ObjectStore:    baseObjects,
		listRawEntered: listEntered,
		listRawRelease: listRelease,
	}, hasher)
	gcRepo := openRepositoryInstance(t, tempDir, openInitializedMetadataStore(t, filepath.Join(tempDir, "metadata.sqlite"), ctx), baseObjects, hasher)
	doctorDone := make(chan error, 1)
	go func() { doctorDone <- doctorRepo.Doctor(ctx) }()
	<-listEntered

	parentID := metadata.EntryID("root")
	entry := metadata.Entry{ID: "unrelated-metadata-write", ParentID: &parentID, Name: "parallel", Kind: metadata.EntryKindDirectory, Revision: 1}
	if _, err := metaStore.Create(ctx, entry); err != nil {
		t.Fatalf("coordination transaction blocked an independent Metadata DB writer: %v", err)
	}
	if deleted, err := gcRepo.GC(ctx); err != nil || deleted != 0 {
		t.Fatalf("GC should not block on non-destructive Doctor: deleted=%d err=%v", deleted, err)
	}
	close(listRelease)
	if err := <-doctorDone; err != nil {
		t.Fatalf("Doctor failed: %v", err)
	}
}

type blockingRepositoryObjectStore struct {
	objectstore.ObjectStore
	readEntered    chan struct{}
	readRelease    chan struct{}
	listRawEntered chan struct{}
	listRawRelease chan struct{}
	readOnce       sync.Once
	listRawOnce    sync.Once
}

func (s *blockingRepositoryObjectStore) Open(ctx context.Context, id content.ID) (io.ReadCloser, error) {
	rc, err := s.ObjectStore.Open(ctx, id)
	if err != nil || s.readEntered == nil {
		return rc, err
	}
	return &blockingReadCloser{ReadCloser: rc, entered: s.readEntered, release: s.readRelease, once: &s.readOnce}, nil
}

type blockingCreateMetadataStore struct {
	metadata.MetadataStore
	entered    chan struct{}
	release    chan struct{}
	entryReady chan metadata.Entry
}

func (s *blockingCreateMetadataStore) Create(ctx context.Context, entry metadata.Entry) (metadata.Entry, error) {
	s.entryReady <- entry
	close(s.entered)
	select {
	case <-s.release:
	case <-ctx.Done():
		return metadata.Entry{}, ctx.Err()
	}
	return s.MetadataStore.Create(ctx, entry)
}

func TestPutReplacementMetadataFailurePreservesOldEntryUntilGC(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	hasher := content.NewHasher()
	metaStore := openInitializedMetadataStore(t, filepath.Join(tempDir, "metadata.sqlite"), ctx)
	objects, err := local.NewStore(filepath.Join(tempDir, "objects"), filepath.Join(tempDir, "temp"), hasher)
	if err != nil {
		t.Fatalf("open object store: %v", err)
	}
	workingRepo := openRepositoryInstance(t, tempDir, metaStore, objects, hasher)
	oldPath := filepath.Join(tempDir, "old.txt")
	if err := os.WriteFile(oldPath, []byte("old committed content"), 0600); err != nil {
		t.Fatalf("write old source: %v", err)
	}
	if err := workingRepo.Put(ctx, oldPath, "/item.txt"); err != nil {
		t.Fatalf("put initial content: %v", err)
	}
	oldEntry, err := workingRepo.Stat(ctx, "/item.txt")
	if err != nil {
		t.Fatalf("stat initial entry: %v", err)
	}
	oldID := *oldEntry.ObjectID

	newPath := filepath.Join(tempDir, "new.txt")
	if err := os.WriteFile(newPath, []byte("replacement content"), 0600); err != nil {
		t.Fatalf("write replacement source: %v", err)
	}
	updateErr := errors.New("injected metadata update failure")
	failingRepo := openRepositoryInstance(t, tempDir, &failingUpdateMetadataStore{MetadataStore: metaStore, err: updateErr}, objects, hasher)
	if err := failingRepo.Put(ctx, newPath, "/item.txt"); !errors.Is(err, updateErr) {
		t.Fatalf("expected injected Metadata Update failure, got %v", err)
	}

	currentEntry, err := workingRepo.Stat(ctx, "/item.txt")
	if err != nil {
		t.Fatalf("old Entry became unavailable after failed replacement: %v", err)
	}
	if currentEntry.Revision != oldEntry.Revision || currentEntry.ObjectID == nil || *currentEntry.ObjectID != oldID {
		t.Fatalf("failed replacement changed authoritative Entry: before=%+v after=%+v", oldEntry, currentEntry)
	}
	if err := objects.Verify(ctx, oldID); err != nil {
		t.Fatalf("old committed Object was not preserved: %v", err)
	}
	deleted, err := workingRepo.GC(ctx)
	if err != nil {
		t.Fatalf("GC orphan after failed replacement: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected failed replacement's orphan Object to be collected, got %d deletions", deleted)
	}
	if err := objects.Verify(ctx, oldID); err != nil {
		t.Fatalf("GC deleted the still-referenced old Object: %v", err)
	}
}

type failingUpdateMetadataStore struct {
	metadata.MetadataStore
	err error
}

func (s *failingUpdateMetadataStore) Update(context.Context, metadata.Entry) (metadata.Entry, error) {
	return metadata.Entry{}, s.err
}

func (s *blockingRepositoryObjectStore) ListRaw(ctx context.Context) ([]string, error) {
	if s.listRawEntered != nil {
		s.listRawOnce.Do(func() { close(s.listRawEntered) })
		select {
		case <-s.listRawRelease:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.ObjectStore.ListRaw(ctx)
}

type blockingReadCloser struct {
	io.ReadCloser
	entered chan struct{}
	release chan struct{}
	once    *sync.Once
}

func (r *blockingReadCloser) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.entered) })
	<-r.release
	return r.ReadCloser.Read(p)
}

func openInitializedMetadataStore(t *testing.T, dbPath string, ctx context.Context) *sqlite.Store {
	t.Helper()
	store, err := sqlite.NewStore(dbPath)
	if err != nil {
		t.Fatalf("open Metadata Store: %v", err)
	}
	if err := store.Initialize(ctx, repositoryformat.Current()); err != nil {
		t.Fatalf("initialize Metadata Store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close Metadata Store: %v", err)
		}
	})
	return store
}

func openRepositoryInstance(t *testing.T, repoDir string, metaStore metadata.MetadataStore, objStore objectstore.ObjectStore, hasher content.Hasher) *repository.Repository {
	t.Helper()
	coordinator, err := coordinatorsqlite.Open(filepath.Join(repoDir, "coordination.db"))
	if err != nil {
		t.Fatalf("open Repository coordinator: %v", err)
	}
	t.Cleanup(func() {
		if err := coordinator.Close(); err != nil {
			t.Errorf("close Repository coordinator: %v", err)
		}
	})
	return repository.NewRepository(metadata.NewValidatingStore(metaStore), objStore, hasher, coordinator)
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
		if err := metaStore.Initialize(ctx, repositoryformat.Current()); err != nil {
			t.Fatalf("failed to init meta store: %v", err)
		}

		objectsDir := filepath.Join(tempDir, "objects")
		tempObjDir := filepath.Join(tempDir, "temp")
		objStore, err := local.NewStore(objectsDir, tempObjDir, hasher)
		if err != nil {
			t.Fatalf("failed to create object store: %v", err)
		}

		validatingMetaStore := metadata.NewValidatingStore(metaStore)
		coordinator, err := coordinatorsqlite.Open(filepath.Join(tempDir, "coordination.db"))
		if err != nil {
			t.Fatalf("failed to open coordinator: %v", err)
		}
		t.Cleanup(func() {
			if err := coordinator.Close(); err != nil {
				t.Errorf("close coordinator: %v", err)
			}
		})
		repo := repository.NewRepository(validatingMetaStore, objStore, hasher, coordinator)
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
		if err := rawStore.Initialize(ctx, repositoryformat.Current()); err != nil {
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

		coordinator, err := coordinatorsqlite.Open(filepath.Join(tempDir, "coordination.db"))
		if err != nil {
			t.Fatalf("failed to open coordinator: %v", err)
		}
		t.Cleanup(func() {
			if err := coordinator.Close(); err != nil {
				t.Errorf("close coordinator: %v", err)
			}
		})
		repo := repository.NewRepository(metaStore, objStore, hasher, coordinator)

		helloFile := filepath.Join(tempDir, "hello.txt")
		err = os.WriteFile(helloFile, []byte("hello nando\n"), 0644)
		if err != nil {
			t.Fatalf("failed to write local file: %v", err)
		}
		if err := repo.Put(ctx, helloFile, "/documents/hello.txt"); err != nil {
			t.Fatalf("failed to put file and create parent directory: %v", err)
		}
		docsDir, err := repo.Stat(ctx, "/documents")
		if err != nil {
			t.Fatalf("failed to stat /documents: %v", err)
		}
		helloStat, err := repo.Stat(ctx, "/documents/hello.txt")
		if err != nil {
			t.Fatalf("failed to stat hello: %v", err)
		}

		var sb strings.Builder

		// 1. Put creates the implied parent directory /documents.
		sb.WriteString("OP: create directory /documents\n")
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
