package content_test

import (
	"context"
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

		if h1 != h2 {
			t.Errorf("expected same hash, got %q and %q", h1, h2)
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

		if h1 == h2 {
			t.Errorf("expected different hash, but both are %q", h1)
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
			t.Errorf("expected %q, got %q", expected, h)
		}
	})
}
