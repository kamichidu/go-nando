package paths_test

import (
	"errors"
	"testing"

	"github.com/kamichidu/go-nando/internal/paths"
)

func TestNormalizeLogicalPath(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		err      error
	}{
		{input: "/", expected: "/"},
		{input: "/documents/report.pdf", expected: "/documents/report.pdf"},
		{input: "/documents//archive///report.pdf", expected: "/documents/archive/report.pdf"},
		{input: "/documents/./archive/../report.pdf", expected: "/documents/report.pdf"},
		{input: "/../escaped", err: paths.ErrInvalidPath},
		{input: "/documents/archive/../../escaped", expected: "/escaped"},
		{input: "/documents/archive/../../../escaped", err: paths.ErrInvalidPath},
		{input: "/documents/CaseSensitivity", expected: "/documents/CaseSensitivity"},
		{input: "/documents/literal%", expected: "/documents/literal%"},
		{input: "/documents/literal_", expected: "/documents/literal_"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			actual, err := paths.NormalizeLogicalPath(tc.input)
			if tc.err != nil {
				if err == nil {
					t.Errorf("expected error %v, got nil", tc.err)
				} else if !errors.Is(err, tc.err) {
					t.Errorf("expected error %v, got %v", tc.err, err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if actual != tc.expected {
					t.Errorf("expected %q, got %q", tc.expected, actual)
				}
			}
		})
	}
}
