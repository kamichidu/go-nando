package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kamichidu/go-nando/controlrpc"
	"github.com/kamichidu/go-nando/internal/app"
	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/paths"
	"github.com/kamichidu/go-nando/internal/repository"
)

const defaultResourceSessionTTL = controlrpc.ResourceSessionTTL

var (
	errSessionNotFound = errors.New("resource session not found")
	errSessionBusy     = errors.New("resource session operation in progress")
	errSessionClosed   = errors.New("resource session is closed")
	errSessionExpired  = errors.New("resource session expired")
)

type recoveryRecord struct {
	Version          int                       `json:"version"`
	Committed        bool                      `json:"committed"`
	Discarding       bool                      `json:"discarding,omitempty"`
	FollowEntryID    bool                      `json:"follow_entry_id,omitempty"`
	LogicalMode      *uint32                   `json:"logical_mode,omitempty"`
	UnlinkEntryID    metadata.EntryID          `json:"unlink_entry_id,omitempty"`
	SessionID        string                    `json:"session_id"`
	StagingPath      string                    `json:"staging_path"`
	RepositoryPath   string                    `json:"repository_path"`
	Expectation      repository.PutExpectation `json:"expectation"`
	ObservedEntry    metadata.Entry            `json:"observed_entry,omitempty"`
	ObservedObjectID string                    `json:"observed_object_id,omitempty"`
	FailureReason    string                    `json:"failure_reason"`
	CreatedAt        time.Time                 `json:"created_at"`
	UpdatedAt        time.Time                 `json:"updated_at"`
	inFlight         bool                      `json:"-"`
}

type writeSession struct {
	recoveryRecord
	expiresAt time.Time
	committed bool
	expired   bool
}

type readSession struct {
	release   func() error
	expiresAt time.Time
	inFlight  bool
	expired   bool
}

// sessionFiles isolates the daemon's durable Resource manifest and staging
// operations so failures at each filesystem boundary have explicit outcomes.
type sessionFiles interface {
	SyncFile(string) error
	WriteAtomic(directory, path string, data []byte) error
	Remove(string) error
	SyncDirectory(string) error
	ReadDir(string) ([]os.DirEntry, error)
	ReadFile(string) ([]byte, error)
}

type osSessionFiles struct{}

type sessionExpiryTicker interface {
	Chan() <-chan time.Time
	Stop()
}

type realSessionExpiryTicker struct{ ticker *time.Ticker }

func (t realSessionExpiryTicker) Chan() <-chan time.Time { return t.ticker.C }
func (t realSessionExpiryTicker) Stop()                  { t.ticker.Stop() }

var newSessionExpiryTicker = func(interval time.Duration) sessionExpiryTicker {
	return realSessionExpiryTicker{ticker: time.NewTicker(interval)}
}

func (osSessionFiles) SyncFile(path string) error { return syncFile(path) }

func (osSessionFiles) WriteAtomic(directory, path string, data []byte) error {
	temp, err := os.CreateTemp(directory, ".recovery-record-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	n, writeErr := temp.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = fmt.Errorf("short recovery record write: %d of %d bytes", n, len(data))
	}
	syncErr := temp.Sync()
	closeErr := temp.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return errors.Join(err, removeIfExists(tempPath))
	}
	if err := os.Rename(tempPath, path); err != nil {
		return errors.Join(err, removeIfExists(tempPath))
	}
	return syncDirectory(directory)
}

func (osSessionFiles) Remove(path string) error                   { return removeIfExists(path) }
func (osSessionFiles) SyncDirectory(path string) error            { return syncDirectory(path) }
func (osSessionFiles) ReadDir(path string) ([]os.DirEntry, error) { return os.ReadDir(path) }
func (osSessionFiles) ReadFile(path string) ([]byte, error)       { return os.ReadFile(path) }

type sessionManager struct {
	app        repositoryUseCases
	root       string
	directory  string
	ttl        time.Duration
	mu         sync.Mutex
	sessions   map[string]*writeSession
	reads      map[string]*readSession
	recoveries map[string]*recoveryRecord
	files      sessionFiles
	done       chan struct{}
	stopOnce   sync.Once
	wg         sync.WaitGroup
}

func newSessionManager(application repositoryUseCases, repoDir string) (*sessionManager, error) {
	return newSessionManagerWithTTL(application, repoDir, defaultResourceSessionTTL)
}

func newSessionManagerWithTTL(application repositoryUseCases, repoDir string, ttl time.Duration) (*sessionManager, error) {
	if ttl <= 0 {
		return nil, errors.New("resource session TTL must be positive")
	}
	root, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, fmt.Errorf("resolve Repository root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve Repository root symlinks: %w", err)
	}
	directory := filepath.Join(root, "temp", "rpc-sessions")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create daemon Resource session directory: %w", err)
	}
	if err := validateSessionDirectories(root, directory); err != nil {
		return nil, err
	}
	if err := syncDirectory(filepath.Dir(directory)); err != nil {
		return nil, fmt.Errorf("sync daemon Resource session parent directory: %w", err)
	}
	manager := &sessionManager{
		app: application, root: root, directory: directory, ttl: ttl,
		sessions: make(map[string]*writeSession), reads: make(map[string]*readSession),
		recoveries: make(map[string]*recoveryRecord), files: osSessionFiles{}, done: make(chan struct{}),
	}
	if err := manager.loadRecoveries(); err != nil {
		return nil, err
	}
	manager.wg.Add(1)
	go manager.expireLoop()
	return manager, nil
}

