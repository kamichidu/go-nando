package controlrpc

import (
	"fmt"
	"time"
)

const (
	ResourceSessionTTL           = 30 * time.Second
	ResourceSessionRenewInterval = 5 * time.Second
)

const (
	CodeParseError             = -32700
	CodeInvalidRequest         = -32600
	CodeMethodNotFound         = -32601
	CodeInvalidParams          = -32602
	CodeInternalError          = -32603
	CodeEntryNotFound          = -32004
	CodeEntryConflict          = -32009
	CodeResourceSessionExpired = -32011
	CodeEntryExists            = -32012
	CodeDirectoryNotEmpty      = -32013
	CodePermissionDenied       = -32014
)

const (
	MethodPing            = "daemon.ping"
	MethodStat            = "repository.stat"
	MethodResolveEntry    = "repository.resolveEntry"
	MethodChmodEntry      = "repository.chmodEntry"
	MethodList            = "repository.list"
	MethodMkdir           = "repository.mkdir"
	MethodMove            = "repository.move"
	MethodMoveEntry       = "repository.moveEntry"
	MethodRemove          = "repository.remove"
	MethodRemoveEntry     = "repository.removeEntry"
	MethodGC              = "repository.gc"
	MethodMaintenance     = "repository.maintenanceOnce"
	MethodDoctor          = "repository.doctor"
	MethodSync            = "repository.sync"
	MethodBeginRead       = "resource.beginRead"
	MethodBeginWrite      = "resource.beginWrite"
	MethodSetMode         = "resource.setMode"
	MethodCommit          = "resource.commit"
	MethodAbort           = "resource.abort"
	MethodMarkUnlinked    = "resource.markUnlinked"
	MethodRelease         = "resource.release"
	MethodRenew           = "resource.renew"
	MethodRevoke          = "resource.revoke"
	MethodRecoveryList    = "resource.recovery.list"
	MethodRecoveryRetry   = "resource.recovery.retry"
	MethodRecoveryDiscard = "resource.recovery.discard"
)

type BeginWriteResult struct {
	SessionID        string `json:"session_id"`
	ResourceURI      string `json:"resource_uri"`
	ExpectedEntryID  string `json:"expected_entry_id,omitempty"`
	ExpectedRevision uint64 `json:"expected_revision"`
	ExpectedMtime    int64  `json:"expected_mtime"`
	MustBeAbsent     bool   `json:"must_be_absent"`
}

type BeginWriteParams struct {
	Path            string   `json:"path"`
	EntryID         string   `json:"entry_id,omitempty"`
	ResourceSchemes []string `json:"resource_schemes"`
	FollowEntryID   bool     `json:"follow_entry_id,omitempty"`
	LogicalMode     *uint32  `json:"logical_mode,omitempty"`
}

type SetModeParams struct {
	SessionID string `json:"session_id"`
	Mode      uint32 `json:"mode"`
}

type BeginReadParams struct {
	Path            string   `json:"path"`
	EntryID         string   `json:"entry_id,omitempty"`
	ResourceSchemes []string `json:"resource_schemes"`
}

type ResolveEntryParams struct {
	EntryID string `json:"entry_id"`
}

type ChmodEntryParams struct {
	EntryID string `json:"entry_id"`
	Mode    uint32 `json:"mode"`
}

type MoveEntryParams struct {
	EntryID     string `json:"entry_id"`
	Destination string `json:"destination"`
}

type RemoveEntryParams struct {
	EntryID string `json:"entry_id"`
}

type BeginReadResult struct {
	SessionID   string `json:"session_id"`
	ResourceURI string `json:"resource_uri"`
	Entry       Entry  `json:"entry"`
}

// ResolveEntryResult identifies an Entry independently of its current name.
type ResolveEntryResult struct {
	Entry Entry  `json:"entry"`
	Path  string `json:"path"`
}

type RenewResult struct {
	ExpiresAt time.Time `json:"expires_at"`
}

type RecoveryRecord struct {
	SessionID        string    `json:"session_id"`
	StagingURI       string    `json:"staging_uri"`
	RepositoryPath   string    `json:"repository_path"`
	ExpectedEntryID  string    `json:"expected_entry_id,omitempty"`
	ExpectedRevision uint64    `json:"expected_revision"`
	MustBeAbsent     bool      `json:"must_be_absent"`
	FailureReason    string    `json:"failure_reason"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	Size             int64     `json:"size"`
}

// Entry is the protocol representation of logical metadata. ObjectID is the
// canonical content ID string; timestamps are Unix nanoseconds.
type Entry struct {
	ID          string  `json:"id"`
	ParentID    *string `json:"parent_id"`
	Name        string  `json:"name"`
	Kind        string  `json:"kind"`
	ObjectID    string  `json:"object_id,omitempty"`
	Size        int64   `json:"size"`
	Mtime       int64   `json:"mtime"`
	Mode        uint32  `json:"mode"`
	ContentType string  `json:"content_type"`
	Revision    uint64  `json:"revision"`
	CreatedAt   int64   `json:"created_at"`
	UpdatedAt   int64   `json:"updated_at"`
}

// RemoteError is the error returned by a JSON-RPC peer.
type RemoteError struct {
	Code    int
	Message string
}

func (e *RemoteError) Error() string { return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message) }
