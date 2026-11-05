package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kamichidu/go-nando/controlrpc"
	"github.com/kamichidu/go-nando/internal/app"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/repository"
)

type fakeRepositoryUseCases struct {
	mu               sync.Mutex
	entry            *metadata.Entry
	content          []byte
	readReleaseCount int
	putStarted       chan struct{}
	putContinue      chan struct{}
	putErr           error
	removeEntryErr   error
}

type failingSessionFiles struct {
	osSessionFiles
	syncErr    error
	writeErr   error
	writePhase string
	removePath string
	removeErr  error
}

type blockingSessionFiles struct {
	osSessionFiles
	started       chan struct{}
	continueWrite chan struct{}
	once          *sync.Once
}

func (f blockingSessionFiles) WriteAtomic(directory, path string, data []byte) error {
	f.once.Do(func() {
		close(f.started)
		<-f.continueWrite
	})
	return f.osSessionFiles.WriteAtomic(directory, path, data)
}

func (f failingSessionFiles) SyncFile(path string) error {
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.osSessionFiles.SyncFile(path)
}

func (f failingSessionFiles) WriteAtomic(directory, path string, data []byte) error {
	if f.writeErr != nil && f.writePhase != "" {
		temp, err := os.CreateTemp(directory, ".test-recovery-record-*")
		if err != nil {
			return err
		}
		tempPath := temp.Name()
		if f.writePhase == "write" {
			return errors.Join(f.writeErr, temp.Close(), os.Remove(tempPath))
		}
		if _, err := temp.Write(data); err != nil {
			return errors.Join(err, temp.Close(), os.Remove(tempPath))
		}
		if f.writePhase == "fsync" {
			return errors.Join(f.writeErr, temp.Close(), os.Remove(tempPath))
		}
		if err := temp.Sync(); err != nil {
			return errors.Join(err, temp.Close(), os.Remove(tempPath))
		}
		if err := temp.Close(); err != nil {
			return errors.Join(err, os.Remove(tempPath))
		}
		return errors.Join(f.writeErr, os.Remove(tempPath))
	}
	if f.writeErr != nil {
		return f.writeErr
	}
	return f.osSessionFiles.WriteAtomic(directory, path, data)
}

func (f failingSessionFiles) Remove(path string) error {
	if path == f.removePath && f.removeErr != nil {
		return f.removeErr
	}
	return f.osSessionFiles.Remove(path)
}

func (f *fakeRepositoryUseCases) Stat(_ context.Context, path string) (metadata.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.entry == nil || path != "/file" {
		return metadata.Entry{}, metadata.ErrEntryNotFound
	}
	return *f.entry, nil
}

func (f *fakeRepositoryUseCases) ResolveEntry(_ context.Context, id metadata.EntryID) (metadata.Entry, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.entry == nil || f.entry.ID != id {
		return metadata.Entry{}, "", metadata.ErrEntryNotFound
	}
	return *f.entry, "/file", nil
}

func (f *fakeRepositoryUseCases) List(_ context.Context, _ string) ([]metadata.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.entry == nil {
		return nil, nil
	}
	return []metadata.Entry{*f.entry}, nil
}

func (f *fakeRepositoryUseCases) Mkdir(context.Context, string, uint32) error { return nil }

func (f *fakeRepositoryUseCases) Move(context.Context, app.MoveInput) error { return nil }
func (f *fakeRepositoryUseCases) MoveEntry(context.Context, metadata.EntryID, string) error {
	return nil
}
func (f *fakeRepositoryUseCases) Remove(context.Context, string) error { return nil }
func (f *fakeRepositoryUseCases) RemoveEntry(_ context.Context, id metadata.EntryID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removeEntryErr != nil {
		return f.removeEntryErr
	}
	if f.entry != nil && f.entry.ID == id {
		f.entry = nil
		f.content = nil
	}
	return nil
}
func (f *fakeRepositoryUseCases) GC(context.Context) (int, error)       { return 0, nil }
func (f *fakeRepositoryUseCases) MaintenanceOnce(context.Context) error { return nil }
func (f *fakeRepositoryUseCases) Doctor(context.Context) error          { return nil }
func (f *fakeRepositoryUseCases) Sync(context.Context) error            { return nil }

func (f *fakeRepositoryUseCases) BeginReadResource(_ context.Context, _ string) (metadata.Entry, string, func() error, error) {
	file, err := os.CreateTemp("", "nando-test-read-resource-")
	if err != nil {
		return metadata.Entry{}, "", nil, err
	}
	path := file.Name()
	if _, err := file.Write([]byte("read")); err != nil {
		return metadata.Entry{}, "", nil, errors.Join(err, file.Close(), os.Remove(path))
	}
	if err := file.Close(); err != nil {
		return metadata.Entry{}, "", nil, errors.Join(err, os.Remove(path))
	}
	return metadata.Entry{ID: "entry-1", Name: "file", Kind: metadata.EntryKindFile, Revision: 1}, path, func() error {
		f.mu.Lock()
		f.readReleaseCount++
		f.mu.Unlock()
		return os.Remove(path)
	}, nil
}

func (f *fakeRepositoryUseCases) BeginReadEntryResource(ctx context.Context, _ metadata.EntryID) (metadata.Entry, string, func() error, error) {
	return f.BeginReadResource(ctx, "/file")
}

func TestReadResourceSessionReleaseRunsRepositoryCleanup(t *testing.T) {
	fake := &fakeRepositoryUseCases{entry: &metadata.Entry{ID: "entry-1", Name: "file", Kind: metadata.EntryKindFile, Revision: 1}}
	sessions, err := newSessionManager(fake, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sessions.close(); err != nil {
			t.Errorf("close sessions: %v", err)
		}
	}()
	service := newService(fake, sessions)
	params := marshalTestJSON(t, map[string]any{"path": "/file", "resource_schemes": []string{"file"}})
	result, err := service.Call(context.Background(), controlrpc.MethodBeginRead, params)
	if err != nil {
		t.Fatal(err)
	}
	var read controlrpc.BeginReadResult
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &read); err != nil {
		t.Fatal(err)
	}
	path := resourcePathFromURI(t, read.ResourceURI)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("read Resource was not created: %v", err)
	}
	params = marshalTestJSON(t, map[string]string{"session_id": read.SessionID})
	if _, err := service.Call(context.Background(), controlrpc.MethodRelease, params); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Call(context.Background(), controlrpc.MethodRelease, params); err != nil {
		t.Fatalf("idempotent release: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.readReleaseCount != 1 {
		t.Fatalf("Repository read resource released %d times, want 1", fake.readReleaseCount)
	}
}