func (m *sessionManager) beginRead(ctx context.Context, repositoryPath string, entryIDs ...metadata.EntryID) (controlrpc.BeginReadResult, error) {
	var entry metadata.Entry
	var resourcePath string
	var release func() error
	var err error
	if len(entryIDs) > 0 && entryIDs[0] != "" {
		entry, resourcePath, release, err = m.app.BeginReadEntryResource(ctx, entryIDs[0])
	} else {
		entry, resourcePath, release, err = m.app.BeginReadResource(ctx, repositoryPath)
	}
	if err != nil {
		return controlrpc.BeginReadResult{}, err
	}
	id := uuid.NewString()
	resourceURI := (&url.URL{Scheme: "file", Path: resourcePath}).String()
	m.mu.Lock()
	select {
	case <-m.done:
		m.mu.Unlock()
		return controlrpc.BeginReadResult{}, errors.Join(errors.New("resource session manager is closed"), release())
	default:
	}
	m.reads[id] = &readSession{release: release, expiresAt: time.Now().Add(m.ttl)}
	m.mu.Unlock()
	return controlrpc.BeginReadResult{SessionID: id, ResourceURI: resourceURI, Entry: protocolEntry(entry)}, nil
}

func (m *sessionManager) beginWrite(ctx context.Context, repositoryPath string, followEntryID ...bool) (controlrpc.BeginWriteResult, error) {
	followID := len(followEntryID) > 0 && followEntryID[0]
	return m.beginWriteWithMode(ctx, repositoryPath, followID, nil)
}

func (m *sessionManager) beginWriteWithMode(ctx context.Context, repositoryPath string, followID bool, logicalMode *uint32) (controlrpc.BeginWriteResult, error) {
	normalized, err := paths.NormalizeLogicalPath(repositoryPath)
	if err != nil || normalized == "/" {
		return controlrpc.BeginWriteResult{}, fmt.Errorf("invalid write path %q", repositoryPath)
	}
	expectation := repository.PutExpectation{MustBeAbsent: true}
	var observedEntry metadata.Entry
	entry, err := m.app.Stat(ctx, normalized)
	if err == nil {
		if entry.Kind != metadata.EntryKindFile {
			return controlrpc.BeginWriteResult{}, fmt.Errorf("write target is not a file: %w", metadata.ErrEntryConflict)
		}
		expectation = repository.PutExpectation{EntryID: entry.ID, Revision: entry.Revision}
		observedEntry = entry
		if logicalMode == nil {
			mode := entry.Mode & 0o7777
			logicalMode = &mode
		}
	} else if !errors.Is(err, metadata.ErrEntryNotFound) {
		return controlrpc.BeginWriteResult{}, fmt.Errorf("inspect write target: %w", err)
	} else if logicalMode == nil {
		// Resource API writes have the POSIX regular-file default when the
		// caller omits a mode. FUSE supplies its already-umask-adjusted mode.
		mode := uint32(0o644)
		logicalMode = &mode
	}
	return m.createWriteSession(normalized, expectation, observedEntry, followID, logicalMode)
}

func (m *sessionManager) beginWriteEntry(ctx context.Context, entryID metadata.EntryID) (controlrpc.BeginWriteResult, error) {
	return m.beginWriteEntryMode(ctx, entryID, nil)
}

func (m *sessionManager) beginWriteEntryMode(ctx context.Context, entryID metadata.EntryID, mode *uint32) (controlrpc.BeginWriteResult, error) {
	entry, repositoryPath, err := m.app.ResolveEntry(ctx, entryID)
	if err != nil {
		return controlrpc.BeginWriteResult{}, err
	}
	if entry.Kind != metadata.EntryKindFile {
		return controlrpc.BeginWriteResult{}, fmt.Errorf("write target is not a file: %w", metadata.ErrEntryConflict)
	}
	if mode == nil {
		logicalMode := entry.Mode & 0o7777
		mode = &logicalMode
	}
	return m.createWriteSession(repositoryPath, repository.PutExpectation{EntryID: entry.ID, Revision: entry.Revision}, entry, true, mode)
}

func (m *sessionManager) beginWriteMode(ctx context.Context, repositoryPath string, follow bool, mode *uint32) (controlrpc.BeginWriteResult, error) {
	return m.beginWriteWithMode(ctx, repositoryPath, follow, mode)
}

