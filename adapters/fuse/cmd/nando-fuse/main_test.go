package main

import (
	"io"
	"strings"
	"testing"
)

func TestRunUsesExplicitSharedConfigAndRejectsMissingMountpoint(t *testing.T) {
	err := runWithOutput([]string{"-config", "/definitely/missing/nando-config.toml"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "-mountpoint is required") {
		t.Fatalf("missing mountpoint error=%v", err)
	}
	err = runWithOutput([]string{"-mountpoint", t.TempDir(), "-config", "/definitely/missing/nando-config.toml"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "load configuration") {
		t.Fatalf("explicit missing config error=%v", err)
	}
}
