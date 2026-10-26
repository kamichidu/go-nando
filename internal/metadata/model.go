package metadata

import (
	"errors"
	"fmt"
	"time"

	"github.com/kamichidu/go-nando/internal/content"
)

// ErrEntryNotFound is returned when a requested metadata entry does not exist.
var ErrEntryNotFound = errors.New("metadata entry not found")

// ErrEntryConflict is returned when there is a unique constraint violation (e.g. parent/name already exists).
var ErrEntryConflict = errors.New("metadata entry conflict")

// EntryID is a stable, immutable, globally unique entry identifier.
type EntryID string

// String returns the string representation of the EntryID.
func (id EntryID) String() string {
	return string(id)
}

// EntryKind defines the kind of a metadata entry: file or directory.
type EntryKind string

const (
	EntryKindFile      EntryKind = "file"
	EntryKindDirectory EntryKind = "directory"
)

// Timestamp is the number of nanoseconds elapsed since the Unix epoch.
type Timestamp int64

// TimestampFromTime converts a time.Time to a Timestamp.
func TimestampFromTime(t time.Time) Timestamp {
	return Timestamp(t.UnixNano())
}

// Time converts a Timestamp to a time.Time in UTC.
func (t Timestamp) Time() time.Time {
	return time.Unix(0, int64(t)).UTC()
}

// Entry represents the metadata of a file or directory in the nando logical repository.
type Entry struct {
	ID          EntryID
	ParentID    *EntryID // nil for the root entry
	Name        string   // Local name of the entry under its parent (empty for root)
	Kind        EntryKind
	ObjectID    *content.ID // pointer to validated Content ID (nil for directories)
	Size        int64       // Size in bytes
	Mtime       Timestamp   // Modification time in UTC
	Mode        uint32      // Unix-style file mode
	ContentType string      // MIME type/Content-Type
	Revision    uint64      // Per-entry revision (monotonically increasing)
	CreatedAt   Timestamp   // Created timestamp (UTC)
	UpdatedAt   Timestamp   // Updated timestamp (UTC)
}

// ParseEntryID validates and returns an EntryID.
func ParseEntryID(value string) (EntryID, error) {
	if value == "" {
		return "", fmt.Errorf("entry ID cannot be empty")
	}
	return EntryID(value), nil
}