func (m *sessionManager) createWriteSession(normalized string, expectation repository.PutExpectation, observedEntry metadata.Entry, followID bool, logicalMode *uint32) (controlrpc.BeginWriteResult, error) {
	id := uuid.NewString()
	resourcePath := filepath.Join(m.directory, id+".stage")
	file, err := os.OpenFile(resourcePath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return controlrpc.BeginWriteResult{}, fmt.Errorf("create mutable Resource: %w", err)
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return controlrpc.BeginWriteResult{}, errors.Join(fmt.Errorf("durably create mutable Resource: %w", err), os.Remove(resourcePath))
	}
	now := time.Now().UTC()
	observedObjectID := ""
	if observedEntry.ObjectID != nil {
		observedObjectID = observedEntry.ObjectID.String()
	}
	if logicalMode == nil {
		mode := uint32(0o644)
		logicalMode = &mode
	}
	session := &writeSession{recoveryRecord: recoveryRecord{
		Version: 2, SessionID: id, StagingPath: resourcePath, RepositoryPath: normalized, Expectation: expectation, FollowEntryID: followID && expectation.EntryID != "", ObservedEntry: observedEntry, LogicalMode: logicalMode,
		ObservedObjectID: observedObjectID,
		CreatedAt:        now, UpdatedAt: now,
	}, expiresAt: time.Now().Add(m.ttl)}
	if err := m.persistRecord(&session.recoveryRecord); err != nil {
		return controlrpc.BeginWriteResult{}, errors.Join(fmt.Errorf("persist Resource session recovery record: %w", err), os.Remove(resourcePath))
	}
	m.mu.Lock()
	select {
	case <-m.done:
		m.mu.Unlock()
		return controlrpc.BeginWriteResult{}, errors.Join(errors.New("resource session manager is closed"), m.preserve(&session.recoveryRecord, "daemon stopped during Resource creation"))
	default:
	}
	m.sessions[id] = session
	m.mu.Unlock()
	resourceURL := (&url.URL{Scheme: "file", Path: resourcePath}).String()
	return controlrpc.BeginWriteResult{
		SessionID: id, ResourceURI: resourceURL,
		ExpectedEntryID: string(expectation.EntryID), ExpectedRevision: expectation.Revision,
		ExpectedMtime: int64(observedEntry.Mtime),
		MustBeAbsent:  expectation.EntryID == "",
	}, nil
}

func (m *sessionManager) renew(id string) (time.Time, error) {
	m.mu.Lock()
	session, ok := m.sessions[id]
	if ok {
		if session.expired {
			m.mu.Unlock()
			return time.Time{}, errSessionExpired
		}
		if session.inFlight {
			m.mu.Unlock()
			return time.Time{}, errSessionBusy
		}
		if session.committed {
			m.mu.Unlock()
			return time.Time{}, errSessionClosed
		}
		if !time.Now().Before(session.expiresAt) {
			session.expired = true
			session.inFlight = true
			m.mu.Unlock()
			return time.Time{}, errors.Join(errSessionExpired, m.finishExpiredWrite(id, session))
		}
		session.expiresAt = time.Now().Add(m.ttl)
		expiresAt := session.expiresAt
		m.mu.Unlock()
		return expiresAt, nil
	}
	read, ok := m.reads[id]
	if !ok {
		m.mu.Unlock()
		return time.Time{}, errSessionNotFound
	}
	if read.expired {
		m.mu.Unlock()
		return time.Time{}, errSessionExpired
	}
	if read.inFlight {
		m.mu.Unlock()
		return time.Time{}, errSessionBusy
	}
	if !time.Now().Before(read.expiresAt) {
		read.expired = true
		read.inFlight = true
		m.mu.Unlock()
		return time.Time{}, errors.Join(errSessionExpired, m.finishExpiredRead(id, read))
	}
	read.expiresAt = time.Now().Add(m.ttl)
	expiresAt := read.expiresAt
	m.mu.Unlock()
	return expiresAt, nil
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
	if session.expired {
		m.mu.Unlock()
		return errSessionExpired
	}
	if session.committed {
		m.mu.Unlock()
		return nil
	}
	if session.Discarding {
		m.mu.Unlock()
		return errSessionClosed
	}
	if !time.Now().Before(session.expiresAt) {
		session.expired = true
		session.inFlight = true
		m.mu.Unlock()
		return errors.Join(errSessionExpired, m.finishExpiredWrite(id, session))
	}
	session.inFlight = true
	m.mu.Unlock()

	commitErr := m.files.SyncFile(session.StagingPath)
	if commitErr == nil {
		input := app.PutInput{LocalPath: session.StagingPath, RepositoryPath: session.RepositoryPath}
		if session.FollowEntryID {
			commitErr = m.app.PutExpectedEntryIDWithMode(ctx, input, session.ObservedEntry, session.LogicalMode)
		} else {
			commitErr = m.app.PutExpectedWithMode(ctx, input, session.Expectation, session.LogicalMode)
		}
	}
	if commitErr != nil {
		preserveErr := m.preserve(&session.recoveryRecord, commitErr.Error())
		m.mu.Lock()
		session.inFlight = false
		if preserveErr == nil {
			delete(m.sessions, id)
			m.recoveries[id] = &session.recoveryRecord
		} else {
			session.expired = true
		}
		m.mu.Unlock()
		return errors.Join(commitErr, preserveErr)
	}
	if err := m.markCommitted(&session.recoveryRecord); err != nil {
		preserveErr := m.preserve(&session.recoveryRecord, "repository commit succeeded but durable completion record failed: "+err.Error())
		m.mu.Lock()
		session.inFlight = false
		if preserveErr == nil {
			delete(m.sessions, id)
			m.recoveries[id] = &session.recoveryRecord
		} else {
			session.expired = true
		}
		m.mu.Unlock()
		return errors.Join(err, preserveErr)
	}
	m.mu.Lock()
	session.inFlight = false
	session.committed = true
	m.mu.Unlock()
	if err := m.cleanupCommittedRecord(&session.recoveryRecord); err != nil {
		log.Printf("clean committed Resource session %s: %v", id, err)
	}
	return nil
}

