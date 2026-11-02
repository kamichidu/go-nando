package local

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/objectstore"
)

type stagedObject struct {
	store       *Store
	operationID string
	path        string
	id          content.ID
	size        int64
	info        os.FileInfo
	md5         []byte
}

func (s *stagedObject) ID() content.ID    { return s.id }
func (s *stagedObject) Size() int64       { return s.size }
func (s *stagedObject) LocalPath() string { return s.path }
func (s *stagedObject) MD5() []byte       { return append([]byte(nil), s.md5...) }

type stageSession struct {
	store       *Store
	operationID string
	path        string
	file        *os.File
	used        bool
	closed      bool
}

func (s *Store) BeginStage(ctx context.Context, operationID string) (objectstore.StageSession, error) {
	if err := validateOperationID(operationID); err != nil {
		return nil, err
	}
	tempPath := s.operationStagePath(operationID)
	file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create operation staging file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return &stageSession{store: s, operationID: operationID, path: tempPath, file: file}, nil
}

func (session *stageSession) Stage(ctx context.Context, src io.Reader) (result objectstore.StagedObject, retErr error) {
	if session.used || session.closed {
		return nil, errors.New("staging session can be used only once")
	}
	session.used = true
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file := session.file
	fileInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat open staging file: %w", err)
	}
	pathInfo, err := os.Stat(session.path)
	if err != nil || !os.SameFile(fileInfo, pathInfo) {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("verify staging path identity: %w", err)
		}
		return nil, objectstore.ErrStagingNotFound
	}

	written, err := io.Copy(file, contextReader{ctx: ctx, reader: src})
	if err != nil {
		return nil, fmt.Errorf("copy input to staged content: %w", err)
	}
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("sync staged content: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek staged content for hashing: %w", err)
	}
	md5Hash := md5.New()
	id, err := session.store.hasher.Sum(ctx, io.TeeReader(contextReader{ctx: ctx, reader: file}, md5Hash))
	if err != nil {
		return nil, fmt.Errorf("hash staged content: %w", err)
	}
	if err := id.Validate(); err != nil {
		return nil, fmt.Errorf("hasher returned invalid object ID: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek staged content for integrity verification: %w", err)
	}
	sha := sha256.New()
	verifiedBytes, err := io.Copy(sha, contextReader{ctx: ctx, reader: file})
	if err != nil {
		return nil, fmt.Errorf("hash staged content: %w", err)
	}
	if verifiedBytes != written || !bytes.Equal(sha.Sum(nil), id.Digest()) {
		return nil, objectstore.ErrHashMismatch
	}
	return &stagedObject{
		store:       session.store,
		operationID: session.operationID,
		path:        session.path,
		id:          id,
		size:        written,
		info:        fileInfo,
		md5:         md5Hash.Sum(nil),
	}, nil
}

func (session *stageSession) Close() error {
	if session.closed {
		return nil
	}
	session.closed = true
	return session.file.Close()
}

func (s *Store) Publish(ctx context.Context, staged objectstore.StagedObject) (objectstore.CommitResult, error) {
	localStage, ok := staged.(*stagedObject)
	if !ok || localStage.store != s {
		return objectstore.CommitResult{}, errors.New("staged object belongs to another Object Store")
	}
	if err := localStage.id.Validate(); err != nil {
		return objectstore.CommitResult{}, err
	}
	if localStage.path != s.operationStagePath(localStage.operationID) {
		return objectstore.CommitResult{}, errors.New("staged object path does not match its operation ID")
	}
	if err := ctx.Err(); err != nil {
		return objectstore.CommitResult{}, err
	}
	pathInfo, err := os.Stat(localStage.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return objectstore.CommitResult{}, objectstore.ErrStagingNotFound
		}
		return objectstore.CommitResult{}, fmt.Errorf("stat staged object: %w", err)
	}
	if localStage.info == nil || !os.SameFile(localStage.info, pathInfo) {
		return objectstore.CommitResult{}, objectstore.ErrStagingNotFound
	}
	destPath := filepath.Join(s.objectsDir, localStage.id.String())
	exists, err := s.Exists(ctx, localStage.id)
	if err != nil {
		return objectstore.CommitResult{}, err
	}
	if exists {
		if err := s.Verify(ctx, localStage.id); err != nil {
			return objectstore.CommitResult{}, fmt.Errorf("existing object %s is corrupt: %w", localStage.id, err)
		}
		if err := os.Remove(localStage.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return objectstore.CommitResult{}, fmt.Errorf("remove reused staged object: %w", err)
		}
		return objectstore.CommitResult{ID: localStage.id, Size: localStage.size}, nil
	}
	published, err := publishNoReplace(localStage.path, destPath)
	if err != nil {
		return objectstore.CommitResult{}, fmt.Errorf("publish staged object: %w", err)
	}
	if !published {
		if err := s.Verify(ctx, localStage.id); err != nil {
			return objectstore.CommitResult{}, fmt.Errorf("existing object %s is corrupt: %w", localStage.id, err)
		}
		if err := os.Remove(localStage.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return objectstore.CommitResult{}, fmt.Errorf("remove duplicate staged object: %w", err)
		}
		return objectstore.CommitResult{ID: localStage.id, Size: localStage.size}, nil
	}
	if err := os.Remove(localStage.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return objectstore.CommitResult{}, fmt.Errorf("object published but staged link cleanup failed: %w", err)
	}
	return objectstore.CommitResult{ID: localStage.id, Size: localStage.size}, nil
}

func (s *Store) CleanupStaging(ctx context.Context, operationID string) error {
	if err := validateOperationID(operationID); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	removeErr := os.Remove(s.operationStagePath(operationID))
	if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return fmt.Errorf("remove operation staging file: %w", removeErr)
	}
	return nil
}

func (s *Store) ListStaging(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(s.tempDir)
	if err != nil {
		return nil, fmt.Errorf("list staging directory: %w", err)
	}
	var operationIDs []string
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".content") {
			continue
		}
		operationID := strings.TrimSuffix(entry.Name(), ".content")
		if err := validateOperationID(operationID); err != nil {
			continue
		}
		operationIDs = append(operationIDs, operationID)
	}
	return operationIDs, nil
}

func (s *Store) operationStagePath(operationID string) string {
	return filepath.Join(s.tempDir, operationID+".content")
}

func validateOperationID(operationID string) error {
	id, err := uuid.Parse(operationID)
	if err != nil || id.String() != strings.ToLower(operationID) {
		return fmt.Errorf("invalid operation ID %q", operationID)
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}
