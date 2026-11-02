package main

import (
	"context"
	"strings"
	"testing"
)

func TestRunUsesExplicitSharedConfigAndRejectsMissingMountpoint(t *testing.T) {
	err := (&command{}).Run(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "--fuse-mountpoint is required") {
		t.Fatalf("missing mountpoint error=%v", err)
	}
	err = (&command{options: flags{Mountpoint: t.TempDir(), Config: "/definitely/missing/nando-config.toml"}}).Run(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "load configuration") {
		t.Fatalf("explicit missing config error=%v", err)
	}
}
