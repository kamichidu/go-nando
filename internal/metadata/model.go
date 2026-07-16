package metadata

import (
	"errors"
	"time"
)

// ErrEntryNotFound is returned when a requested metadata entry does not exist.
var ErrEntryNotFound = errors.New("metadata entry not found")

// ErrEntryConflict is returned when there is an unique constraint violation (e.g. path already exists).
var ErrEntryConflict = errors.New("metadata entry path conflict")

// EntryState defines the state of a metadata entry.
type EntryState string

const (
	StatePending EntryState = "pending"
	StateReady   EntryState = "ready"
	StateDeleted EntryState = "deleted"
)

// Entry represents the metadata of a file or directory in the nando logical repository.
type Entry struct {
	Path        string     // Absolute logical path (e.g. "/docs/report.pdf")
	Hash        string     // Content Addressable Object Hash
	Size        int64      // Size in bytes
	Mtime       time.Time  // Modification time in UTC
	Mode        uint32     // Unix-style file mode
	ContentType string     // MIME type/Content-Type
	State       EntryState // "pending", "ready", or "deleted"
	IsDir       bool       // Identifies directory entries vs normal files
	CreatedAt   time.Time  // Created timestamp (UTC)
	UpdatedAt   time.Time  // Updated timestamp (UTC)
}
