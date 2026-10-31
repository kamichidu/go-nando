package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/uuid"
	"github.com/kamichidu/go-nando/internal/app"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/paths"
	"github.com/kamichidu/go-nando/internal/repository"
	"github.com/kamichidu/go-nando/pkg/controlrpc"
)

var (
	errSessionNotFound = errors.New("resource session not found")
	errSessionBusy     = errors.New("resource session operation in progress")
	errSessionClosed   = errors.New("resource session is closed")
)

type writeSession struct {
	id             string
	path           string
	repositoryPath string
	expectation    repository.PutExpectation
	inFlight       bool
	committed      bool
	aborted        bool
}

type readSession struct {
	release  func() error
	inFlight bool
}

type sessionManager struct {
	app       repositoryUseCases
	directory string
	mu        sync.Mutex
	sessions  map[string]*writeSession
	reads     map[string]*readSession
}

func newSessionManager(application repositoryUseCases, repoDir string) (*sessionManager, error) {
	directory := filepath.Join(repoDir, "temp", "rpc-sessions")
	if err := os.RemoveAll(directory); err != nil {
		return nil, fmt.Errorf("clean abandoned daemon Resource sessions: %w", err)
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("create daemon Resource session directory: %w", err)
	}
	return &sessionManager{app: application, directory: directory, sessions: make(map[string]*writeSession), reads: make(map[string]*readSession)}, nil
}

func (m *sessionManager) beginRead(ctx context.Context, repositoryPath string) (controlrpc.BeginReadResult, error) {
	entry, resourcePath, release, err := m.app.BeginReadResource(ctx, repositoryPath)
	if err != nil {
		return controlrpc.BeginReadResult{}, err
	}
	id := uuid.NewString()
	resourceURI := (&url.URL{Scheme: "file", Path: resourcePath}).String()
	m.mu.Lock()
	m.reads[id] = &readSession{release: release}
	m.mu.Unlock()
	return controlrpc.BeginReadResult{SessionID: id, ResourceURI: resourceURI, Entry: protocolEntry(entry)}, nil
}

func (m *sessionManager) beginWrite(ctx context.Context, repositoryPath string) (controlrpc.BeginWriteResult, error) {
	normalized, err := paths.NormalizeLogicalPath(repositoryPath)
	if err != nil || normalized == "/" {
		return controlrpc.BeginWriteResult{}, fmt.Errorf("invalid write path %q", repositoryPath)
	}
	expectation := repository.PutExpectation{MustBeAbsent: true}
	entry, err := m.app.Stat(ctx, normalized)
	if err == nil {
		if entry.Kind != metadata.EntryKindFile {
			return controlrpc.BeginWriteResult{}, fmt.Errorf("write target is not a file: %w", metadata.ErrEntryConflict)
		}
		expectation = repository.PutExpectation{EntryID: entry.ID, Revision: entry.Revision}
	} else if !errors.Is(err, metadata.ErrEntryNotFound) {
		return controlrpc.BeginWriteResult{}, fmt.Errorf("inspect write target: %w", err)
	}

	file, err := os.CreateTemp(m.directory, "write-")
	if err != nil {
		return controlrpc.BeginWriteResult{}, fmt.Errorf("create mutable Resource: %w", err)
	}
	resourcePath := file.Name()
	if err := file.Close(); err != nil {
		return controlrpc.BeginWriteResult{}, errors.Join(fmt.Errorf("close new mutable Resource: %w", err), os.Remove(resourcePath))
	}
	resourceURL := (&url.URL{Scheme: "file", Path: resourcePath}).String()
	id := uuid.NewString()
	session := &writeSession{id: id, path: resourcePath, repositoryPath: normalized, expectation: expectation}
	m.mu.Lock()
	m.sessions[id] = session
	m.mu.Unlock()
	return controlrpc.BeginWriteResult{SessionID: id, ResourceURI: resourceURL}, nil
}

func (m *sessionManager) commit(ctx context.Context, id string) error {
	m.mu.Lock()
	session, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return errSessionNotFound
	}
	if session.inFlight {
		m.mu.Unlock()
		return errSessionBusy
	}
	if session.aborted {
		m.mu.Unlock()
		return errSessionClosed
	}
	if session.committed {
		m.mu.Unlock()
		return nil
	}
	session.inFlight = true
	m.mu.Unlock()
	putErr := m.app.PutExpected(ctx, app.PutInput{LocalPath: session.path, RepositoryPath: session.repositoryPath}, session.expectation)
	m.mu.Lock()
	session.inFlight = false
	if putErr == nil || errors.Is(putErr, repository.ErrPutCommitted) {
		session.committed = true
	}
	m.mu.Unlock()
	return putErr
}

func (m *sessionManager) abort(id string) error {
	m.mu.Lock()
	session, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return errSessionNotFound
	}
	if session.inFlight {
		m.mu.Unlock()
		return errSessionBusy
	}
	if session.committed {
		m.mu.Unlock()
		return fmt.Errorf("cannot abort a committed Resource session: %w", errSessionClosed)
	}
	session.aborted = true
	session.inFlight = true
	path := session.path
	m.mu.Unlock()
	err := os.Remove(path)
	m.mu.Lock()
	session.inFlight = false
	m.mu.Unlock()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove aborted Resource: %w", err)
	}
	return nil
}

func (m *sessionManager) release(id string) error {
	m.mu.Lock()
	if session, ok := m.reads[id]; ok {
		if session.inFlight {
			m.mu.Unlock()
			return errSessionBusy
		}
		session.inFlight = true
		m.mu.Unlock()
		if err := session.release(); err != nil {
			m.mu.Lock()
			session.inFlight = false
			m.mu.Unlock()
			return fmt.Errorf("release read Resource lease: %w", err)
		}
		m.mu.Lock()
		delete(m.reads, id)
		m.mu.Unlock()
		return nil
	}
	session, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	if session.inFlight {
		m.mu.Unlock()
		return errSessionBusy
	}
	session.inFlight = true
	path := session.path
	m.mu.Unlock()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		m.mu.Lock()
		session.inFlight = false
		m.mu.Unlock()
		return fmt.Errorf("remove released Resource: %w", err)
	}
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
	return nil
}

func (m *sessionManager) close() error {
	m.mu.Lock()
	pathsToRemove := make([]string, 0, len(m.sessions))
	for _, session := range m.sessions {
		pathsToRemove = append(pathsToRemove, session.path)
	}
	readReleases := make([]func() error, 0, len(m.reads))
	for _, session := range m.reads {
		readReleases = append(readReleases, session.release)
	}
	m.sessions = make(map[string]*writeSession)
	m.reads = make(map[string]*readSession)
	m.mu.Unlock()
	var failures []error
	for _, release := range readReleases {
		if err := release(); err != nil {
			failures = append(failures, err)
		}
	}
	for _, path := range pathsToRemove {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, err)
		}
	}
	if err := os.Remove(m.directory); err != nil && !errors.Is(err, os.ErrNotExist) {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}