func (m *sessionManager) setMode(id string, mode uint32) error {
	m.mu.Lock()
	session := m.sessions[id]
	if session == nil || session.inFlight || session.committed || session.expired {
		m.mu.Unlock()
		return errSessionNotFound
	}
	session.inFlight = true
	updated := session.recoveryRecord
	value := mode & 0o7777
	updated.LogicalMode = &value
	updated.UpdatedAt = time.Now().UTC()
	m.mu.Unlock()
	err := m.persistRecord(&updated)
	m.mu.Lock()
	if err == nil {
		session.LogicalMode = &value
		session.UpdatedAt = updated.UpdatedAt
	}
	session.inFlight = false
	m.mu.Unlock()
	return err
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
	if session.expired {
		m.mu.Unlock()
		return errSessionExpired
	}
	if session.committed {
		m.mu.Unlock()
		return fmt.Errorf("cannot abort a committed Resource session: %w", errSessionClosed)
	}
	if !time.Now().Before(session.expiresAt) {
		session.expired = true
		session.inFlight = true
		m.mu.Unlock()
		return errors.Join(errSessionExpired, m.finishExpiredWrite(id, session))
	}
	session.inFlight = true
	m.mu.Unlock()
	if err := m.markDiscarding(&session.recoveryRecord, "explicit resource abort"); err != nil {
		m.mu.Lock()
		session.inFlight = false
		m.mu.Unlock()
		return fmt.Errorf("persist Abort intent: %w", err)
	}
	if err := m.finishDiscardedRecord(&session.recoveryRecord); err != nil {
		m.mu.Lock()
		session.inFlight = false
		m.mu.Unlock()
		return err
	}
	m.mu.Lock()
	delete(m.sessions, id)
	session.inFlight = false
	m.mu.Unlock()
	return nil
}

// release closes a client handle. Uncommitted bytes are preserved for explicit
// recovery instead of being silently discarded.
func (m *sessionManager) release(id string) error {
	m.mu.Lock()
	if session, ok := m.sessions[id]; ok {
		if session.inFlight {
			m.mu.Unlock()
			return errSessionBusy
		}
		if session.Discarding {
			session.inFlight = true
			m.mu.Unlock()
			if err := m.finishDiscardedRecord(&session.recoveryRecord); err != nil {
				m.mu.Lock()
				session.inFlight = false
				m.mu.Unlock()
				return err
			}
			m.mu.Lock()
			delete(m.sessions, id)
			m.mu.Unlock()
			return nil
		}
		if session.expired {
			m.mu.Unlock()
			return errSessionExpired
		}
		if session.committed {
			delete(m.sessions, id)
			m.mu.Unlock()
			return nil
		}
		session.inFlight = true
		m.mu.Unlock()
		err := m.preserve(&session.recoveryRecord, "session released before successful commit")
		m.mu.Lock()
		session.inFlight = false
		if err == nil {
			delete(m.sessions, id)
			m.recoveries[id] = &session.recoveryRecord
		} else {
			session.expired = true
		}
		m.mu.Unlock()
		return err
	}
	if record, ok := m.recoveries[id]; ok && record.Discarding {
		if record.inFlight {
			m.mu.Unlock()
			return errSessionBusy
		}
		record.inFlight = true
		m.mu.Unlock()
		if err := m.finishDiscardedRecord(record); err != nil {
			m.mu.Lock()
			record.inFlight = false
			m.mu.Unlock()
			return err
		}
		m.mu.Lock()
		delete(m.recoveries, id)
		m.mu.Unlock()
		return nil
	}
	if session, ok := m.reads[id]; ok {
		if session.inFlight {
			m.mu.Unlock()
			return errSessionBusy
		}
		session.inFlight = true
		m.mu.Unlock()
		err := session.release()
		m.mu.Lock()
		delete(m.reads, id)
		m.mu.Unlock()
		if err != nil {
			return fmt.Errorf("release read Resource lease: %w", err)
		}
		return nil
	}
	m.mu.Unlock()
	return nil
}

