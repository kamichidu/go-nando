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
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kamichidu/go-nando/internal/app"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/paths"
	"github.com/kamichidu/go-nando/internal/repository"
	"github.com/kamichidu/go-nando/pkg/controlrpc"
)

const defaultResourceSessionTTL = controlrpc.ResourceSessionTTL

var (
	errSessionNotFound = errors.New("resource session not found")
	errSessionBusy     = errors.New("resource session operation in progress")
	errSessionClosed   = errors.New("resource session is closed")
	errSessionExpired  = errors.New("resource session expired")
)

type recoveryRecord struct {
	Version        int                       `json:"version"`
	Committed      bool                      `json:"committed"`
	SessionID      string                    `json:"session_id"`
	StagingPath    string                    `json:"staging_path"`
	RepositoryPath string                    `json:"repository_path"`
	Expectation    repository.PutExpectation `json:"expectation"`
	FailureReason  string                    `json:"failure_reason"`
	CreatedAt      time.Time                 `json:"created_at"`
	UpdatedAt      time.Time                 `json:"updated_at"`
	inFlight       bool                      `json:"-"`
}

type writeSession struct {
	recoveryRecord
	expiresAt time.Time
	committed bool
}

type readSession struct {
	release   func() error
	expiresAt time.Time
	inFlight  bool
}

type sessionManager struct {
	app        repositoryUseCases
	directory  string
	ttl        time.Duration
	mu         sync.Mutex
	sessions   map[string]*writeSession
	reads      map[string]*readSession
	recoveries map[string]*recoveryRecord
	done       chan struct{}
	wg         sync.WaitGroup
}

func newSessionManager(application repositoryUseCases, repoDir string) (*sessionManager, error) {
	return newSessionManagerWithTTL(application, repoDir, defaultResourceSessionTTL)
}

func newSessionManagerWithTTL(application repositoryUseCases, repoDir string, ttl time.Duration) (*sessionManager, error) {
	if ttl <= 0 {
		return nil, errors.New("resource session TTL must be positive")
	}
	directory := filepath.Join(repoDir, "temp", "rpc-sessions")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("create daemon Resource session directory: %w", err)
	}
	if err := syncDirectory(filepath.Dir(directory)); err != nil {
		return nil, fmt.Errorf("sync daemon Resource session parent directory: %w", err)
	}
	manager := &sessionManager{
		app: application, directory: directory, ttl: ttl,
		sessions: make(map[string]*writeSession), reads: make(map[string]*readSession),
		recoveries: make(map[string]*recoveryRecord), done: make(chan struct{}),
	}
	if err := manager.loadRecoveries(); err != nil {
		return nil, err
	}
	manager.wg.Add(1)
	go manager.expireLoop()
	return manager, nil
}

