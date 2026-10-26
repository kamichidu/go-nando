package content_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kamichidu/go-nando/internal/content"
)

func TestHasher(t *testing.T) {
	hasher := content.NewHasher()
	ctx := context.Background()

	t.Run("same content produces same hash", func(t *testing.T) {
		r1 := strings.NewReader("hello nando")
		r2 := strings.NewReader("hello nando")

		h1, err := hasher.Sum(ctx, r1)
		if err != nil {
			t.Fatalf("failed to hash: %v", err)
		}
		h2, err := hasher.Sum(ctx, r2)
		if err != nil {
			t.Fatalf("failed to hash: %v", err)
		}

		if !h1.Equal(h2) {
			t.Errorf("expected same hash, got %s and %s", h1.String(), h2.String())
		}
	})

	t.Run("different content produces different hash", func(t *testing.T) {
		r1 := strings.NewReader("hello nando")
		r2 := strings.NewReader("hello nando!")

		h1, err := hasher.Sum(ctx, r1)
		if err != nil {
			t.Fatalf("failed to hash: %v", err)
		}
		h2, err := hasher.Sum(ctx, r2)
		if err != nil {
			t.Fatalf("failed to hash: %v", err)
		}

		if h1.Equal(h2) {
			t.Errorf("expected different hash, but both are %s", h1.String())
		}
	})

	t.Run("empty content is handled correctly", func(t *testing.T) {
		r := strings.NewReader("")
		h, err := hasher.Sum(ctx, r)
		if err != nil {
			t.Fatalf("failed to hash empty: %v", err)
		}

		expected := "sha256-e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" // SHA-256 of empty string
		if h.String() != expected {
			t.Errorf("expected %q, got %q", expected, h.String())
		}
	})

	t.Run("Content ID parsing and validation", func(t *testing.T) {
		tests := []struct {
			input string
			valid bool
		}{
			{input: "sha256-e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", valid: true},
			{input: "sha256-4b234e4b38341d10a8135b4a0af020a2c4db56b805272fd0fde62c568c8dd77e", valid: true},
			{input: "da39a3ee5e6b4b0d3255bfef95601890afd80709", valid: false}, // legacy unnamespaced SHA-1
			// Invalid length
			{input: "da39a3ee5e6b4b0d3255bfef95601890afd8070", valid: false},
			{input: "da39a3ee5e6b4b0d3255bfef95601890afd807091", valid: false},
			// Uppercase rejection (canonical lowercase only)
			{input: "sha256-E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855", valid: false},
			// Invalid characters
			{input: "da39a3ee5e6b4b0d3255bfef95601890afd8070z", valid: false},
			// Slashes and directory traversal rejection
			{input: "da39a3ee5e6b4b0d3255bfef95601890afd807/a", valid: false},
			{input: "da39a3ee5e6b4b0d3255bfef95601890afd807..", valid: false},
		}

		for _, tc := range tests {
			t.Run(tc.input, func(t *testing.T) {
				id, err := content.ParseID(tc.input)
				if tc.valid {
					if err != nil {
						t.Errorf("unexpected error for %q: %v", tc.input, err)
					}
					// Verify round-trip string representation
					if id.String() != tc.input {
						t.Errorf("expected %q, got %q on round-trip", tc.input, id.String())
					}
				} else {
					if err == nil {
						t.Errorf("expected validation failure for %q, but it succeeded", tc.input)
					} else if !errors.Is(err, content.ErrInvalidID) {
						t.Errorf("expected ErrInvalidID, got: %v", err)
					}
				}
			})
		}
	})
}
