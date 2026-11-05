// Package format owns the meaning and compatibility policy of persisted
// Repository Format metadata.
package format

import (
	"errors"
	"fmt"
)

// ErrUnsupported identifies repository format metadata this binary cannot use.
var ErrUnsupported = errors.New("unsupported repository format")

const (
	CurrentVersion         = 1
	ContentAlgorithmSHA256 = "sha256"
	ContentIDFormatSHA256  = "sha256-<lowercase-hex>"
)

// Format describes durable Repository semantics. SQLite schema versions are
// deliberately tracked separately from SQLite's PRAGMA user_version schema
// authority.
type Format struct {
	Version          int
	ContentAlgorithm string
	ContentIDFormat  string
}

// Current returns the Repository Format created by this version of nando.
func Current() Format {
	return Format{
		Version:          CurrentVersion,
		ContentAlgorithm: ContentAlgorithmSHA256,
		ContentIDFormat:  ContentIDFormatSHA256,
	}
}

// Validate checks whether this binary can interpret the persisted semantics.
func (f Format) Validate() error {
	current := Current()
	if f != current {
		return fmt.Errorf("%w: found version=%d algorithm=%q id_format=%q; supported version=%d algorithm=%q id_format=%q",
			ErrUnsupported, f.Version, f.ContentAlgorithm, f.ContentIDFormat,
			current.Version, current.ContentAlgorithm, current.ContentIDFormat)
	}
	return nil
}
