package format_test

import (
	"errors"
	"testing"

	repositoryformat "github.com/kamichidu/go-nando/internal/repository/format"
)

func TestFormatValidate(t *testing.T) {
	if err := repositoryformat.Current().Validate(); err != nil {
		t.Fatalf("current format should be supported: %v", err)
	}

	unsupported := repositoryformat.Current()
	unsupported.ContentAlgorithm = "sha1"
	if err := unsupported.Validate(); !errors.Is(err, repositoryformat.ErrUnsupported) {
		t.Fatalf("expected unsupported algorithm to be rejected, got %v", err)
	}
}
