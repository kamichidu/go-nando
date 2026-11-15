package objectstore

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/kamichidu/go-nando/internal/content"
)

// ErrObjectNotFound is returned when an object is not found in the store.
var ErrObjectNotFound = errors.New("object not found")

// ErrHashMismatch is returned when the object's content hash does not match its identifier.
var ErrHashMismatch = errors.New("object hash mismatch")

// ErrStagingNotFound indicates that an operation's one-time staging file has
// already been removed and cannot be recreated.
var ErrStagingNotFound = errors.New("staging file not found")

// CommitResult holds the content ID and persisted size of a committed object.
type CommitResult struct {
	ID   content.ID
	Size int64
}

// RemoteObjectInfo is API-observed metadata for one remote immutable object.
type RemoteObjectInfo struct {
	ID content.ID
	// ContentID is the provider's raw content-ID metadata. It is retained when
	// ID cannot be parsed so maintenance can report malformed remote objects.
	ContentID string
	Size      int64
	MD5       []byte
}

// LocalObjectInfo exposes only physical accounting data, not filesystem paths.
type LocalObjectInfo struct {
	ID      content.ID
	Size    int64
	ModTime time.Time
}

// RemoteObjectStore contains operations for the single selected remote object
// storage backend. It does not own logical paths or Repository consistency.
type RemoteObjectStore interface {
	Upload(context.Context, content.ID, int64, []byte, io.Reader) (RemoteObjectInfo, error)
	OpenRemote(context.Context, content.ID) (io.ReadCloser, error)
	RemoteExists(context.Context, content.ID) (bool, error)
	RemoteAttrs(context.Context, content.ID) (RemoteObjectInfo, error)
	ListRemoteObjects(context.Context) ([]RemoteObjectInfo, error)
}

// StagedObject is an immutable backend-owned handle produced by Stage.
// Repository code must not interpret its representation.
type StagedObject interface {
	ID() content.ID
	Size() int64
	MD5() []byte
}

// StageSession owns one operation-ID-specific temporary file opened for a
// single Stage attempt. Stage must never reopen or recreate the file path.
type StageSession interface {
	Stage(ctx context.Context, src io.Reader) (StagedObject, error)
	Close() error
}

// ObjectStore defines the interface for interacting with the content-addressable object store.
type ObjectStore interface {
	Commit(ctx context.Context, src io.Reader) (CommitResult, error)
	BeginStage(ctx context.Context, operationID string) (StageSession, error)
	Publish(ctx context.Context, staged StagedObject) (CommitResult, error)
	CleanupStaging(ctx context.Context, operationID string) error
	ListStaging(ctx context.Context) ([]string, error)
	Open(ctx context.Context, id content.ID) (io.ReadCloser, error)
	Exists(ctx context.Context, id content.ID) (bool, error)
	Delete(ctx context.Context, id content.ID) error
	Verify(ctx context.Context, id content.ID) error
	List(ctx context.Context) ([]content.ID, error)
	ListRaw(ctx context.Context) ([]string, error)
}
