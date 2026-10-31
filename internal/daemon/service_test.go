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

	"github.com/kamichidu/go-nando/internal/app"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/repository"
	"github.com/kamichidu/go-nando/pkg/controlrpc"
)

type fakeRepositoryUseCases struct {
	mu               sync.Mutex
	entry            *metadata.Entry
	content          []byte
	readReleaseCount int
	putStarted       chan struct{}
	putContinue      chan struct{}
}

func (f *fakeRepositoryUseCases) Stat(_ context.Context, path string) (metadata.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.entry == nil || path != "/file" {
		return metadata.Entry{}, metadata.ErrEntryNotFound
	}
	return *f.entry, nil
}

func (f *fakeRepositoryUseCases) List(_ context.Context, _ string) ([]metadata.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.entry == nil {
		return nil, nil
	}
	return []metadata.Entry{*f.entry}, nil
}

func (f *fakeRepositoryUseCases) Move(context.Context, app.MoveInput) error { return nil }
func (f *fakeRepositoryUseCases) Remove(context.Context, string) error      { return nil }
func (f *fakeRepositoryUseCases) GC(context.Context) (int, error)           { return 0, nil }
func (f *fakeRepositoryUseCases) MaintenanceOnce(context.Context) error     { return nil }
func (f *fakeRepositoryUseCases) Doctor(context.Context) error              { return nil }
func (f *fakeRepositoryUseCases) Sync(context.Context) error                { return nil }

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

func TestResourceSessionsExpireRenewAndPersistRecovery(t *testing.T) {
	fake := &fakeRepositoryUseCases{}
	root := t.TempDir()
	sessions, err := newSessionManagerWithTTL(fake, root, 120*time.Millisecond)
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
	if err := os.WriteFile(stage, []byte("durable recovery bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	firstRenew, err := sessions.renew(begin.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Call(context.Background(), controlrpc.MethodRenew, marshalTestJSON(t, map[string]string{"session_id": begin.SessionID})); err != nil {
		t.Fatalf("Renew RPC: %v", err)
	}
	time.Sleep(70 * time.Millisecond)
	secondRenew, err := sessions.renew(begin.SessionID)
	if err != nil || !secondRenew.After(firstRenew) {
		t.Fatalf("session renewal did not extend expiry: first=%v second=%v err=%v", firstRenew, secondRenew, err)
	}
	time.Sleep(70 * time.Millisecond)
	if _, err := sessions.renew(begin.SessionID); err != nil {
		t.Fatalf("renewed session expired at original deadline: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	sessions.expireDue(time.Now())
	if _, err := sessions.renew(begin.SessionID); !errors.Is(err, errSessionNotFound) {
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
	if err := os.WriteFile(revokedStage, []byte("revoked but retained"), 0600); err != nil {
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
	if err := os.WriteFile(stagePath, []byte("survive daemon crash"), 0600); err != nil {
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
	if err := os.WriteFile(stagePath, []byte("already committed"), 0600); err != nil {
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
	var methodErr *controlrpc.MethodError
	if !errors.As(err, &methodErr) || methodErr.Code != controlrpc.CodeResourceSessionExpired {
		t.Fatalf("expired Commit error=%v, want session-expired RPC code", err)
	}
	if records := sessions.listRecoveries(); len(records) != 1 || records[0].SessionID != begin.SessionID {
		t.Fatalf("expired Commit did not preserve staging: %+v", records)
	}
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
	if err := os.WriteFile(stage, []byte("commit wins"), 0600); err != nil {
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
	if err := os.WriteFile(stage, []byte("stale edit"), 0600); err != nil {
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
	if err := os.WriteFile(resourcePath, []byte("session payload"), 0600); err != nil {
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
	if err := os.WriteFile(resourcePath, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.entry.Revision++
	fake.mu.Unlock()
	sessionParams := marshalTestJSON(t, map[string]string{"session_id": begin.SessionID})
	if _, err := service.Call(context.Background(), controlrpc.MethodCommit, sessionParams); err == nil {
		t.Fatal("stale write Resource session committed")
	} else {
		var methodErr *controlrpc.MethodError
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