// markUnlinked durably records FUSE unlink's explicit discard intent while
// leaving the staging path available to already-open file descriptors.
func (m *sessionManager) markUnlinked(id string) error {
	m.mu.Lock()
	session, active := m.sessions[id]
	var record *recoveryRecord
	if active {
		record = &session.recoveryRecord
	} else {
		record = m.recoveries[id]
	}
	if record == nil {
		m.mu.Unlock()
		return errSessionNotFound
	}
	if record.Committed {
		m.mu.Unlock()
		return nil
	}
	if record.inFlight {
		m.mu.Unlock()
		return errSessionBusy
	}
	if record.Discarding {
		m.mu.Unlock()
		return nil
	}
	record.inFlight = true
	updated := *record
	updated.Discarding = true
	updated.UnlinkEntryID = record.Expectation.EntryID
	updated.FailureReason = "FUSE Entry unlinked; discard after final handle close"
	updated.UpdatedAt = time.Now().UTC()
	m.mu.Unlock()
	err := m.persistRecord(&updated)
	m.mu.Lock()
	if err == nil {
		record.Discarding = true
		record.UnlinkEntryID = updated.UnlinkEntryID
		record.FailureReason = updated.FailureReason
		record.UpdatedAt = updated.UpdatedAt
	}
	record.inFlight = false
	m.mu.Unlock()
	return err
}

func (m *sessionManager) revoke(id string) error {
	return m.release(id)
}

func (m *sessionManager) listRecoveries() []controlrpc.RecoveryRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]controlrpc.RecoveryRecord, 0, len(m.recoveries))
	seen := make(map[string]struct{}, len(m.recoveries))
	for _, record := range m.recoveries {
		if record.Discarding {
			continue
		}
		result = append(result, protocolRecovery(*record))
		seen[record.SessionID] = struct{}{}
	}
	for id, session := range m.sessions {
		if !session.expired || session.inFlight || session.Discarding {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		result = append(result, protocolRecovery(session.recoveryRecord))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result
}

func (m *sessionManager) retryRecovery(ctx context.Context, id string) error {
	m.mu.Lock()
	record, ok := m.recoveries[id]
	session := m.sessions[id]
	if ok && session != nil {
		m.mu.Unlock()
		return errors.New("resource session exists in both active and recovery maps")
	}
	if !ok && session != nil && session.expired {
		record = &session.recoveryRecord
		ok = true
	}
	if !ok {
		m.mu.Unlock()
		return errSessionNotFound
	}
	if record.inFlight {
		m.mu.Unlock()
		return errSessionBusy
	}
	if record.Discarding {
		m.mu.Unlock()
		return errSessionClosed
	}
	record.inFlight = true
	m.mu.Unlock()
	if err := m.files.SyncFile(record.StagingPath); err != nil {
		m.mu.Lock()
		record.inFlight = false
		m.mu.Unlock()
		return fmt.Errorf("sync recovery staging before retry: %w", err)
	}
	updated := *record
	updated.UpdatedAt = time.Now().UTC()
	if err := m.persistRecord(&updated); err != nil {
		m.mu.Lock()
		record.inFlight = false
		m.mu.Unlock()
		return fmt.Errorf("persist recovery metadata before retry: %w", err)
	}
	m.mu.Lock()
	record.UpdatedAt = updated.UpdatedAt
	if session != nil {
		delete(m.sessions, id)
		m.recoveries[id] = record
	}
	m.mu.Unlock()
	input := app.PutInput{LocalPath: record.StagingPath, RepositoryPath: record.RepositoryPath}
	var err error
	if record.FollowEntryID {
		err = m.app.PutExpectedEntryIDWithMode(ctx, input, record.ObservedEntry, record.LogicalMode)
	} else {
		err = m.app.PutExpectedWithMode(ctx, input, record.Expectation, record.LogicalMode)
	}
	if err == nil {
		if err := m.markCommitted(record); err == nil {
			m.mu.Lock()
			delete(m.recoveries, id)
			delete(m.sessions, id)
			m.mu.Unlock()
			if cleanupErr := m.cleanupCommittedRecord(record); cleanupErr != nil {
				log.Printf("clean committed recovery Resource %s: %v", id, cleanupErr)
			}
			return nil
		}
		err = fmt.Errorf("recovery repository commit succeeded but completion record failed: %w", err)
	}
	return m.updateRecoveryFailure(record, err)
}

func (m *sessionManager) updateRecoveryFailure(record *recoveryRecord, cause error) error {
	persistErr := m.preserve(record, cause.Error())
	m.mu.Lock()
	record.inFlight = false
	m.mu.Unlock()
	return errors.Join(cause, persistErr)
}

func (m *sessionManager) discardRecovery(id string) error {
	m.mu.Lock()
	record, ok := m.recoveries[id]
	if !ok {
		m.mu.Unlock()
		return errSessionNotFound
	}
	if record.inFlight {
		m.mu.Unlock()
		return errSessionBusy
	}
	record.inFlight = true
	m.mu.Unlock()
	if err := m.markDiscarding(record, "explicit recovery discard"); err != nil {
		m.mu.Lock()
		record.inFlight = false
		m.mu.Unlock()
		return fmt.Errorf("persist recovery discard intent: %w", err)
	}
	if err := m.finishDiscardedRecord(record); err != nil {
		m.mu.Lock()
		record.inFlight = false
		m.mu.Unlock()
		return err
	}
	m.mu.Lock()
	delete(m.sessions, id)
	delete(m.recoveries, id)
	m.mu.Unlock()
	return nil
}

func (m *sessionManager) expireLoop() {
	defer m.wg.Done()
	interval := m.ttl / 4
	if interval > 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	if interval < 5*time.Millisecond {
		interval = 5 * time.Millisecond
	}
	ticker := newSessionExpiryTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.done:
			return
		case now := <-ticker.Chan():
			m.expireDue(now)
		}
	}
}