func (f *fakeRepositoryUseCases) PutExpected(_ context.Context, input app.PutInput, expected repository.PutExpectation) error {
	if f.putStarted != nil {
		select {
		case f.putStarted <- struct{}{}:
		default:
		}
		<-f.putContinue
	}
	data, err := os.ReadFile(input.LocalPath)
	if err != nil {
		return err
	}
	if f.putErr != nil {
		return f.putErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if expected.MustBeAbsent {
		if f.entry != nil {
			return metadata.ErrEntryConflict
		}
		f.entry = &metadata.Entry{ID: "entry-1", Name: "file", Kind: metadata.EntryKindFile, Revision: 1}
	} else if f.entry == nil || f.entry.ID != expected.EntryID || f.entry.Revision != expected.Revision {
		return metadata.ErrEntryConflict
	} else {
		f.entry.Revision++
	}
	f.entry.Size = int64(len(data))
	f.content = append([]byte(nil), data...)
	return nil
}

func (f *fakeRepositoryUseCases) PutExpectedEntryID(ctx context.Context, input app.PutInput, observed metadata.Entry) error {
	return f.PutExpected(ctx, input, repository.PutExpectation{EntryID: observed.ID, Revision: observed.Revision})
}

func TestResourceSessionsExpireRenewAndPersistRecovery(t *testing.T) {
	fake := &fakeRepositoryUseCases{}
	root := t.TempDir()
	sessions, err := newSessionManagerWithTTL(fake, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	service := newService(fake, sessions)
	params := marshalTestJSON(t, map[string]any{"path": "/file", "resource_schemes": []string{"file"}})
	result, err := service.Call(context.Background(), controlrpc.MethodBeginWrite, params)
	if err != nil {
		t.Fatal(err)
	}
	var begin controlrpc.BeginWriteResult
	if err := decodeTestResult(t, result, &begin); err != nil {
		t.Fatal(err)
	}
	stage := resourcePathFromURI(t, begin.ResourceURI)
	if err := os.WriteFile(stage, []byte("durable recovery bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	firstRenew, err := sessions.renew(begin.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Call(context.Background(), controlrpc.MethodRenew, marshalTestJSON(t, map[string]string{"session_id": begin.SessionID})); err != nil {
		t.Fatalf("Renew RPC: %v", err)
	}
	secondRenew, err := sessions.renew(begin.SessionID)
	if err != nil || !secondRenew.After(firstRenew) {
		t.Fatalf("session renewal did not extend expiry: first=%v second=%v err=%v", firstRenew, secondRenew, err)
	}
	// Make expiry deterministic and allow the renew call itself to perform the
	// expiry transition. The renew result is expired here; if the background
	// sweep wins first, the terminal result is not-found instead.
	sessions.mu.Lock()
	sessions.sessions[begin.SessionID].expiresAt = time.Now().Add(-time.Second)
	sessions.mu.Unlock()
	if _, err := sessions.renew(begin.SessionID); !errors.Is(err, errSessionExpired) && !errors.Is(err, errSessionNotFound) {
		t.Fatalf("expired session remained renewable: %v", err)
	}
	records := sessions.listRecoveries()
	if len(records) != 1 || records[0].SessionID != begin.SessionID || records[0].RepositoryPath != "/file" || records[0].MustBeAbsent != true || records[0].Size != int64(len("durable recovery bytes")) || !strings.Contains(records[0].FailureReason, "expired") {
		t.Fatalf("expired Resource was not preserved with its CAS metadata: %+v", records)
	}
	if data, err := os.ReadFile(stage); err != nil || string(data) != "durable recovery bytes" {
		t.Fatalf("expired staging content=%q err=%v", data, err)
	}
	if err := sessions.close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := newSessionManagerWithTTL(fake, root, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := restarted.close(); err != nil {
			t.Errorf("close restarted session manager: %v", err)
		}
	}()
	if got := restarted.listRecoveries(); len(got) != 1 || got[0].SessionID != begin.SessionID {
		t.Fatalf("recovery did not survive daemon restart: %+v", got)
	}
	if err := restarted.discardRecovery(begin.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("explicit discard left staging file: %v", err)
	}
	restartedService := newService(fake, restarted)
	result, err = restartedService.Call(context.Background(), controlrpc.MethodBeginWrite, marshalTestJSON(t, map[string]any{"path": "/revoked", "resource_schemes": []string{"file"}}))
	if err != nil {
		t.Fatal(err)
	}
	if err := decodeTestResult(t, result, &begin); err != nil {
		t.Fatal(err)
	}
	revokedStage := resourcePathFromURI(t, begin.ResourceURI)
	if err := os.WriteFile(revokedStage, []byte("revoked but retained"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := restartedService.Call(context.Background(), controlrpc.MethodRevoke, marshalTestJSON(t, map[string]string{"session_id": begin.SessionID})); err != nil {
		t.Fatalf("revoke active Resource: %v", err)
	}
	if records := restarted.listRecoveries(); len(records) != 1 || records[0].SessionID != begin.SessionID {
		t.Fatalf("revoke did not preserve session as recovery: %+v", records)
	}
	if data, err := os.ReadFile(revokedStage); err != nil || string(data) != "revoked but retained" {
		t.Fatalf("revoke removed staging: data=%q err=%v", data, err)
	}
}

func TestExpirySweepPreservesUnexpiredSessionsAndReleasesExpiredReads(t *testing.T) {
	root := t.TempDir()
	sessions, err := newSessionManagerWithTTL(&fakeRepositoryUseCases{}, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sessions.close(); err != nil {
			t.Errorf("close sessions: %v", err)
		}
	}()

	activeWrite, err := sessions.beginWrite(context.Background(), "/active")
	if err != nil {
		t.Fatal(err)
	}
	expiredWrite, err := sessions.beginWrite(context.Background(), "/expired")
	if err != nil {
		t.Fatal(err)
	}
	activeReadReleased := 0
	expiredReadReleased := 0
	sessions.mu.Lock()
	now := time.Now()
	sessions.sessions[activeWrite.SessionID].expiresAt = now.Add(time.Second)
	sessions.sessions[expiredWrite.SessionID].expiresAt = now.Add(-time.Second)
	sessions.reads["active-read"] = &readSession{expiresAt: now.Add(time.Second), release: func() error { activeReadReleased++; return nil }}
	sessions.reads["expired-read"] = &readSession{expiresAt: now.Add(-time.Second), release: func() error { expiredReadReleased++; return nil }}
	sessions.mu.Unlock()

	sessions.expireDue(now)

	if _, err := sessions.renew(activeWrite.SessionID); err != nil {
		t.Fatalf("unexpired write session was swept: %v", err)
	}
	if _, err := os.Stat(sessions.expectedStagePath(activeWrite.SessionID)); err != nil {
		t.Fatalf("unexpired staging was removed: %v", err)
	}
	if _, err := os.Stat(sessions.expectedStagePath(expiredWrite.SessionID)); err != nil {
		t.Fatalf("expired write staging should be preserved for recovery: %v", err)
	}
	if records := sessions.listRecoveries(); len(records) != 1 || records[0].SessionID != expiredWrite.SessionID {
		t.Fatalf("sweep recovery candidates=%+v", records)
	}
	if activeReadReleased != 0 || expiredReadReleased != 1 {
		t.Fatalf("read releases: active=%d expired=%d", activeReadReleased, expiredReadReleased)
	}
}

func TestExpirySweepClaimsSessionBeforePersistingRecovery(t *testing.T) {
	root := t.TempDir()
	sessions, err := newSessionManagerWithTTL(&fakeRepositoryUseCases{}, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sessions.close(); err != nil {
			t.Errorf("close sessions: %v", err)
		}
	}()
	result, err := sessions.beginWrite(context.Background(), "/contended")
	if err != nil {
		t.Fatal(err)
	}
	sessions.mu.Lock()
	sessions.sessions[result.SessionID].expiresAt = time.Now().Add(-time.Second)
	files := blockingSessionFiles{started: make(chan struct{}), continueWrite: make(chan struct{}), once: &sync.Once{}}
	sessions.files = files
	sessions.mu.Unlock()

	swept := make(chan struct{})
	go func() { sessions.expireDue(time.Now()); close(swept) }()
	<-files.started
	if _, err := sessions.renew(result.SessionID); !errors.Is(err, errSessionExpired) {
		t.Fatalf("renew during expiry preservation error=%v, want expired", err)
	}
	sessions.expireDue(time.Now())
	close(files.continueWrite)
	<-swept
	if records := sessions.listRecoveries(); len(records) != 1 || records[0].SessionID != result.SessionID {
		t.Fatalf("concurrent sweep produced recovery records %+v", records)
	}
}

func TestDaemonRestartPromotesActiveStagingToRecovery(t *testing.T) {
	root := t.TempDir()
	first, err := newSessionManagerWithTTL(&fakeRepositoryUseCases{}, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	result, err := first.beginWrite(context.Background(), "/file")
	if err != nil {
		t.Fatal(err)
	}
	stagePath := resourcePathFromURI(t, result.ResourceURI)
	if err := os.WriteFile(stagePath, []byte("survive daemon crash"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Stop only the in-memory expiry loop to model process loss before its
	// graceful shutdown preservation path can run.
	close(first.done)
	first.wg.Wait()

	restarted, err := newSessionManagerWithTTL(&fakeRepositoryUseCases{}, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := restarted.close(); err != nil {
			t.Errorf("close restarted manager: %v", err)
		}
	}()
	records := restarted.listRecoveries()
	if len(records) != 1 || records[0].SessionID != result.SessionID || !strings.Contains(records[0].FailureReason, "daemon stopped") {
		t.Fatalf("interrupted active session was not promoted to recovery: %+v", records)
	}
	if data, err := os.ReadFile(stagePath); err != nil || string(data) != "survive daemon crash" {
		t.Fatalf("restart lost interrupted staging: bytes=%q err=%v", data, err)
	}
	manifestData, err := os.ReadFile(restarted.recordPath(result.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	var manifest recoveryRecord
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != 2 || manifest.StagingPath != filepath.ToSlash(filepath.Join("temp", "rpc-sessions", result.SessionID+".stage")) {
		t.Fatalf("crash recovery did not persist the root-relative manifest: %+v", manifest)
	}
}

func TestRecoveryRetryAfterRepositoryRelocationUsesRootRelativeManifest(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	fake := &fakeRepositoryUseCases{}
	first, err := newSessionManagerWithTTL(fake, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	begin, err := first.beginWrite(ctx, "/relocated")
	if err != nil {
		t.Fatal(err)
	}
	stage := resourcePathFromURI(t, begin.ResourceURI)
	want := []byte("recovery bytes survive a root move")
	if err := os.WriteFile(stage, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := first.close(); err != nil {
		t.Fatal(err)
	}
	manifestPath := first.recordPath(begin.SessionID)
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest recoveryRecord
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != 2 || manifest.StagingPath != filepath.ToSlash(filepath.Join("temp", "rpc-sessions", begin.SessionID+".stage")) || filepath.IsAbs(manifest.StagingPath) {
		t.Fatalf("manifest did not store canonical relative staging path: %+v", manifest)
	}

	movedRoot := filepath.Join(t.TempDir(), "restored-repository")
	if err := os.Rename(root, movedRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old absolute staging location unexpectedly remains accessible: %v", err)
	}

	restarted, err := newSessionManagerWithTTL(fake, movedRoot, time.Hour)
	if err != nil {
		t.Fatalf("start Recovery after repository relocation: %v", err)
	}
	defer func() {
		if err := restarted.close(); err != nil {
			t.Errorf("close relocated Repository manager: %v", err)
		}
	}()
	newStage := filepath.Join(movedRoot, "temp", "rpc-sessions", begin.SessionID+".stage")
	if records := restarted.listRecoveries(); len(records) != 1 || records[0].SessionID != begin.SessionID {
		t.Fatalf("relocated Recovery was not loaded: %+v", records)
	}
	manifestData, err = os.ReadFile(filepath.Join(movedRoot, "temp", "rpc-sessions", begin.SessionID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != 2 || manifest.StagingPath != filepath.ToSlash(filepath.Join("temp", "rpc-sessions", begin.SessionID+".stage")) {
		t.Fatalf("manifest was not canonical before Recovery exposure: %+v", manifest)
	}
	if err := restarted.retryRecovery(ctx, begin.SessionID); err != nil {
		t.Fatalf("retry Recovery after relocation: %v", err)
	}
	if string(fake.content) != string(want) || fake.entry == nil || fake.entry.Size != int64(len(want)) {
		t.Fatalf("recovered content mismatch: bytes=%q entry=%+v", fake.content, fake.entry)
	}
	for _, path := range []string{newStage, filepath.Join(movedRoot, "temp", "rpc-sessions", begin.SessionID+".json")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("successful retry did not clean %q: %v", path, err)
		}
	}
}

func TestRecoveryManifestRejectsUnsafeStagingPathsAndSymlinks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, root, stage string, record *recoveryRecord)
	}{
		{name: "absolute version 2 path", mutate: func(_ *testing.T, _, stage string, record *recoveryRecord) { record.StagingPath = stage }},
		{name: "parent traversal", mutate: func(_ *testing.T, _, _ string, record *recoveryRecord) {
			record.StagingPath = "temp/rpc-sessions/../escape.stage"
		}},
		{name: "session filename mismatch", mutate: func(_ *testing.T, _, _ string, record *recoveryRecord) {
			record.StagingPath = filepath.ToSlash(filepath.Join("temp", "rpc-sessions", uuid.NewString()+".stage"))
		}},
		{name: "unsupported v1 manifest", mutate: func(_ *testing.T, _, stage string, record *recoveryRecord) {
			record.Version = 1
			record.StagingPath = stage
		}},
		{name: "staging symlink", mutate: func(t *testing.T, _, stage string, _ *recoveryRecord) {
			if err := os.Remove(stage); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("do not read"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, stage); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "staging directory", mutate: func(t *testing.T, _, stage string, _ *recoveryRecord) {
			if err := os.Remove(stage); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(stage, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			manager, err := newSessionManagerWithTTL(&fakeRepositoryUseCases{}, root, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			begin, err := manager.beginWrite(context.Background(), "/file")
			if err != nil {
				t.Fatal(err)
			}
			stage := resourcePathFromURI(t, begin.ResourceURI)
			if err := os.WriteFile(stage, []byte("data"), 0o600); err != nil {
				t.Fatal(err)
			}
			manager.mu.Lock()
			record := manager.sessions[begin.SessionID].recoveryRecord
			manager.mu.Unlock()
			if err := manager.close(); err != nil {
				t.Fatal(err)
			}
			tc.mutate(t, root, stage, &record)
			recordData, err := json.Marshal(&record)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manager.recordPath(begin.SessionID), recordData, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := newSessionManagerWithTTL(&fakeRepositoryUseCases{}, root, time.Hour); err == nil {
				t.Fatal("startup accepted unsafe Recovery staging")
			}
		})
	}
}

func TestRecoveryRejectsSymlinkedSessionDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "temp"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "temp", "rpc-sessions")); err != nil {
		t.Fatal(err)
	}
	if _, err := newSessionManagerWithTTL(&fakeRepositoryUseCases{}, root, time.Hour); err == nil {
		t.Fatal("startup accepted symlinked Recovery directory")
	}
}

func TestRecoveryRejectsLegacyManifestWithoutTouchingAbsolutePath(t *testing.T) {
	root := t.TempDir()
	manager, err := newSessionManagerWithTTL(&fakeRepositoryUseCases{}, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	begin, err := manager.beginWrite(context.Background(), "/file")
	if err != nil {
		t.Fatal(err)
	}
	stage := resourcePathFromURI(t, begin.ResourceURI)
	if err := os.WriteFile(stage, []byte("current root bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	record := manager.sessions[begin.SessionID].recoveryRecord
	manager.mu.Unlock()
	if err := manager.close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(stage); err != nil {
		t.Fatal(err)
	}
	oldStage := filepath.Join(t.TempDir(), "old-root", "temp", "rpc-sessions", begin.SessionID+".stage")
	if err := os.MkdirAll(filepath.Dir(oldStage), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldStage, []byte("must never be opened"), 0o600); err != nil {
		t.Fatal(err)
	}
	record.Version = 1
	record.StagingPath = oldStage
	data, err := json.Marshal(&record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.recordPath(begin.SessionID), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newSessionManagerWithTTL(&fakeRepositoryUseCases{}, root, time.Hour); err == nil || !strings.Contains(err.Error(), "unsupported Recovery manifest version") {
		t.Fatalf("legacy manifest was not explicitly rejected: %v", err)
	}
	got, err := os.ReadFile(oldStage)
	if err != nil || string(got) != "must never be opened" {
		t.Fatalf("legacy absolute path was touched: bytes=%q err=%v", got, err)
	}
}

func TestRestartCleansStagingWhoseCommitWasDurablyRecorded(t *testing.T) {
	root := t.TempDir()
	first, err := newSessionManagerWithTTL(&fakeRepositoryUseCases{}, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	result, err := first.beginWrite(context.Background(), "/file")
	if err != nil {
		t.Fatal(err)
	}
	stagePath := resourcePathFromURI(t, result.ResourceURI)
	if err := os.WriteFile(stagePath, []byte("already committed"), 0o600); err != nil {
		t.Fatal(err)
	}
	first.mu.Lock()
	session := first.sessions[result.SessionID]
	first.mu.Unlock()
	if err := first.markCommitted(&session.recoveryRecord); err != nil {
		t.Fatal(err)
	}
	close(first.done)
	first.wg.Wait()

	restarted, err := newSessionManagerWithTTL(&fakeRepositoryUseCases{}, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := restarted.close(); err != nil {
			t.Errorf("close restarted manager: %v", err)
		}
	}()
	if records := restarted.listRecoveries(); len(records) != 0 {
		t.Fatalf("committed Resource was incorrectly exposed as recovery: %+v", records)
	}
	if _, err := os.Stat(stagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restart did not clean staging after durable commit marker: %v", err)
	}
}

func TestExpiredCommitIsRejectedWithSessionExpiredRPCError(t *testing.T) {
	fake := &fakeRepositoryUseCases{}
	sessions, err := newSessionManagerWithTTL(fake, t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sessions.close(); err != nil {
			t.Errorf("close sessions: %v", err)
		}
	}()
	service := newService(fake, sessions)
	result, err := service.Call(context.Background(), controlrpc.MethodBeginWrite, marshalTestJSON(t, map[string]any{"path": "/file", "resource_schemes": []string{"file"}}))
	if err != nil {
		t.Fatal(err)
	}
	var begin controlrpc.BeginWriteResult
	if err := decodeTestResult(t, result, &begin); err != nil {
		t.Fatal(err)
	}
	sessions.mu.Lock()
	sessions.sessions[begin.SessionID].expiresAt = time.Now().Add(-time.Second)
	sessions.mu.Unlock()
	_, err = service.Call(context.Background(), controlrpc.MethodCommit, marshalTestJSON(t, map[string]string{"session_id": begin.SessionID}))
	var methodErr *methodError
	if !errors.As(err, &methodErr) || methodErr.Code != controlrpc.CodeResourceSessionExpired {
		t.Fatalf("expired Commit error=%v, want session-expired RPC code", err)
	}
	if records := sessions.listRecoveries(); len(records) != 1 || records[0].SessionID != begin.SessionID {
		t.Fatalf("expired Commit did not preserve staging: %+v", records)
	}
}

func TestExpiredRenewSerializesWithExpirySweep(t *testing.T) {
	fake := &fakeRepositoryUseCases{}
	root := t.TempDir()
	sessions, err := newSessionManagerWithTTL(fake, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	result, err := sessions.beginWrite(context.Background(), "/file")
	if err != nil {
		t.Fatal(err)
	}
	sessions.mu.Lock()
	sessions.sessions[result.SessionID].expiresAt = time.Now().Add(-time.Second)
	sessions.mu.Unlock()
	start := make(chan struct{})
	renewDone := make(chan error, 1)
	sweepDone := make(chan struct{})
	go func() { <-start; _, renewErr := sessions.renew(result.SessionID); renewDone <- renewErr }()
	go func() { <-start; sessions.expireDue(time.Now()); close(sweepDone) }()
	close(start)
	renewErr := <-renewDone
	<-sweepDone
	if renewErr == nil {
		t.Fatal("expired session was renewed")
	}
	if records := sessions.listRecoveries(); len(records) != 1 || records[0].SessionID != result.SessionID {
		t.Fatalf("expired session was not preserved exactly once: %+v", records)
	}
	if _, err := sessions.renew(result.SessionID); !errors.Is(err, errSessionExpired) && !errors.Is(err, errSessionNotFound) {
		t.Fatalf("session became renewable after expiry transition: %v", err)
	}
	stopSessionManagerForCrash(sessions)
	restarted, err := newSessionManagerWithTTL(fake, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := restarted.close(); err != nil {
			t.Errorf("close restarted manager: %v", err)
		}
	}()
	if records := restarted.listRecoveries(); len(records) != 1 || records[0].SessionID != result.SessionID {
		t.Fatalf("expiry recovery did not survive restart: %+v", records)
	}
}

func TestRecoveryPreservationFailuresKeepBeginManifestAndStaging(t *testing.T) {
	failure := errors.New("injected persistence boundary failure")
	for _, test := range []struct {
		name  string
		files failingSessionFiles
	}{
		{name: "staging fsync", files: failingSessionFiles{syncErr: failure}},
		{name: "manifest write", files: failingSessionFiles{writeErr: failure, writePhase: "write"}},
		{name: "manifest fsync", files: failingSessionFiles{writeErr: failure, writePhase: "fsync"}},
		{name: "manifest rename", files: failingSessionFiles{writeErr: failure, writePhase: "rename"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeRepositoryUseCases{putErr: metadata.ErrEntryConflict}
			root := t.TempDir()
			sessions, err := newSessionManagerWithTTL(fake, root, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			result, err := sessions.beginWrite(context.Background(), "/file")
			if err != nil {
				t.Fatal(err)
			}
			stage := resourcePathFromURI(t, result.ResourceURI)
			if err := os.WriteFile(stage, []byte("recoverable payload"), 0o600); err != nil {
				t.Fatal(err)
			}
			sessions.files = test.files
			if err := sessions.commit(context.Background(), result.SessionID); err == nil {
				t.Fatal("Commit unexpectedly succeeded with injected storage failure")
			}
			sessions.mu.Lock()
			_, active := sessions.sessions[result.SessionID]
			_, duplicated := sessions.recoveries[result.SessionID]
			sessions.mu.Unlock()
			if !active || duplicated {
				t.Fatalf("failed preservation must remain in exactly one retryable map: active=%v recovery=%v", active, duplicated)
			}
			if records := sessions.listRecoveries(); len(records) != 1 || records[0].SessionID != result.SessionID {
				t.Fatalf("pending Recovery reconciliation not exposed: %+v", records)
			}
			if _, err := sessions.renew(result.SessionID); !errors.Is(err, errSessionExpired) {
				t.Fatalf("failed Recovery persistence made expired session renewable: %v", err)
			}
			if err := sessions.commit(context.Background(), result.SessionID); !errors.Is(err, errSessionExpired) {
				t.Fatalf("failed Recovery persistence allowed Commit: %v", err)
			}
			if data, err := os.ReadFile(stage); err != nil || string(data) != "recoverable payload" {
				t.Fatalf("staging lost on preservation failure: %q, %v", data, err)
			}
			manifest := sessions.recordPath(result.SessionID)
			if _, err := os.Stat(manifest); err != nil {
				t.Fatalf("Begin-time durable manifest missing: %v", err)
			}
			stopSessionManagerForCrash(sessions)
			restarted, err := newSessionManagerWithTTL(fake, root, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := restarted.close(); err != nil {
					t.Errorf("close restarted manager: %v", err)
				}
			}()
			if records := restarted.listRecoveries(); len(records) != 1 || records[0].SessionID != result.SessionID {
				t.Fatalf("Begin manifest did not reconstruct recovery: %+v", records)
			}
		})
	}
}

func TestExpiredSessionRetryReconcilesBeforeRepositoryPut(t *testing.T) {
	fake := &fakeRepositoryUseCases{putErr: metadata.ErrEntryConflict}
	sessions, err := newSessionManagerWithTTL(fake, t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sessions.close(); err != nil {
			t.Errorf("close sessions: %v", err)
		}
	}()
	result, err := sessions.beginWrite(context.Background(), "/file")
	if err != nil {
		t.Fatal(err)
	}
	stage := resourcePathFromURI(t, result.ResourceURI)
	if err := os.WriteFile(stage, []byte("reconcile before retry"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions.files = failingSessionFiles{writeErr: errors.New("injected recovery rename failure"), writePhase: "rename"}
	if err := sessions.commit(context.Background(), result.SessionID); !errors.Is(err, metadata.ErrEntryConflict) {
		t.Fatalf("Commit error=%v, want initial CAS failure", err)
	}
	if records := sessions.listRecoveries(); len(records) != 1 || records[0].SessionID != result.SessionID {
		t.Fatalf("failed recovery metadata was not exposed for reconciliation: %+v", records)
	}
	fake.putErr = nil
	sessions.files = failingSessionFiles{syncErr: errors.New("injected staging sync failure")}
	if err := sessions.retryRecovery(context.Background(), result.SessionID); err == nil {
		t.Fatal("recovery retry proceeded despite staging sync failure")
	}
	if fake.entry != nil {
		t.Fatalf("Repository Put ran before staging sync: %+v", fake.entry)
	}
	sessions.files = failingSessionFiles{writeErr: errors.New("injected recovery write failure"), writePhase: "write"}
	if err := sessions.retryRecovery(context.Background(), result.SessionID); err == nil {
		t.Fatal("recovery retry proceeded despite metadata persistence failure")
	}
	if fake.entry != nil {
		t.Fatalf("Repository Put ran before Recovery metadata was durable: %+v", fake.entry)
	}
	sessions.files = osSessionFiles{}
	if err := sessions.retryRecovery(context.Background(), result.SessionID); err != nil {
		t.Fatalf("retry after reconciliation storage recovered: %v", err)
	}
	fake.mu.Lock()
	if fake.entry == nil || fake.entry.Revision != 1 || string(fake.content) != "reconcile before retry" {
		t.Fatalf("reconciled retry did not commit expected bytes: entry=%+v content=%q", fake.entry, fake.content)
	}
	fake.mu.Unlock()
	sessions.mu.Lock()
	_, active := sessions.sessions[result.SessionID]
	_, recovery := sessions.recoveries[result.SessionID]
	sessions.mu.Unlock()
	if active || recovery {
		t.Fatalf("successful retry left duplicate/stale session state: active=%v recovery=%v", active, recovery)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful retry did not clean staging: %v", err)
	}
}

func TestDiscardTombstonePrecedesCleanupAndRestartFinishesPartialRemoval(t *testing.T) {
	fake := &fakeRepositoryUseCases{putErr: metadata.ErrEntryConflict}
	root := t.TempDir()
	sessions, err := newSessionManagerWithTTL(fake, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	result, err := sessions.beginWrite(context.Background(), "/file")
	if err != nil {
		t.Fatal(err)
	}
	stage := resourcePathFromURI(t, result.ResourceURI)
	if err := os.WriteFile(stage, []byte("discard me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sessions.commit(context.Background(), result.SessionID); !errors.Is(err, metadata.ErrEntryConflict) {
		t.Fatalf("Commit error=%v", err)
	}
	manifest := sessions.recordPath(result.SessionID)
	sessions.files = failingSessionFiles{removePath: manifest, removeErr: errors.New("injected manifest unlink error")}
	if err := sessions.discardRecovery(result.SessionID); err == nil {
		t.Fatal("partial cleanup error was not reported")
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected stage cleanup to precede manifest cleanup: %v", err)
	}
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("discard tombstone missing after partial cleanup: %v", err)
	}
	var record recoveryRecord
	if err := json.Unmarshal(data, &record); err != nil || !record.Discarding {
		t.Fatalf("manifest is not a durable discard tombstone: %+v err=%v", record, err)
	}
	if records := sessions.listRecoveries(); len(records) != 0 {
		t.Fatalf("discarding record was exposed as recoverable data: %+v", records)
	}
	stopSessionManagerForCrash(sessions)
	cleanup := &sessionManager{
		root: root, directory: filepath.Dir(manifest), files: failingSessionFiles{removePath: manifest, removeErr: errors.New("injected restart cleanup failure")},
		sessions: make(map[string]*writeSession), reads: make(map[string]*readSession), recoveries: make(map[string]*recoveryRecord),
	}
	if err := cleanup.loadRecoveries(); err == nil {
		t.Fatal("startup accepted failed tombstone cleanup instead of retaining a retryable intent")
	}
	if _, err := os.Stat(manifest); err != nil {
		t.Fatalf("failed cleanup attempt lost tombstone: %v", err)
	}
	cleanup.files = osSessionFiles{}
	if err := cleanup.loadRecoveries(); err != nil {
		t.Fatalf("repeated restart cleanup attempt: %v", err)
	}
	if _, err := os.Stat(manifest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repeated cleanup did not remove tombstone: %v", err)
	}
	restarted, err := newSessionManagerWithTTL(fake, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := restarted.close(); err != nil {
			t.Errorf("close restarted manager: %v", err)
		}
	}()
	if records := restarted.listRecoveries(); len(records) != 0 {
		t.Fatalf("discarded session reappeared as recovery: %+v", records)
	}
	if _, err := os.Stat(manifest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restart did not finish tombstone cleanup: %v", err)
	}
}

func TestAbortPersistenceFailureLeavesOriginalStateForRetry(t *testing.T) {
	fake := &fakeRepositoryUseCases{}
	sessions, err := newSessionManagerWithTTL(fake, t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sessions.close(); err != nil {
			t.Errorf("close sessions: %v", err)
		}
	}()
	result, err := sessions.beginWrite(context.Background(), "/file")
	if err != nil {
		t.Fatal(err)
	}
	stage := resourcePathFromURI(t, result.ResourceURI)
	if err := os.WriteFile(stage, []byte("keep until abort intent is durable"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions.files = failingSessionFiles{writeErr: errors.New("injected rename failure"), writePhase: "rename"}
	if err := sessions.abort(result.SessionID); err == nil {
		t.Fatal("Abort persistence failure was not returned")
	}
	if data, err := os.ReadFile(stage); err != nil || string(data) != "keep until abort intent is durable" {
		t.Fatalf("stage removed before durable intent: %q, %v", data, err)
	}
	if _, err := sessions.renew(result.SessionID); err != nil {
		t.Fatalf("failed Abort changed original session state: %v", err)
	}
	sessions.files = osSessionFiles{}
	if err := sessions.abort(result.SessionID); err != nil {
		t.Fatalf("retry Abort after storage recovered: %v", err)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful explicit Abort retained staging: %v", err)
	}
}

func TestCloseRetriesDiscardCleanupWithoutAdvertisingRecovery(t *testing.T) {
	root := t.TempDir()
	sessions, err := newSessionManagerWithTTL(&fakeRepositoryUseCases{}, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	result, err := sessions.beginWrite(context.Background(), "/file")
	if err != nil {
		t.Fatal(err)
	}
	stage := resourcePathFromURI(t, result.ResourceURI)
	if err := os.WriteFile(stage, []byte("explicitly discarded"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions.mu.Lock()
	session := sessions.sessions[result.SessionID]
	sessions.mu.Unlock()
	if err := sessions.markDiscarding(&session.recoveryRecord, "explicit resource abort"); err != nil {
		t.Fatal(err)
	}
	sessions.files = failingSessionFiles{removePath: stage, removeErr: errors.New("injected cleanup failure")}
	if err := sessions.close(); err == nil {
		t.Fatal("shutdown cleanup failure was not reported")
	}
	if records := sessions.listRecoveries(); len(records) != 0 {
		t.Fatalf("Discarding session was inserted into Recovery list: %+v", records)
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("failed shutdown cleanup should leave tombstoned staging for restart: %v", err)
	}
	restarted, err := newSessionManagerWithTTL(&fakeRepositoryUseCases{}, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := restarted.close(); err != nil {
			t.Errorf("close restarted manager: %v", err)
		}
	}()
	if records := restarted.listRecoveries(); len(records) != 0 {
		t.Fatalf("restart exposed tombstoned staging as Recovery: %+v", records)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restart did not finish discard cleanup: %v", err)
	}
}

func TestUncommittedReleasePreservesInsteadOfCommitting(t *testing.T) {
	fake := &fakeRepositoryUseCases{}
	sessions, err := newSessionManagerWithTTL(fake, t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sessions.close(); err != nil {
			t.Errorf("close sessions: %v", err)
		}
	}()
	result, err := sessions.beginWrite(context.Background(), "/file")
	if err != nil {
		t.Fatal(err)
	}
	stage := resourcePathFromURI(t, result.ResourceURI)
	if err := os.WriteFile(stage, []byte("uncommitted close"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sessions.release(result.SessionID); err != nil {
		t.Fatal(err)
	}
	if fake.entry != nil {
		t.Fatalf("generic Release committed repository data: %+v", fake.entry)
	}
	if records := sessions.listRecoveries(); len(records) != 1 || records[0].SessionID != result.SessionID {
		t.Fatalf("Release did not preserve abandoned staging: %+v", records)
	}
}

func TestUnlinkedWriteResourceIsDiscardedInsteadOfRecovered(t *testing.T) {
	fake := &fakeRepositoryUseCases{}
	sessions, err := newSessionManagerWithTTL(fake, t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sessions.close(); err != nil {
			t.Errorf("close sessions: %v", err)
		}
	}()
	service := newService(fake, sessions)
	result, err := service.Call(context.Background(), controlrpc.MethodBeginWrite, marshalTestJSON(t, map[string]any{"path": "/file", "resource_schemes": []string{"file"}}))
	if err != nil {
		t.Fatal(err)
	}
	var begin controlrpc.BeginWriteResult
	if err := decodeTestResult(t, result, &begin); err != nil {
		t.Fatal(err)
	}
	stage := resourcePathFromURI(t, begin.ResourceURI)
	if err := os.WriteFile(stage, []byte("private dirty bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	params := marshalTestJSON(t, map[string]string{"session_id": begin.SessionID})
	if _, err := service.Call(context.Background(), controlrpc.MethodMarkUnlinked, params); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.renew(begin.SessionID); err != nil {
		t.Fatalf("open unlinked handle could not renew its session: %v", err)
	}
	if err := sessions.commit(context.Background(), begin.SessionID); !errors.Is(err, errSessionClosed) {
		t.Fatalf("unlinked write was commit eligible: %v", err)
	}
	if err := os.WriteFile(stage, []byte("post-unlink write remains private"), 0o600); err != nil {
		t.Fatalf("open unlinked handle could not continue private I/O: %v", err)
	}
	if records := sessions.listRecoveries(); len(records) != 0 {
		t.Fatalf("explicit unlink intent was exposed as ordinary recovery: %+v", records)
	}
	if _, err := service.Call(context.Background(), controlrpc.MethodRelease, params); err != nil {
		t.Fatalf("final release cleanup: %v", err)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final release retained explicitly discarded staging: %v", err)
	}
	if records := sessions.listRecoveries(); len(records) != 0 || fake.entry != nil {
		t.Fatalf("unlinked write reappeared: recovery=%+v entry=%+v", records, fake.entry)
	}
}

func TestUnlinkedIntentSurvivesDaemonRestartWithoutRecovery(t *testing.T) {
	root := t.TempDir()
	fake := &fakeRepositoryUseCases{}
	sessions, err := newSessionManagerWithTTL(fake, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	begin, err := sessions.beginWrite(context.Background(), "/file")
	if err != nil {
		t.Fatal(err)
	}
	stage := resourcePathFromURI(t, begin.ResourceURI)
	if err := os.WriteFile(stage, []byte("unlinked private data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sessions.markUnlinked(begin.SessionID); err != nil {
		t.Fatal(err)
	}
	stopSessionManagerForCrash(sessions)
	restarted, err := newSessionManagerWithTTL(fake, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := restarted.close(); err != nil {
			t.Errorf("close restarted manager: %v", err)
		}
	}()
	if records := restarted.listRecoveries(); len(records) != 0 {
		t.Fatalf("unlinked tombstone was exposed as ordinary Recovery: %+v", records)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restart did not finish unlinked staging cleanup: %v", err)
	}
}

func TestUnlinkRemovalFailureRetainsTombstoneForRetry(t *testing.T) {
	fake := &fakeRepositoryUseCases{entry: &metadata.Entry{ID: "entry-1", Kind: metadata.EntryKindFile, Revision: 1}}
	root := t.TempDir()
	sessions, err := newSessionManagerWithTTL(fake, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	begin, err := sessions.beginWrite(context.Background(), "/file", true)
	if err != nil {
		t.Fatal(err)
	}
	stage := resourcePathFromURI(t, begin.ResourceURI)
	if err := os.WriteFile(stage, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sessions.markUnlinked(begin.SessionID); err != nil {
		t.Fatal(err)
	}
	fake.removeEntryErr = errors.New("injected metadata delete failure")
	if err := sessions.release(begin.SessionID); err == nil {
		t.Fatal("Release reported success although tombstone namespace removal failed")
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("failed unlink discarded staging: %v", err)
	}
	if _, err := os.Stat(sessions.recordPath(begin.SessionID)); err != nil {
		t.Fatalf("failed unlink lost durable tombstone: %v", err)
	}
	if records := sessions.listRecoveries(); len(records) != 0 {
		t.Fatalf("unlink tombstone was exposed as ordinary Recovery: %+v", records)
	}
	fake.removeEntryErr = nil
	if err := sessions.release(begin.SessionID); err != nil {
		t.Fatalf("retry tombstone cleanup: %v", err)
	}
	if fake.entry != nil {
		t.Fatalf("retry did not remove stable Entry: %+v", fake.entry)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retry retained discarded staging: %v", err)
	}
	if err := sessions.close(); err != nil {
		t.Fatal(err)
	}
}

func stopSessionManagerForCrash(sessions *sessionManager) {
	close(sessions.done)
	sessions.wg.Wait()
}

func TestCommitAndExpirySerializeAndCommitFailureIsRecoverable(t *testing.T) {
	fake := &fakeRepositoryUseCases{putStarted: make(chan struct{}, 1), putContinue: make(chan struct{})}
	sessions, err := newSessionManagerWithTTL(fake, t.TempDir(), 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sessions.close(); err != nil {
			t.Errorf("close sessions: %v", err)
		}
	}()
	service := newService(fake, sessions)
	result, err := service.Call(context.Background(), controlrpc.MethodBeginWrite, marshalTestJSON(t, map[string]any{"path": "/file", "resource_schemes": []string{"file"}}))
	if err != nil {
		t.Fatal(err)
	}
	var begin controlrpc.BeginWriteResult
	if err := decodeTestResult(t, result, &begin); err != nil {
		t.Fatal(err)
	}
	stage := resourcePathFromURI(t, begin.ResourceURI)
	if err := os.WriteFile(stage, []byte("commit wins"), 0o600); err != nil {
		t.Fatal(err)
	}
	commitDone := make(chan error, 1)
	go func() { commitDone <- sessions.commit(context.Background(), begin.SessionID) }()
	select {
	case <-fake.putStarted:
	case <-time.After(time.Second):
		t.Fatal("Commit did not enter Repository")
	}
	time.Sleep(250 * time.Millisecond)
	sessions.expireDue(time.Now())
	close(fake.putContinue)
	if err := <-commitDone; err != nil {
		t.Fatalf("Commit that began before expiry did not finish: %v", err)
	}
	if records := sessions.listRecoveries(); len(records) != 0 {
		t.Fatalf("expiry raced past in-flight Commit: %+v", records)
	}

	// A later CAS conflict must retain the bytes and the original expectation.
	fake.putStarted = nil
	fake.putContinue = nil
	fake.mu.Lock()
	fake.entry = &metadata.Entry{ID: "entry-1", Name: "file", Kind: metadata.EntryKindFile, Revision: 7}
	fake.content = []byte("winner")
	fake.mu.Unlock()
	result, err = service.Call(context.Background(), controlrpc.MethodBeginWrite, marshalTestJSON(t, map[string]any{"path": "/file", "resource_schemes": []string{"file"}}))
	if err != nil {
		t.Fatal(err)
	}
	if err := decodeTestResult(t, result, &begin); err != nil {
		t.Fatal(err)
	}
	stage = resourcePathFromURI(t, begin.ResourceURI)
	if err := os.WriteFile(stage, []byte("stale edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.entry.Revision++
	fake.mu.Unlock()
	if err := sessions.commit(context.Background(), begin.SessionID); !errors.Is(err, metadata.ErrEntryConflict) {
		t.Fatalf("stale CAS result=%v, want conflict", err)
	}
	records := sessions.listRecoveries()
	if len(records) != 1 || records[0].ExpectedEntryID != "entry-1" || records[0].ExpectedRevision != 7 || !strings.Contains(records[0].FailureReason, "conflict") || records[0].CreatedAt.IsZero() || records[0].UpdatedAt.Before(records[0].CreatedAt) {
		t.Fatalf("CAS failure recovery metadata=%+v", records)
	}
	if data, err := os.ReadFile(stage); err != nil || string(data) != "stale edit" {
		t.Fatalf("CAS failure did not retain staging: content=%q err=%v", data, err)
	}
	if err := sessions.retryRecovery(context.Background(), begin.SessionID); !errors.Is(err, metadata.ErrEntryConflict) {
		t.Fatalf("explicit recovery retry bypassed CAS: %v", err)
	}
	if records := sessions.listRecoveries(); len(records) != 1 || records[0].ExpectedRevision != 7 {
		t.Fatalf("failed recovery retry changed CAS expectation: %+v", records)
	}
}

func decodeTestResult(t *testing.T, result any, target any) error {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

func TestWriteResourceSessionCommitsAndReleasesViaControlMethods(t *testing.T) {
	root := t.TempDir()
	fake := &fakeRepositoryUseCases{}
	sessions, err := newSessionManager(fake, root)
	if err != nil {
		t.Fatal(err)
	}
	service := newService(fake, sessions)
	t.Cleanup(func() {
		if err := sessions.close(); err != nil {
			t.Errorf("close sessions: %v", err)
		}
	})

	var begin controlrpc.BeginWriteResult
	params := marshalTestJSON(t, map[string]any{"path": "/file", "resource_schemes": []string{"file"}})
	result, err := service.Call(context.Background(), controlrpc.MethodBeginWrite, params)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &begin); err != nil {
		t.Fatal(err)
	}
	resourcePath := resourcePathFromURI(t, begin.ResourceURI)
	if err := os.WriteFile(resourcePath, []byte("session payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionParams := marshalTestJSON(t, map[string]string{"session_id": begin.SessionID})
	if _, err := service.Call(context.Background(), controlrpc.MethodCommit, sessionParams); err != nil {
		t.Fatalf("commit Resource session: %v", err)
	}
	fake.mu.Lock()
	if fake.entry == nil || fake.entry.Revision != 1 || string(fake.content) != "session payload" {
		t.Errorf("committed result mismatch: entry=%+v content=%q", fake.entry, fake.content)
	}
	fake.mu.Unlock()
	if _, err := service.Call(context.Background(), controlrpc.MethodRelease, sessionParams); err != nil {
		t.Fatalf("release Resource session: %v", err)
	}
	if _, err := os.Stat(resourcePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("released Resource still exists: %v", err)
	}
}

func TestWriteResourceSessionUsesRevisionCAS(t *testing.T) {
	fake := &fakeRepositoryUseCases{entry: &metadata.Entry{ID: "entry-1", Name: "file", Kind: metadata.EntryKindFile, Revision: 4}, content: []byte("old")}
	sessions, err := newSessionManager(fake, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sessions.close(); err != nil {
			t.Errorf("close sessions: %v", err)
		}
	}()
	service := newService(fake, sessions)
	params := marshalTestJSON(t, map[string]any{"path": "/file", "resource_schemes": []string{"file"}})
	result, err := service.Call(context.Background(), controlrpc.MethodBeginWrite, params)
	if err != nil {
		t.Fatal(err)
	}
	var begin controlrpc.BeginWriteResult
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &begin); err != nil {
		t.Fatal(err)
	}
	resourcePath := resourcePathFromURI(t, begin.ResourceURI)
	if err := os.WriteFile(resourcePath, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.entry.Revision++
	fake.mu.Unlock()
	sessionParams := marshalTestJSON(t, map[string]string{"session_id": begin.SessionID})
	if _, err := service.Call(context.Background(), controlrpc.MethodCommit, sessionParams); err == nil {
		t.Fatal("stale write Resource session committed")
	} else {
		var methodErr *methodError
		if !errors.As(err, &methodErr) || methodErr.Code != jsonRPCEntryConflict {
			t.Fatalf("expected mapped revision conflict, got %v", err)
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.entry.Revision != 5 || string(fake.content) != "old" {
		t.Fatalf("conflicted session changed persisted state: entry=%+v content=%q", fake.entry, fake.content)
	}
}

func marshalTestJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func resourcePathFromURI(t *testing.T, resourceURI string) string {
	t.Helper()
	u, err := url.Parse(resourceURI)
	if err != nil || u.Scheme != "file" || u.Host != "" {
		t.Fatalf("invalid local Resource URI %q: %v", resourceURI, err)
	}
	return filepath.FromSlash(u.Path)
}
