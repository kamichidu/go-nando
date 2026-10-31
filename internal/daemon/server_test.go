package daemon

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kamichidu/go-nando/internal/app"
	"github.com/kamichidu/go-nando/internal/content"
	coordinatorsqlite "github.com/kamichidu/go-nando/internal/coordinator/sqlite"
	"github.com/kamichidu/go-nando/internal/metadata"
	metadatasqlite "github.com/kamichidu/go-nando/internal/metadata/sqlite"
	"github.com/kamichidu/go-nando/internal/objectstore/local"
	"github.com/kamichidu/go-nando/internal/repository"
	"github.com/kamichidu/go-nando/pkg/controlrpc"
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

	socketPath := filepath.Join(t.TempDir(), "nando.sock")
	endpoint := (&url.URL{Scheme: "unix", Path: socketPath}).String()
	serveCtx, cancel := context.WithCancel(ctx)
	serveErr := make(chan error, 1)
	go func() { serveErr <- Serve(serveCtx, application, repoDir, controlrpc.ListenConfig{Endpoint: endpoint}) }()
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
	if err := os.WriteFile(resourceURL.Path, []byte("JSON-RPC resource payload"), 0600); err != nil {
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
	if err := os.WriteFile(staleURL.Path, []byte("stale session"), 0600); err != nil {
		t.Fatal(err)
	}
	winner := filepath.Join(t.TempDir(), "winner")
	if err := os.WriteFile(winner, []byte("concurrent winner"), 0600); err != nil {
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
	go func() { serveErr <- Serve(serveCtx, application, repoDir, controlrpc.ListenConfig{Endpoint: endpoint}) }()
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
	if err := os.WriteFile(oldSource, []byte("pinned immutable bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := application.Put(ctx, app.PutInput{LocalPath: oldSource, RepositoryPath: "/pinned"}); err != nil {
		t.Fatal(err)
	}
	oldEntry, err := application.Stat(ctx, "/pinned")
	if err != nil || oldEntry.ObjectID == nil {
		t.Fatalf("stat initial file: entry=%+v err=%v", oldEntry, err)
	}

	socketPath := filepath.Join(t.TempDir(), "nando.sock")
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
	go func() { serveErr <- Serve(serveCtx, application, repoDir, controlrpc.ListenConfig{Endpoint: endpoint}) }()
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
	if err := os.WriteFile(source, []byte("expiring read object"), 0600); err != nil {
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
	if err := os.WriteFile(stagePath, []byte("must survive repository GC"), 0600); err != nil {
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
	repo := repository.NewRepositoryWithReplica(metadata.NewValidatingStore(metaStore), objects, nil, hasher, coordinator, settings)
	return app.NewApp(repo), func() error { return errors.Join(coordinator.Close(), metaStore.Close()) }
}
