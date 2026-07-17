package content

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

type Algorithm string

const (
	AlgorithmSHA1 Algorithm = "sha1"
)

// ID represents a validated content-addressable object identifier.
type ID struct {
	algorithm Algorithm
	digest    [20]byte
}

// NewID creates a new ID from a SHA-1 raw 20-byte array.
func NewID(digest [20]byte) ID {
	return ID{
		algorithm: AlgorithmSHA1,
		digest:    digest,
	}
}

var ErrInvalidID = errors.New("invalid content ID")

// ParseID parses a lowercase hexadecimal string as a validated ID.
func ParseID(value string) (ID, error) {
	// Reject invalid length
	if len(value) != 40 {
		return ID{}, fmt.Errorf("%w: length must be exactly 40 characters", ErrInvalidID)
	}

	// Reject uppercase characters or non-hex
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ID{}, fmt.Errorf("%w: must be lowercase hexadecimal characters only", ErrInvalidID)
		}
	}

	// Decode hex
	bytes, err := hex.DecodeString(value)
	if err != nil {
		return ID{}, fmt.Errorf("%w: failed to decode hex: %w", ErrInvalidID, err)
	}

	var digest [20]byte
	copy(digest[:], bytes)

	return ID{
		algorithm: AlgorithmSHA1,
		digest:    digest,
	}, nil
}

// String returns the canonical lowercase hexadecimal representation of the ID.
func (id ID) String() string {
	return hex.EncodeToString(id.digest[:])
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

// NewHasher returns the default SHA-1 hasher.
func NewHasher() Hasher {
	return &sha1Hasher{}
}

type sha1Hasher struct{}

// Sum calculates the SHA-1 hash of the given reader's content and returns it as an ID.
func (h *sha1Hasher) Sum(ctx context.Context, r io.Reader) (ID, error) {
	hasher := sha1.New()
	if _, err := io.Copy(hasher, r); err != nil {
		return ID{}, err
	}
	var digest [20]byte
	copy(digest[:], hasher.Sum(nil))
	return NewID(digest), nil
}