func (m *sessionManager) expireDue(now time.Time) {
	type expiringWrite struct {
		id      string
		session *writeSession
	}
	type expiringRead struct {
		id      string
		session *readSession
	}
	m.mu.Lock()
	writes := make([]expiringWrite, 0)
	reads := make([]expiringRead, 0)
	for id, session := range m.sessions {
		if !session.inFlight && !session.expired && !now.Before(session.expiresAt) {
			session.expired = true
			session.inFlight = true
			writes = append(writes, expiringWrite{id, session})
		}
	}
	for id, session := range m.reads {
		if !session.inFlight && !session.expired && !now.Before(session.expiresAt) {
			session.expired = true
			session.inFlight = true
			reads = append(reads, expiringRead{id, session})
		}
	}
	m.mu.Unlock()
	for _, item := range writes {
		if err := m.finishExpiredWrite(item.id, item.session); err != nil {
			log.Printf("expire write Resource session %s: %v", item.id, err)
		}
	}
	for _, item := range reads {
		if err := m.finishExpiredRead(item.id, item.session); err != nil {
			log.Printf("expire read Resource session %s: %v", item.id, err)
		}
	}
}

func (m *sessionManager) finishExpiredWrite(id string, session *writeSession) error {
	if session.committed {
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
		if err := m.cleanupCommittedRecord(&session.recoveryRecord); err != nil {
			log.Printf("retry committed Resource cleanup %s: %v", id, err)
		}
		return nil
	}
	if session.Discarding {
		err := m.finishDiscardedRecord(&session.recoveryRecord)
		m.mu.Lock()
		session.inFlight = false
		if err == nil {
			delete(m.sessions, id)
		}
		m.mu.Unlock()
		return err
	}
	err := m.preserve(&session.recoveryRecord, "Resource session expired before successful commit")
	m.mu.Lock()
	session.inFlight = false
	if err == nil {
		delete(m.sessions, id)
		m.recoveries[id] = &session.recoveryRecord
	}
	m.mu.Unlock()
	return err
}

func (m *sessionManager) finishExpiredRead(id string, session *readSession) error {
	err := session.release()
	m.mu.Lock()
	delete(m.reads, id)
	m.mu.Unlock()
	return err
}

func (m *sessionManager) preserve(record *recoveryRecord, reason string) error {
	if record.Discarding {
		return errors.New("resource session has a pending explicit discard")
	}
	if err := m.files.SyncFile(record.StagingPath); err != nil {
		reason += "; staging sync failed: " + err.Error()
		now := time.Now().UTC()
		m.mu.Lock()
		record.FailureReason = reason
		record.UpdatedAt = now
		m.mu.Unlock()
		return fmt.Errorf("sync recovery staging: %w", err)
	}
	now := time.Now().UTC()
	updated := *record
	updated.FailureReason = reason
	updated.UpdatedAt = now
	m.mu.Lock()
	record.FailureReason = updated.FailureReason
	record.UpdatedAt = updated.UpdatedAt
	m.mu.Unlock()
	if err := m.persistRecord(&updated); err != nil {
		return fmt.Errorf("persist recovery record: %w", err)
	}
	return nil
}

func (m *sessionManager) persistRecord(record *recoveryRecord) error {
	if err := m.validateRuntimeStage(record.SessionID, record.StagingPath, record.Committed || record.Discarding); err != nil {
		return err
	}
	persisted := *record
	persisted.Version = 2
	persisted.StagingPath = m.relativeStagePath(record.SessionID)
	data, err := json.Marshal(&persisted)
	if err != nil {
		return err
	}
	return m.files.WriteAtomic(m.directory, m.recordPath(record.SessionID), data)
}

func (m *sessionManager) markDiscarding(record *recoveryRecord, reason string) error {
	m.mu.Lock()
	updated := *record
	m.mu.Unlock()
	updated.Discarding = true
	updated.FailureReason = reason
	updated.UpdatedAt = time.Now().UTC()
	if err := m.persistRecord(&updated); err != nil {
		return err
	}
	m.mu.Lock()
	record.Discarding = true
	record.FailureReason = updated.FailureReason
	record.UpdatedAt = updated.UpdatedAt
	m.mu.Unlock()
	return nil
}

