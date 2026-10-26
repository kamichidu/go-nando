package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

type Algorithm string

const (
	AlgorithmSHA256 Algorithm = "sha256"
)

// ID represents a validated content-addressable object identifier.
type ID struct {
	algorithm Algorithm
	digest    [32]byte
}

// NewID creates a new SHA-256 content ID.
func NewID(digest [32]byte) ID {
	return ID{
		algorithm: AlgorithmSHA256,
		digest:    digest,
	}
}

var ErrInvalidID = errors.New("invalid content ID")

// ParseID parses a canonical algorithm-namespaced content ID.
func ParseID(value string) (ID, error) {
	const prefix = "sha256-"
	if !strings.HasPrefix(value, prefix) {
		return ID{}, fmt.Errorf("%w: expected sha256- prefix", ErrInvalidID)
	}
	encoded := strings.TrimPrefix(value, prefix)
	if len(encoded) != 64 {
		return ID{}, fmt.Errorf("%w: SHA-256 digest must be exactly 64 characters", ErrInvalidID)
	}

	// Reject uppercase characters or non-hex
	for _, r := range encoded {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ID{}, fmt.Errorf("%w: must be lowercase hexadecimal characters only", ErrInvalidID)
		}
	}

	// Decode hex
	bytes, err := hex.DecodeString(encoded)
	if err != nil {
		return ID{}, fmt.Errorf("%w: failed to decode hex: %w", ErrInvalidID, err)
	}

	var digest [32]byte
	copy(digest[:], bytes)

	return ID{
		algorithm: AlgorithmSHA256,
		digest:    digest,
	}, nil
}

// Validate reports whether id contains a supported algorithm namespace.
func (id ID) Validate() error {
	if id.algorithm != AlgorithmSHA256 {
		return fmt.Errorf("%w: unsupported or empty algorithm namespace %q", ErrInvalidID, id.algorithm)
	}
	return nil
}

// String returns the canonical lowercase hexadecimal representation of the ID.
func (id ID) String() string {
	return string(id.algorithm) + "-" + hex.EncodeToString(id.digest[:])
}

// Algorithm returns the hashing algorithm used.
func (id ID) Algorithm() Algorithm {
	return id.algorithm
}

// Equal returns true if the two IDs are equal.
func (id ID) Equal(other ID) bool {
	return id.algorithm == other.algorithm && id.digest == other.digest
}

// Hasher defines the interface for calculating content hashes.
type Hasher interface {
	Sum(ctx context.Context, r io.Reader) (ID, error)
}

// NewHasher returns the default SHA-256 hasher.
func NewHasher() Hasher {
	return &sha256Hasher{}
}

type sha256Hasher struct{}

// Sum calculates the SHA-256 hash of the given reader's content and returns it as an ID.
func (h *sha256Hasher) Sum(ctx context.Context, r io.Reader) (ID, error) {
	hasher := sha256.New()
	if _, err := io.Copy(hasher, r); err != nil {
		return ID{}, err
	}
	var digest [32]byte
	copy(digest[:], hasher.Sum(nil))
	return NewID(digest), nil
}