func (m *sessionManager) beginRead(ctx context.Context, repositoryPath string) (controlrpc.BeginReadResult, error) {
	entry, resourcePath, release, err := m.app.BeginReadResource(ctx, repositoryPath)
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

	id := uuid.NewString()
	resourcePath := filepath.Join(m.directory, id+".stage")
	file, err := os.OpenFile(resourcePath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return controlrpc.BeginWriteResult{}, fmt.Errorf("create mutable Resource: %w", err)
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return controlrpc.BeginWriteResult{}, errors.Join(fmt.Errorf("durably create mutable Resource: %w", err), os.Remove(resourcePath))
	}
	now := time.Now().UTC()
	session := &writeSession{recoveryRecord: recoveryRecord{
		Version: 1, SessionID: id, StagingPath: resourcePath, RepositoryPath: normalized, Expectation: expectation,
		CreatedAt: now, UpdatedAt: now,
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
	return controlrpc.BeginWriteResult{SessionID: id, ResourceURI: resourceURL}, nil
}

func (m *sessionManager) renew(id string) (time.Time, error) {
	m.mu.Lock()
	session, ok := m.sessions[id]
	if ok {
		if session.inFlight {
			m.mu.Unlock()
			return time.Time{}, errSessionBusy
		}
		if session.committed {
			m.mu.Unlock()
			return time.Time{}, errSessionClosed
		}
		if !time.Now().Before(session.expiresAt) {
			m.mu.Unlock()
			return time.Time{}, errors.Join(errSessionExpired, m.expireWrite(id))
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
	if read.inFlight {
		m.mu.Unlock()
		return time.Time{}, errSessionBusy
	}
	if !time.Now().Before(read.expiresAt) {
		m.mu.Unlock()
		return time.Time{}, errors.Join(errSessionExpired, m.expireRead(id))
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
	if session.committed {
		m.mu.Unlock()
		return nil
	}
	if !time.Now().Before(session.expiresAt) {
		m.mu.Unlock()
		return errors.Join(errSessionExpired, m.expireWrite(id))
	}
	session.inFlight = true
	m.mu.Unlock()

	commitErr := syncFile(session.StagingPath)
	if commitErr == nil {
		commitErr = m.app.PutExpected(ctx, app.PutInput{LocalPath: session.StagingPath, RepositoryPath: session.RepositoryPath}, session.Expectation)
	}
	if commitErr != nil {
		preserveErr := m.preserve(&session.recoveryRecord, commitErr.Error())
		m.mu.Lock()
		delete(m.sessions, id)
		session.inFlight = false
		m.recoveries[id] = &session.recoveryRecord
		m.mu.Unlock()
		return errors.Join(commitErr, preserveErr)
	}
	if err := m.markCommitted(&session.recoveryRecord); err != nil {
		preserveErr := m.preserve(&session.recoveryRecord, "repository commit succeeded but durable completion record failed: "+err.Error())
		m.mu.Lock()
		delete(m.sessions, id)
		session.inFlight = false
		m.recoveries[id] = &session.recoveryRecord
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
	if !time.Now().Before(session.expiresAt) {
		m.mu.Unlock()
		return errors.Join(errSessionExpired, m.expireWrite(id))
	}
	session.inFlight = true
	m.mu.Unlock()
	err := m.removeStageAndRecord(id, session.StagingPath)
	m.mu.Lock()
	if err == nil {
		delete(m.sessions, id)
	} else {
		session.inFlight = false
	}
	m.mu.Unlock()
	return err
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
		if session.committed {
			delete(m.sessions, id)
			m.mu.Unlock()
			return nil
		}
		session.inFlight = true
		m.mu.Unlock()
		err := m.preserve(&session.recoveryRecord, "session released before successful commit")
		m.mu.Lock()
		delete(m.sessions, id)
		session.inFlight = false
		m.recoveries[id] = &session.recoveryRecord
		m.mu.Unlock()
		return err
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

func (m *sessionManager) revoke(id string) error {
	return m.release(id)
}

func (m *sessionManager) listRecoveries() []controlrpc.RecoveryRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]controlrpc.RecoveryRecord, 0, len(m.recoveries))
	for _, record := range m.recoveries {
		result = append(result, protocolRecovery(*record))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result
}

func (m *sessionManager) retryRecovery(ctx context.Context, id string) error {
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
	if err := syncFile(record.StagingPath); err != nil {
		return m.updateRecoveryFailure(record, err)
	}
	err := m.app.PutExpected(ctx, app.PutInput{LocalPath: record.StagingPath, RepositoryPath: record.RepositoryPath}, record.Expectation)
	if err == nil {
		if err := m.markCommitted(record); err == nil {
			m.mu.Lock()
			delete(m.recoveries, id)
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
	if err := m.removeStageAndRecord(id, record.StagingPath); err != nil {
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

func (m *sessionManager) expireLoop() {
	defer m.wg.Done()
	interval := m.ttl / 4
	if interval > 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	if interval < 5*time.Millisecond {
		interval = 5 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.done:
			return
		case now := <-ticker.C:
			m.expireDue(now)
		}
	}
}

func (m *sessionManager) expireDue(now time.Time) {
	m.mu.Lock()
	writeIDs := make([]string, 0)
	readIDs := make([]string, 0)
	for id, session := range m.sessions {
		if !session.inFlight && !now.Before(session.expiresAt) {
			writeIDs = append(writeIDs, id)
		}
	}
	for id, session := range m.reads {
		if !session.inFlight && !now.Before(session.expiresAt) {
			readIDs = append(readIDs, id)
		}
	}
	m.mu.Unlock()
	for _, id := range writeIDs {
		if err := m.expireWrite(id); err != nil {
			log.Printf("expire write Resource session %s: %v", id, err)
		}
	}
	for _, id := range readIDs {
		if err := m.expireRead(id); err != nil {
			log.Printf("expire read Resource session %s: %v", id, err)
		}
	}
}

func (m *sessionManager) expireWrite(id string) error {
	m.mu.Lock()
	session, ok := m.sessions[id]
	if !ok || session.inFlight || time.Now().Before(session.expiresAt) {
		m.mu.Unlock()
		return nil
	}
	if session.committed {
		delete(m.sessions, id)
		m.mu.Unlock()
		if err := m.cleanupCommittedRecord(&session.recoveryRecord); err != nil {
			log.Printf("retry committed Resource cleanup %s: %v", id, err)
		}
		return nil
	}
	session.inFlight = true
	m.mu.Unlock()
	err := m.preserve(&session.recoveryRecord, "Resource session expired before successful commit")
	m.mu.Lock()
	delete(m.sessions, id)
	session.inFlight = false
	m.recoveries[id] = &session.recoveryRecord
	m.mu.Unlock()
	return err
}

func (m *sessionManager) expireRead(id string) error {
	m.mu.Lock()
	session, ok := m.reads[id]
	if !ok || session.inFlight || time.Now().Before(session.expiresAt) {
		m.mu.Unlock()
		return nil
	}
	session.inFlight = true
	m.mu.Unlock()
	err := session.release()
	m.mu.Lock()
	delete(m.reads, id)
	m.mu.Unlock()
	return err
}

func (m *sessionManager) preserve(record *recoveryRecord, reason string) error {
	var syncErr error
	if err := syncFile(record.StagingPath); err != nil {
		syncErr = fmt.Errorf("sync recovery staging: %w", err)
		reason += "; staging sync failed: " + err.Error()
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
		return errors.Join(syncErr, fmt.Errorf("persist recovery record: %w", err))
	}
	return syncErr
}

func (m *sessionManager) persistRecord(record *recoveryRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(m.directory, ".recovery-record-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	_, writeErr := temp.Write(data)
	syncErr := temp.Sync()
	closeErr := temp.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return errors.Join(err, os.Remove(tempPath))
	}
	if err := os.Rename(tempPath, m.recordPath(record.SessionID)); err != nil {
		return errors.Join(err, os.Remove(tempPath))
	}
	return syncDirectory(m.directory)
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
	if err := removeIfExists(record.StagingPath); err != nil {
		return err
	}
	if err := removeIfExists(m.recordPath(record.SessionID)); err != nil {
		return err
	}
	return syncDirectory(m.directory)
}

func (m *sessionManager) loadRecoveries() error {
	entries, err := os.ReadDir(m.directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(m.directory, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read Resource recovery record %q: %w", path, err)
		}
		var record recoveryRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return fmt.Errorf("decode Resource recovery record %q: %w", path, err)
		}
		if record.Version != 1 || record.SessionID == "" || filepath.Base(m.recordPath(record.SessionID)) != entry.Name() || !m.ownsStagePath(record.StagingPath) {
			return fmt.Errorf("invalid Resource recovery record %q", path)
		}
		if record.Committed {
			if err := m.cleanupCommittedRecord(&record); err != nil {
				log.Printf("clean committed Resource record %s: %v", record.SessionID, err)
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

func (m *sessionManager) ownsStagePath(path string) bool {
	rel, err := filepath.Rel(m.directory, path)
	return err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}

func (m *sessionManager) close() error {
	close(m.done)
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
		if err := m.preserve(&session.recoveryRecord, "daemon stopped before session completed"); err != nil {
			failures = append(failures, err)
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

func (m *sessionManager) removeStageAndRecord(id, stagePath string) error {
	err := errors.Join(removeIfExists(stagePath), removeIfExists(m.recordPath(id)))
	if err != nil {
		return err
	}
	return syncDirectory(m.directory)
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