func (m *sessionManager) cleanupDiscardedRecord(record *recoveryRecord) error {
	if !record.Discarding {
		return errors.New("refuse cleanup without durable discard intent")
	}
	if err := m.files.Remove(record.StagingPath); err != nil {
		return err
	}
	if err := m.files.Remove(m.recordPath(record.SessionID)); err != nil {
		return err
	}
	return m.files.SyncDirectory(m.directory)
}

func (m *sessionManager) finishDiscardedRecord(record *recoveryRecord) error {
	if record.UnlinkEntryID != "" {
		_, _, err := m.app.ResolveEntry(context.Background(), record.UnlinkEntryID)
		if err == nil {
			if err := m.app.RemoveEntry(context.Background(), record.UnlinkEntryID); err != nil && !errors.Is(err, metadata.ErrEntryNotFound) {
				return fmt.Errorf("finish unlink for Entry %s: %w", record.UnlinkEntryID, err)
			}
		} else if !errors.Is(err, metadata.ErrEntryNotFound) {
			return fmt.Errorf("resolve unlink Entry %s: %w", record.UnlinkEntryID, err)
		}
	}
	return m.cleanupDiscardedRecord(record)
}

func (m *sessionManager) markCommitted(record *recoveryRecord) error {
	updated := *record
	updated.Committed = true
	updated.UpdatedAt = time.Now().UTC()
	if err := m.persistRecord(&updated); err != nil {
		return err
	}
	m.mu.Lock()
	record.Committed = true
	record.UpdatedAt = updated.UpdatedAt
	m.mu.Unlock()
	return nil
}

func (m *sessionManager) cleanupCommittedRecord(record *recoveryRecord) error {
	if err := m.files.Remove(record.StagingPath); err != nil {
		return err
	}
	if err := m.files.Remove(m.recordPath(record.SessionID)); err != nil {
		return err
	}
	return m.files.SyncDirectory(m.directory)
}

func (m *sessionManager) loadRecoveries() error {
	entries, err := m.files.ReadDir(m.directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(m.directory, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect Resource recovery record %q: %w", path, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("invalid Resource recovery record %q: not a regular file", path)
		}
		data, err := m.files.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read Resource recovery record %q: %w", path, err)
		}
		var record recoveryRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return fmt.Errorf("decode Resource recovery record %q: %w", path, err)
		}
		if err := validateSessionID(record.SessionID); err != nil || (record.Committed && record.Discarding) || m.recordPath(record.SessionID) != path {
			return fmt.Errorf("invalid Resource recovery record %q", path)
		}
		if err := m.resolveManifestStage(&record); err != nil {
			return fmt.Errorf("invalid Resource recovery record %q: %w", path, err)
		}
		if err := m.restoreLegacyStageAccess(&record); err != nil {
			return fmt.Errorf("restore access to legacy Resource staging %q: %w", path, err)
		}
		if record.ObservedObjectID != "" {
			objectID, err := content.ParseID(record.ObservedObjectID)
			if err != nil {
				return fmt.Errorf("invalid observed Object ID in Resource recovery record %q: %w", path, err)
			}
			record.ObservedEntry.ObjectID = &objectID
		}
		if record.Committed {
			if err := m.cleanupCommittedRecord(&record); err != nil {
				log.Printf("clean committed Resource record %s: %v", record.SessionID, err)
			}
			continue
		}
		if record.Discarding {
			if err := m.finishDiscardedRecord(&record); err != nil {
				return fmt.Errorf("finish discarded Resource cleanup %s: %w", record.SessionID, err)
			}
			continue
		}
		if record.FailureReason == "" {
			if err := m.preserve(&record, "daemon stopped before session completed"); err != nil {
				log.Printf("record interrupted Resource session %s for recovery: %v", record.SessionID, err)
			}
		}
		m.recoveries[record.SessionID] = &record
	}
	return nil
}

func (m *sessionManager) recordPath(id string) string { return filepath.Join(m.directory, id+".json") }

func validateSessionID(id string) error {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return fmt.Errorf("session ID is not a canonical UUID")
	}
	return nil
}

func (m *sessionManager) relativeStagePath(id string) string {
	return filepath.ToSlash(filepath.Join("temp", "rpc-sessions", id+".stage"))
}

func (m *sessionManager) expectedStagePath(id string) string {
	return filepath.Join(m.directory, id+".stage")
}

func validateSessionDirectories(root, directory string) error {
	for _, path := range []string{filepath.Join(root, "temp"), directory} {
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect Recovery directory %q: %w", path, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("recovery path %q is not a real directory", path)
		}
	}
	return nil
}

func (m *sessionManager) validateRuntimeStage(id, path string, allowMissing bool) error {
	if err := validateSessionID(id); err != nil {
		return err
	}
	expected := m.expectedStagePath(id)
	if filepath.Clean(path) != expected {
		return fmt.Errorf("staging path does not match session ID %q", id)
	}
	if err := validateSessionDirectories(m.root, m.directory); err != nil {
		return err
	}
	info, err := os.Lstat(expected)
	if errors.Is(err, os.ErrNotExist) && allowMissing {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect Recovery staging %q: %w", expected, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("recovery staging %q is not a regular file", expected)
	}
	return nil
}

