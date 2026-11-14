package daemon

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kamichidu/go-nando/controlrpc"
	"github.com/kamichidu/go-nando/internal/app"
	"github.com/kamichidu/go-nando/internal/content"
	coordinatorsqlite "github.com/kamichidu/go-nando/internal/coordinator/sqlite"
	"github.com/kamichidu/go-nando/internal/metadata"
	metadatasqlite "github.com/kamichidu/go-nando/internal/metadata/sqlite"
	"github.com/kamichidu/go-nando/internal/objectstore/local"
	"github.com/kamichidu/go-nando/internal/repository"
)

func TestDaemonWriteResourceVerticalSlice(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	if err := app.InitRepository(ctx, repoDir); err != nil {
		t.Fatal(err)
	}
	application, closeRepository := openTestApplication(t, repoDir)
	defer func() {
		if err := closeRepository(); err != nil {
			t.Errorf("close Repository: %v", err)
		}
	}()
	unlock, err := AcquireRepositoryLock(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unlock(); err != nil {
			t.Errorf("release Repository lock: %v", err)
		}
	}()

	socketPath := filepath.Join(t.TempDir(), "socket", "nando.sock")
	endpoint := (&url.URL{Scheme: "unix", Path: socketPath}).String()
	serveCtx, cancel := context.WithCancel(ctx)
	serveErr := make(chan error, 1)
	go func() { serveErr <- Serve(serveCtx, application, repoDir, ListenConfig{Endpoint: endpoint}) }()
	serverStopped := false
	t.Cleanup(func() {
		if serverStopped {
			return
		}
		cancel()
		select {
		case err := <-serveErr:
			if err != nil {
				t.Errorf("stop daemon: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop")
		}
	})

	client, err := controlrpc.NewClient(controlrpc.ClientConfig{Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForDaemon(ctx, client); err != nil {
		t.Fatal(err)
	}
	var begin controlrpc.BeginWriteResult
	beginParams := map[string]any{"path": "/rpc.txt", "resource_schemes": []string{"file"}}
	if err := client.Call(ctx, controlrpc.MethodBeginWrite, beginParams, &begin); err != nil {
		t.Fatal(err)
	}
	resourceURL, err := url.Parse(begin.ResourceURI)
	if err != nil || resourceURL.Scheme != "file" || resourceURL.Host != "" {
		t.Fatalf("daemon returned inaccessible Resource URI %q: %v", begin.ResourceURI, err)
	}
	if err := os.WriteFile(resourceURL.Path, []byte("JSON-RPC resource payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	resource, err := os.OpenFile(resourceURL.Path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := resource.Sync(); err != nil {
		t.Fatal(errors.Join(err, resource.Close()))
	}
	if err := resource.Close(); err != nil {
		t.Fatal(err)
	}
	params := map[string]string{"session_id": begin.SessionID}
	if err := client.Call(ctx, controlrpc.MethodCommit, params, nil); err != nil {
		t.Fatal(err)
	}
	if err := client.Call(ctx, controlrpc.MethodCommit, params, nil); err != nil {
		t.Fatalf("idempotent retry of completed Commit: %v", err)
	}
	entry, err := application.Stat(ctx, "/rpc.txt")
	if err != nil || entry.Size != int64(len("JSON-RPC resource payload")) || entry.Revision != 1 {
		t.Fatalf("Local Commit result: entry=%+v err=%v", entry, err)
	}
	if err := client.Call(ctx, controlrpc.MethodRelease, params, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(resourceURL.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Release did not remove mutable Resource: %v", err)
	}

	var stale controlrpc.BeginWriteResult
	if err := client.Call(ctx, controlrpc.MethodBeginWrite, beginParams, &stale); err != nil {
		t.Fatal(err)
	}
	staleURL, err := url.Parse(stale.ResourceURI)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staleURL.Path, []byte("stale session"), 0o600); err != nil {
		t.Fatal(err)
	}
	winner := filepath.Join(t.TempDir(), "winner")
	if err := os.WriteFile(winner, []byte("concurrent winner"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := application.Put(ctx, app.PutInput{LocalPath: winner, RepositoryPath: "/rpc.txt"}); err != nil {
		t.Fatalf("concurrent Repository write: %v", err)
	}
	staleParams := map[string]string{"session_id": stale.SessionID}
	if err := client.Call(ctx, controlrpc.MethodCommit, staleParams, nil); err == nil {
		t.Fatal("stale Resource session committed after a concurrent revision")
	} else {
		var remoteErr *controlrpc.RemoteError
		if !errors.As(err, &remoteErr) || remoteErr.Code != controlrpc.CodeEntryConflict {
			t.Fatalf("expected revision conflict from daemon, got %v", err)
		}
	}
	if err := client.Call(ctx, controlrpc.MethodRelease, staleParams, nil); err != nil {
		t.Fatal(err)
	}
	entry, err = application.Stat(ctx, "/rpc.txt")
	if err != nil || entry.Revision != 2 || entry.Size != int64(len("concurrent winner")) {
		t.Fatalf("stale session changed the concurrent winner: entry=%+v err=%v", entry, err)
	}

	cancel()
	if err := <-serveErr; err != nil {
		t.Fatalf("daemon shutdown: %v", err)
	}
	serverStopped = true
}

func TestRecoveryRetryAfterRepositoryMoveWithRealLocalRepository(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := app.InitRepository(ctx, root); err != nil {
		t.Fatal(err)
	}
	application, closeRepository := openTestApplication(t, root)
	manager, err := newSessionManagerWithTTL(application, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	begin, err := manager.beginWrite(ctx, "/moved-recovery")
	if err != nil {
		t.Fatal(err)
	}
	stage := resourcePathFromURI(t, begin.ResourceURI)
	want := []byte("real Repository recovery payload after relocation")
	if err := os.WriteFile(stage, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.close(); err != nil {
		t.Fatal(err)
	}
	if err := closeRepository(); err != nil {
		t.Fatal(err)
	}
	movedRoot := filepath.Join(t.TempDir(), "repository-at-new-path")
	if err := os.Rename(root, movedRoot); err != nil {
		t.Fatal(err)
	}
	application, closeRepository = openTestApplication(t, movedRoot)
	defer func() {
		if err := closeRepository(); err != nil {
			t.Errorf("close relocated Repository: %v", err)
		}
	}()
	manager, err = newSessionManagerWithTTL(application, movedRoot, time.Hour)
	if err != nil {
		t.Fatalf("start Recovery from relocated Repository: %v", err)
	}
	defer func() {
		if err := manager.close(); err != nil {
			t.Errorf("close relocated session manager: %v", err)
		}
	}()
	if records := manager.listRecoveries(); len(records) != 1 || records[0].SessionID != begin.SessionID {
		t.Fatalf("Recovery was not loaded after relocation: %+v", records)
	}
	if err := manager.retryRecovery(ctx, begin.SessionID); err != nil {
		t.Fatalf("retry relocated Recovery: %v", err)
	}
	entry, err := application.Stat(ctx, "/moved-recovery")
	if err != nil || entry.Size != int64(len(want)) {
		t.Fatalf("committed Entry=%+v err=%v", entry, err)
	}
	_, readPath, release, err := application.BeginReadResource(ctx, "/moved-recovery")
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := os.ReadFile(readPath)
	releaseErr := release()
	if readErr != nil || releaseErr != nil || string(got) != string(want) {
		t.Fatalf("recovered Repository bytes=%q readErr=%v releaseErr=%v", got, readErr, releaseErr)
	}
	for _, path := range []string{
		filepath.Join(movedRoot, "temp", "rpc-sessions", begin.SessionID+".stage"),
		filepath.Join(movedRoot, "temp", "rpc-sessions", begin.SessionID+".json"),
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("successful Recovery retry left %q: %v", path, err)
		}
	}
}

func TestStableEntryWriteSessionFollowsRenameWithOriginalCAS(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	if err := app.InitRepository(ctx, repoDir); err != nil {
		t.Fatal(err)
	}
	application, closeRepository := openTestApplication(t, repoDir)
	defer func() {
		if err := closeRepository(); err != nil {
			t.Errorf("close Repository: %v", err)
		}
	}()
	initial := filepath.Join(t.TempDir(), "initial")
	if err := os.WriteFile(initial, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := application.Put(ctx, app.PutInput{LocalPath: initial, RepositoryPath: "/old-name"}); err != nil {
		t.Fatal(err)
	}
	sessions, err := newSessionManager(application, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sessions.close(); err != nil {
			t.Errorf("close sessions: %v", err)
		}
	}()
	service := newService(application, sessions)
	result, err := service.Call(ctx, controlrpc.MethodBeginWrite, marshalTestJSON(t, map[string]any{"path": "/old-name", "resource_schemes": []string{"file"}, "follow_entry_id": true}))
	if err != nil {
		t.Fatal(err)
	}
	var begin controlrpc.BeginWriteResult
	if err := decodeTestResult(t, result, &begin); err != nil {
		t.Fatal(err)
	}
	stage := resourcePathFromURI(t, begin.ResourceURI)
	if err := os.WriteFile(stage, []byte("renamed handle bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	entryID := beginEntryIDForTest(t, application, ctx, "/old-name")
	if _, err := service.Call(ctx, controlrpc.MethodMoveEntry, marshalTestJSON(t, controlrpc.MoveEntryParams{EntryID: entryID, Destination: "/new-name"})); err != nil {
		t.Fatalf("move stable Entry through public RPC contract: %v", err)
	}
	resolved, err := service.Call(ctx, controlrpc.MethodResolveEntry, marshalTestJSON(t, map[string]string{"entry_id": beginEntryIDForTest(t, application, ctx, "/new-name")}))
	if err != nil {
		t.Fatalf("resolve stable Entry after rename: %v", err)
	}
	var resolution controlrpc.ResolveEntryResult
	if err := decodeTestResult(t, resolved, &resolution); err != nil || resolution.Path != "/new-name" {
		t.Fatalf("stable Entry resolution=%+v err=%v", resolution, err)
	}
	readResult, err := service.Call(ctx, controlrpc.MethodBeginRead, marshalTestJSON(t, controlrpc.BeginReadParams{EntryID: entryID, ResourceSchemes: []string{"file"}}))
	if err != nil {
		t.Fatalf("BeginRead by Entry ID after rename: %v", err)
	}
	var read controlrpc.BeginReadResult
	if err := decodeTestResult(t, readResult, &read); err != nil || read.Entry.ID != entryID {
		t.Fatalf("read Resource did not follow renamed Entry: result=%+v err=%v", read, err)
	}
	if _, err := service.Call(ctx, controlrpc.MethodRelease, marshalTestJSON(t, map[string]string{"session_id": read.SessionID})); err != nil {
		t.Fatalf("release read after rename: %v", err)
	}
	writeResult, err := service.Call(ctx, controlrpc.MethodBeginWrite, marshalTestJSON(t, controlrpc.BeginWriteParams{EntryID: entryID, FollowEntryID: true, ResourceSchemes: []string{"file"}}))
	if err != nil {
		t.Fatalf("BeginWrite by Entry ID after rename: %v", err)
	}
	var byID controlrpc.BeginWriteResult
	if err := decodeTestResult(t, writeResult, &byID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Call(ctx, controlrpc.MethodAbort, marshalTestJSON(t, map[string]string{"session_id": byID.SessionID})); err != nil {
		t.Fatalf("abort extra stable-ID session: %v", err)
	}
	params := marshalTestJSON(t, map[string]string{"session_id": begin.SessionID})
	if _, err := service.Call(ctx, controlrpc.MethodCommit, params); err != nil {
		t.Fatalf("commit after rename: %v", err)
	}
	entry, err := application.Stat(ctx, "/new-name")
	if err != nil || entry.Revision != 3 || entry.Size != int64(len("renamed handle bytes")) {
		t.Fatalf("renamed Entry commit result: entry=%+v err=%v", entry, err)
	}
	if _, err := application.Stat(ctx, "/old-name"); !errors.Is(err, metadata.ErrEntryNotFound) {
		t.Fatalf("old path unexpectedly exists after rename: %v", err)
	}
}

func TestRecoveryRetryPreservesLatestModeForExistingEntries(t *testing.T) {
	for _, followEntryID := range []bool{false, true} {
		name := "path expectation"
		if followEntryID {
			name = "stable Entry ID across rename"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			if err := app.InitRepository(ctx, root); err != nil {
				t.Fatal(err)
			}
			application, closeRepository := openTestApplication(t, root)
			defer func() {
				if err := closeRepository(); err != nil {
					t.Errorf("close repository: %v", err)
				}
			}()
			initial := filepath.Join(t.TempDir(), "initial")
			if err := os.WriteFile(initial, []byte("before"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := application.Put(ctx, app.PutInput{LocalPath: initial, RepositoryPath: "/old"}); err != nil {
				t.Fatal(err)
			}
			manager, err := newSessionManager(application, root)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := manager.close(); err != nil {
					t.Errorf("close session manager: %v", err)
				}
			}()
			begin, err := manager.beginWrite(ctx, "/old", followEntryID)
			if err != nil {
				t.Fatal(err)
			}
			stage := resourcePathFromURI(t, begin.ResourceURI)
			if err := os.WriteFile(stage, []byte("after"), 0o600); err != nil {
				t.Fatal(err)
			}
			if followEntryID {
				id, err := application.Stat(ctx, "/old")
				if err != nil {
					t.Fatal(err)
				}
				if err := application.MoveEntry(ctx, id.ID, "/new"); err != nil {
					t.Fatal(err)
				}
			}
			path := "/old"
			if followEntryID {
				path = "/new"
			}
			entry, err := application.Stat(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			if err := application.ChmodEntry(ctx, entry.ID, 0o640); err != nil {
				t.Fatal(err)
			}
			if !followEntryID {
				// Path/revision CAS correctly rejects an Entry changed by chmod.
				if err := manager.commit(ctx, begin.SessionID); !errors.Is(err, metadata.ErrEntryConflict) {
					t.Fatalf("commit after external chmod = %v, want Entry conflict", err)
				}
				return
			}
			// Fail before the repository commit, then retry from the durable
			// record after chmod. Stable Entry ID semantics permit this while
			// retaining the original content identity checks.
			manager.files = &failNextSyncFile{sessionFiles: manager.files}
			if err := manager.commit(ctx, begin.SessionID); err == nil {
				t.Fatal("first commit unexpectedly succeeded")
			}
			if manager.recoveries[begin.SessionID] == nil {
				t.Fatal("failed commit was not preserved for recovery")
			}
			// Reopen the durable record to exercise the restart/recovery path.
			if err := manager.close(); err != nil {
				t.Fatal(err)
			}
			manager, err = newSessionManager(application, root)
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.retryRecovery(ctx, begin.SessionID); err != nil {
				t.Fatal(err)
			}
			entry, err = application.Stat(ctx, path)
			if err != nil || entry.Mode&0o7777 != 0o640 {
				t.Fatalf("mode after retry = %04o, err=%v; want 0640", entry.Mode&0o7777, err)
			}
			// The ordinary commit path uses the same mode rule.
			ordinary, err := manager.beginWrite(ctx, path, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(resourcePathFromURI(t, ordinary.ResourceURI), []byte("ordinary commit"), 0o600); err != nil {
				t.Fatal(err)
			}
			entry, err = application.Stat(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			// A chmod after Begin remains the Repository authority at Commit.
			if err := application.ChmodEntry(ctx, entry.ID, 0o604); err != nil {
				t.Fatal(err)
			}
			if err := manager.commit(ctx, ordinary.SessionID); err != nil {
				t.Fatal(err)
			}
			entry, err = application.Stat(ctx, path)
			if err != nil || entry.Mode&0o7777 != 0o604 {
				t.Fatalf("mode after ordinary commit = %04o, err=%v; want 0604", entry.Mode&0o7777, err)
			}
		})
	}
}

func TestRecoveryRetryAppliesBeginModeForNewEntry(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := app.InitRepository(ctx, root); err != nil {
		t.Fatal(err)
	}
	application, closeRepository := openTestApplication(t, root)
	defer func() {
		if err := closeRepository(); err != nil {
			t.Errorf("close repository: %v", err)
		}
	}()
	manager, err := newSessionManager(application, root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := manager.close(); err != nil {
			t.Errorf("close session manager: %v", err)
		}
	}()
	begin, err := manager.beginWrite(ctx, "/new")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resourcePathFromURI(t, begin.ResourceURI), []byte("new content"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager.files = &failNextSyncFile{sessionFiles: manager.files}
	if err := manager.commit(ctx, begin.SessionID); err == nil {
		t.Fatal("first commit unexpectedly succeeded")
	}
	if err := manager.close(); err != nil {
		t.Fatal(err)
	}
	manager, err = newSessionManager(application, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.retryRecovery(ctx, begin.SessionID); err != nil {
		t.Fatal(err)
	}
	entry, err := application.Stat(ctx, "/new")
	if err != nil || entry.Mode&0o7777 != 0o644 {
		t.Fatalf("new Entry recovery mode = %04o, err=%v; want 0644", entry.Mode&0o7777, err)
	}
}

type failNextSyncFile struct {
	sessionFiles
	failed bool
}

func (f *failNextSyncFile) SyncFile(path string) error {
	if !f.failed {
		f.failed = true
		return errors.New("injected staging sync failure")
	}
	return f.sessionFiles.SyncFile(path)
}

func beginEntryIDForTest(t *testing.T, application *app.App, ctx context.Context, repositoryPath string) string {
	t.Helper()
	entry, err := application.Stat(ctx, repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(entry.ID)
}

func TestStableEntryWriteSessionRejectsInterveningContentUpdate(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	if err := app.InitRepository(ctx, repoDir); err != nil {
		t.Fatal(err)
	}
	application, closeRepository := openTestApplication(t, repoDir)
	defer func() {
		if err := closeRepository(); err != nil {
			t.Errorf("close Repository: %v", err)
		}
	}()
	initial := filepath.Join(t.TempDir(), "initial")
	if err := os.WriteFile(initial, []byte("initial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := application.Put(ctx, app.PutInput{LocalPath: initial, RepositoryPath: "/file"}); err != nil {
		t.Fatal(err)
	}
	sessions, err := newSessionManager(application, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sessions.close(); err != nil {
			t.Errorf("close sessions: %v", err)
		}
	}()
	service := newService(application, sessions)
	result, err := service.Call(ctx, controlrpc.MethodBeginWrite, marshalTestJSON(t, map[string]any{"path": "/file", "resource_schemes": []string{"file"}, "follow_entry_id": true}))
	if err != nil {
		t.Fatal(err)
	}
	var begin controlrpc.BeginWriteResult
	if err := decodeTestResult(t, result, &begin); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resourcePathFromURI(t, begin.ResourceURI), []byte("stale bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	winner := filepath.Join(t.TempDir(), "winner")
	if err := os.WriteFile(winner, []byte("winner bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := application.Put(ctx, app.PutInput{LocalPath: winner, RepositoryPath: "/file"}); err != nil {
		t.Fatal(err)
	}
	err = sessions.commit(ctx, begin.SessionID)
	if !errors.Is(err, metadata.ErrEntryConflict) {
		t.Fatalf("stale stable-ID commit error=%v, want CAS conflict", err)
	}
	entry, err := application.Stat(ctx, "/file")
	if err != nil || entry.Revision != 2 || entry.ObjectID == nil {
		t.Fatalf("winner Entry state: entry=%+v err=%v", entry, err)
	}
	_, readPath, release, err := application.BeginReadResource(ctx, "/file")
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := os.ReadFile(readPath)
	if releaseErr := release(); releaseErr != nil {
		t.Errorf("release winner read: %v", releaseErr)
	}
	if readErr != nil || string(data) != "winner bytes" {
		t.Fatalf("CAS conflict changed winner content=%q err=%v", data, readErr)
	}
}

func TestUnlinkTombstoneRestartDeletesOnlyStableEntryID(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	if err := app.InitRepository(ctx, repoDir); err != nil {
		t.Fatal(err)
	}
	application, closeRepository := openTestApplication(t, repoDir)
	defer func() {
		if err := closeRepository(); err != nil {
			t.Errorf("close Repository: %v", err)
		}
	}()
	put := func(repoPath, content string) {
		t.Helper()
		localPath := filepath.Join(t.TempDir(), "source")
		if err := os.WriteFile(localPath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := application.Put(ctx, app.PutInput{LocalPath: localPath, RepositoryPath: repoPath}); err != nil {
			t.Fatal(err)
		}
	}
	put("/target", "original")
	original, err := application.Stat(ctx, "/target")
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := newSessionManager(application, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	begin, err := sessions.beginWrite(ctx, "/target", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.markUnlinked(begin.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := application.Move(ctx, app.MoveInput{SourcePath: "/target", DestinationPath: "/moved"}); err != nil {
		t.Fatal(err)
	}
	put("/target", "replacement")
	stopSessionManagerForCrash(sessions)

	restarted, err := newSessionManager(application, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := restarted.close(); err != nil {
			t.Errorf("close restarted session manager: %v", err)
		}
	}()
	if _, err := application.Stat(ctx, "/moved"); !errors.Is(err, metadata.ErrEntryNotFound) {
		t.Fatalf("restart did not finish unlink of original Entry %s: %v", original.ID, err)
	}
	replacement, err := application.Stat(ctx, "/target")
	if err != nil || replacement.ID == original.ID {
		t.Fatalf("restart removed/replaced the wrong Entry: replacement=%+v err=%v", replacement, err)
	}
	if records := restarted.listRecoveries(); len(records) != 0 {
		t.Fatalf("unlink tombstone was exposed as ordinary Recovery: %+v", records)
	}
	if _, err := os.Stat(resourcePathFromURI(t, begin.ResourceURI)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restart did not remove tombstoned staging: %v", err)
	}
}

func TestDaemonUsesDefaultEndpoint(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	if err := app.InitRepository(ctx, repoDir); err != nil {
		t.Fatal(err)
	}
	application, closeRepository := openTestApplication(t, repoDir)
	defer func() {
		if err := closeRepository(); err != nil {
			t.Errorf("close Repository: %v", err)
		}
	}()
	unlock, err := AcquireRepositoryLock(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unlock(); err != nil {
			t.Errorf("release Repository lock: %v", err)
		}
	}()

	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	endpoint, err := controlrpc.DefaultEndpoint(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- Serve(serveCtx, application, repoDir, ListenConfig{Endpoint: endpoint}) }()
	client, err := controlrpc.NewClient(controlrpc.ClientConfig{Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForDaemon(ctx, client); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Version string `json:"version"`
	}
	if err := client.Call(ctx, controlrpc.MethodPing, struct{}{}, &result); err != nil || result.Version == "" {
		t.Fatalf("default daemon endpoint ping: result=%+v err=%v", result, err)
	}
	cancel()
	if err := <-serveErr; err != nil {
		t.Fatalf("stop default-endpoint daemon: %v", err)
	}
	endpointURL, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	socketPath := endpointURL.Path
	if endpointURL.Opaque != "" {
		socketPath = endpointURL.Opaque
	}
	if _, err := os.Stat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("daemon shutdown did not remove default socket %q: %v", socketPath, err)
	}
}

func TestReadResourceLeaseProtectsObjectUntilRelease(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	if err := app.InitRepository(ctx, repoDir); err != nil {
		t.Fatal(err)
	}
	application, closeRepository := openTestApplication(t, repoDir)
	defer func() {
		if err := closeRepository(); err != nil {
			t.Errorf("close Repository: %v", err)
		}
	}()
	unlock, err := AcquireRepositoryLock(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unlock(); err != nil {
			t.Errorf("release Repository lock: %v", err)
		}
	}()

	oldSource := filepath.Join(t.TempDir(), "old")
	if err := os.WriteFile(oldSource, []byte("pinned immutable bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := application.Put(ctx, app.PutInput{LocalPath: oldSource, RepositoryPath: "/pinned"}); err != nil {
		t.Fatal(err)
	}
	oldEntry, err := application.Stat(ctx, "/pinned")
	if err != nil || oldEntry.ObjectID == nil {
		t.Fatalf("stat initial file: entry=%+v err=%v", oldEntry, err)
	}

	socketPath := filepath.Join(t.TempDir(), "socket", "nando.sock")
	endpoint := (&url.URL{Scheme: "unix", Path: socketPath}).String()
	serveCtx, cancel := context.WithCancel(ctx)
	serveErr := make(chan error, 1)
	serverStopped := false
	t.Cleanup(func() {
		if serverStopped {
			return
		}
		cancel()
		select {
		case err := <-serveErr:
			if err != nil {
				t.Errorf("stop daemon: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	go func() { serveErr <- Serve(serveCtx, application, repoDir, ListenConfig{Endpoint: endpoint}) }()
	client, err := controlrpc.NewClient(controlrpc.ClientConfig{Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForDaemon(ctx, client); err != nil {
		t.Fatal(err)
	}
	var read controlrpc.BeginReadResult
	if err := client.Call(ctx, controlrpc.MethodBeginRead, map[string]any{"path": "/pinned", "resource_schemes": []string{"file"}}, &read); err != nil {
		t.Fatal(err)
	}
	resourceURL, err := url.Parse(read.ResourceURI)
	if err != nil || resourceURL.Scheme != "file" {
		t.Fatalf("invalid read Resource URI %q: %v", read.ResourceURI, err)
	}
	resource, err := os.ReadFile(resourceURL.Path)
	if err != nil || string(resource) != "pinned immutable bytes" {
		t.Fatalf("read Resource content=%q err=%v", resource, err)
	}

	if err := application.Remove(ctx, "/pinned"); err != nil {
		t.Fatal(err)
	}
	type gcResult struct {
		count int
		err   error
	}
	gcDone := make(chan gcResult, 1)
	gcCtx, cancelGC := context.WithTimeout(ctx, 3*time.Second)
	defer cancelGC()
	go func() {
		count, err := application.GC(gcCtx)
		gcDone <- gcResult{count: count, err: err}
	}()
	select {
	case result := <-gcDone:
		t.Fatalf("GC should wait for active read Resource lease: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}
	oldObjectPath := filepath.Join(repoDir, "objects", oldEntry.ObjectID.String())
	if _, err := os.Stat(oldObjectPath); err != nil {
		t.Fatalf("GC removed object while read Resource lease was active: %v", err)
	}
	if err := client.Call(ctx, controlrpc.MethodRelease, map[string]string{"session_id": read.SessionID}, nil); err != nil {
		t.Fatal(err)
	}
	result := <-gcDone
	if result.err != nil || result.count != 1 {
		t.Fatalf("GC after Resource release: %+v", result)
	}
	if _, err := os.Stat(oldObjectPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old object remains after read Resource release and GC: %v", err)
	}
	cancel()
	if err := <-serveErr; err != nil {
		t.Fatalf("stop daemon: %v", err)
	}
	serverStopped = true
}

func TestExpiredReadLeaseStopsRenewingAndRecoveryStagingSurvivesGC(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	if err := app.InitRepository(ctx, repoDir); err != nil {
		t.Fatal(err)
	}
	application, closeRepository := openTestApplication(t, repoDir)
	defer func() {
		if err := closeRepository(); err != nil {
			t.Errorf("close Repository: %v", err)
		}
	}()
	source := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(source, []byte("expiring read object"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := application.Put(ctx, app.PutInput{LocalPath: source, RepositoryPath: "/file"}); err != nil {
		t.Fatal(err)
	}
	entry, err := application.Stat(ctx, "/file")
	if err != nil || entry.ObjectID == nil {
		t.Fatalf("stat committed file: entry=%+v err=%v", entry, err)
	}
	sessions, err := newSessionManagerWithTTL(application, repoDir, 30*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sessions.close(); err != nil {
			t.Errorf("close sessions: %v", err)
		}
	}()
	service := newService(application, sessions)
	result, err := service.Call(ctx, controlrpc.MethodBeginRead, marshalTestJSON(t, map[string]any{"path": "/file", "resource_schemes": []string{"file"}}))
	if err != nil {
		t.Fatal(err)
	}
	var read controlrpc.BeginReadResult
	if err := decodeTestResult(t, result, &read); err != nil {
		t.Fatal(err)
	}
	if err := application.Remove(ctx, "/file"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	sessions.expireDue(time.Now())
	if deleted, err := application.GC(ctx); err != nil || deleted != 1 {
		t.Fatalf("GC after read session expiry: deleted=%d err=%v", deleted, err)
	}
	objectPath := filepath.Join(repoDir, "objects", entry.ObjectID.String())
	if _, err := os.Stat(objectPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired read session kept renewing the object lease: %v", err)
	}

	result, err = service.Call(ctx, controlrpc.MethodBeginWrite, marshalTestJSON(t, map[string]any{"path": "/recovery", "resource_schemes": []string{"file"}}))
	if err != nil {
		t.Fatal(err)
	}
	var write controlrpc.BeginWriteResult
	if err := decodeTestResult(t, result, &write); err != nil {
		t.Fatal(err)
	}
	stagePath := resourcePathFromURI(t, write.ResourceURI)
	if err := os.WriteFile(stagePath, []byte("must survive repository GC"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sessions.release(write.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := application.GC(ctx); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(stagePath); err != nil || string(data) != "must survive repository GC" {
		t.Fatalf("Repository GC removed recovery staging: data=%q err=%v", data, err)
	}
}

func waitForDaemon(ctx context.Context, client *controlrpc.Client) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var result struct {
			Version string `json:"version"`
		}
		if err := client.Call(ctx, controlrpc.MethodPing, struct{}{}, &result); err == nil && result.Version != "" {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("daemon did not become ready")
}

func openTestApplication(t *testing.T, repoDir string) (*app.App, func() error) {
	t.Helper()
	metaStore, err := metadatasqlite.NewStore(filepath.Join(repoDir, "metadata.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	hasher := content.NewHasher()
	objects, err := local.NewStore(filepath.Join(repoDir, "objects"), filepath.Join(repoDir, "temp"), hasher)
	if err != nil {
		t.Fatal(errors.Join(err, metaStore.Close()))
	}
	coordinator, err := coordinatorsqlite.Open(filepath.Join(repoDir, "coordination.db"))
	if err != nil {
		t.Fatal(errors.Join(err, metaStore.Close()))
	}
	settings, err := app.LoadRepositorySettings(repoDir)
	if err != nil {
		t.Fatal(errors.Join(err, coordinator.Close(), metaStore.Close()))
	}
	repo := repository.NewRepositoryWithRemoteObjectStore(metadata.NewValidatingStore(metaStore), objects, nil, hasher, coordinator, settings)
	return app.NewApp(repo), func() error { return errors.Join(coordinator.Close(), metaStore.Close()) }
}
