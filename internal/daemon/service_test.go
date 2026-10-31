package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/kamichidu/go-nando/internal/app"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/repository"
	"github.com/kamichidu/go-nando/pkg/controlrpc"
)

type fakeRepositoryUseCases struct {
	mu      sync.Mutex
	entry   *metadata.Entry
	content []byte
}

func (f *fakeRepositoryUseCases) Stat(_ context.Context, path string) (metadata.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.entry == nil || path != "/file" {
		return metadata.Entry{}, metadata.ErrEntryNotFound
	}
	return *f.entry, nil
}

func (f *fakeRepositoryUseCases) PutExpected(_ context.Context, input app.PutInput, expected repository.PutExpectation) error {
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