func (m *sessionManager) resolveManifestStage(record *recoveryRecord) error {
	switch record.Version {
	case 2:
		if filepath.IsAbs(record.StagingPath) || record.StagingPath != m.relativeStagePath(record.SessionID) {
			return errors.New("version 2 staging path is not the canonical Repository-relative session path")
		}
	default:
		return fmt.Errorf("unsupported Recovery manifest version %d", record.Version)
	}

	stagePath := m.expectedStagePath(record.SessionID)
	allowMissing := record.Committed || record.Discarding
	if err := m.validateRuntimeStage(record.SessionID, stagePath, allowMissing); err != nil {
		return err
	}
	record.StagingPath = stagePath
	return nil
}

// Legacy FUSE sessions could chmod their private stage to mode 000. Old
// manifests have no writer-kind field, so zero permissions are the only
// recoverable signal for that legacy case. Other nil-mode records stay
// mode-unspecified, preserving ordinary Resource commit semantics. A legacy
// ordinary Resource deliberately chmodded to 000 is indistinguishable; in
// that case recovery treats its captured zero mode as explicit.
func (m *sessionManager) restoreLegacyStageAccess(record *recoveryRecord) error {
	if record.Committed || record.Discarding {
		return nil
	}
	info, err := os.Stat(record.StagingPath)
	if err != nil {
		return err
	}
	if record.LogicalMode == nil {
		// Old FUSE sessions could chmod staging to 000, while ordinary Resource
		// sessions generally left it 0600. This isolated migration preserves
		// FUSE mode 000 when detectable; other old records use the captured
		// stage mode as the best available estimate. Provenance is unavailable.
		mode := uint32(info.Mode().Perm() & 0o7777)
		if mode == 0o600 {
			mode = 0o644 // legacy Resource API omission default
		}
		updated := *record
		updated.LogicalMode = &mode
		updated.UpdatedAt = time.Now().UTC()
		if err := m.persistRecord(&updated); err != nil {
			return fmt.Errorf("persist migrated legacy logical mode: %w", err)
		}
		record.LogicalMode = &mode
		record.UpdatedAt = updated.UpdatedAt
	}
	if info.Mode().Perm()&0o600 == 0o600 {
		return nil
	}
	if err := os.Chmod(record.StagingPath, 0o600); err != nil {
		return err
	}
	return m.files.SyncFile(record.StagingPath)
}

func (m *sessionManager) close() error {
	m.stopOnce.Do(func() { close(m.done) })
	m.wg.Wait()
	m.mu.Lock()
	writeIDs := make([]string, 0, len(m.sessions))
	committed := make([]*recoveryRecord, 0)
	for id, session := range m.sessions {
		if !session.committed {
			writeIDs = append(writeIDs, id)
		} else {
			committed = append(committed, &session.recoveryRecord)
			delete(m.sessions, id)
		}
	}
	readIDs := make([]string, 0, len(m.reads))
	for id := range m.reads {
		readIDs = append(readIDs, id)
	}
	m.mu.Unlock()
	var failures []error
	for _, record := range committed {
		if err := m.cleanupCommittedRecord(record); err != nil {
			failures = append(failures, err)
		}
	}
	for _, id := range writeIDs {
		m.mu.Lock()
		session := m.sessions[id]
		m.mu.Unlock()
		if session == nil {
			continue
		}
		if session.Discarding {
			if err := m.finishDiscardedRecord(&session.recoveryRecord); err != nil {
				failures = append(failures, err)
				continue
			}
			m.mu.Lock()
			delete(m.sessions, id)
			m.mu.Unlock()
			continue
		} else if err := m.preserve(&session.recoveryRecord, "daemon stopped before session completed"); err != nil {
			failures = append(failures, err)
			m.mu.Lock()
			session.expired = true
			m.mu.Unlock()
			continue
		}
		m.mu.Lock()
		delete(m.sessions, id)
		m.recoveries[id] = &session.recoveryRecord
		m.mu.Unlock()
	}
	for _, id := range readIDs {
		m.mu.Lock()
		session := m.reads[id]
		m.mu.Unlock()
		if session != nil {
			if err := session.release(); err != nil {
				failures = append(failures, err)
			}
			m.mu.Lock()
			delete(m.reads, id)
			m.mu.Unlock()
		}
	}
	return errors.Join(failures...)
}

func syncFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func protocolRecovery(record recoveryRecord) controlrpc.RecoveryRecord {
	return controlrpc.RecoveryRecord{
		SessionID: record.SessionID, StagingURI: (&url.URL{Scheme: "file", Path: record.StagingPath}).String(),
		RepositoryPath: record.RepositoryPath, ExpectedEntryID: string(record.Expectation.EntryID),
		ExpectedRevision: record.Expectation.Revision, MustBeAbsent: record.Expectation.MustBeAbsent,
		FailureReason: record.FailureReason, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
		Size: stageFileSize(record.StagingPath),
	}
}

func stageFileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return info.Size()
}
