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

		expected := "da39a3ee5e6b4b0d3255bfef95601890afd80709" // SHA-1 of empty string
		if h.String() != expected {
			t.Errorf("expected %q, got %q", expected, h.String())
		}
	})

	t.Run("Content ID parsing and validation", func(t *testing.T) {
		tests := []struct {
			input string
			valid bool
		}{
			{input: "da39a3ee5e6b4b0d3255bfef95601890afd80709", valid: true},
			{input: "6640ec4d43fe9d18fcfee38caab6c91e3f231e17", valid: true},
			// Invalid length
			{input: "da39a3ee5e6b4b0d3255bfef95601890afd8070", valid: false},
			{input: "da39a3ee5e6b4b0d3255bfef95601890afd807091", valid: false},
			// Uppercase rejection (canonical lowercase only)
			{input: "DA39A3EE5E6B4B0D3255BFEF95601890AFD80709", valid: false},
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
