package content

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"io"
)

// Digest represents a content hash as a lowercase hexadecimal string.
type Digest string

// String returns the string representation of the digest.
func (d Digest) String() string {
	return string(d)
}

// Hasher defines the interface for calculating content hashes.
type Hasher interface {
	Sum(ctx context.Context, r io.Reader) (Digest, error)
}

// NewHasher returns the default SHA-1 hasher.
func NewHasher() Hasher {
	return &sha1Hasher{}
}

type sha1Hasher struct{}

// Sum calculates the SHA-1 hash of the given reader's content and returns it as a Digest.
func (h *sha1Hasher) Sum(ctx context.Context, r io.Reader) (Digest, error) {
	hasher := sha1.New()
	if _, err := io.Copy(hasher, r); err != nil {
		return "", err
	}
	return Digest(hex.EncodeToString(hasher.Sum(nil))), nil
}
