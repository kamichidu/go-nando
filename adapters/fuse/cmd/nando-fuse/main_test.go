package main

import (
	"context"
	"flag"
	"reflect"
	"strings"
	"testing"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/kamichidu/go-flagshim"
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

func TestFuseMountOptionFlagAndPrecedence(t *testing.T) {
	c := &command{}
	fs := flagshim.NewFlagSet("nando-fuse", flag.ContinueOnError)
	c.RegisterFlags(context.Background(), fs)
	if err := fs.Parse([]string{"--fuse-mount-option", "allow_other", "--fuse-mount-option", "default_permissions"}); err != nil {
		t.Fatal(err)
	}
	wantCLI := []string{"allow_other", "default_permissions"}
	if !reflect.DeepEqual(c.options.MountOptions, wantCLI) {
		t.Fatalf("CLI options=%q, want %q", c.options.MountOptions, wantCLI)
	}
	if got := resolveMountOptions([]string{"ro"}, c.options.MountOptions); !reflect.DeepEqual(got, wantCLI) {
		t.Fatalf("resolved options=%q, want CLI options %q", got, wantCLI)
	}
	if got := resolveMountOptions([]string{"allow_other"}, nil); !reflect.DeepEqual(got, []string{"allow_other"}) {
		t.Fatalf("configured options=%q", got)
	}
	var mountOptions fuse.MountOptions
	mountOptions.Options = resolveMountOptions(nil, c.options.MountOptions)
	if !reflect.DeepEqual(mountOptions.Options, wantCLI) {
		t.Fatalf("go-fuse mount options=%q, want %q", mountOptions.Options, wantCLI)
	}
}

func TestValidateFuseMountOptions(t *testing.T) {
	if err := validateMountOptions([]string{"allow_other", "default_permissions"}); err != nil {
		t.Fatalf("supported options rejected: %v", err)
	}
	for _, option := range []string{"ro", "fsname=nando", "allow_other=true", ""} {
		err := validateMountOptions([]string{option})
		if err == nil || !strings.Contains(err.Error(), "unsupported FUSE mount option") {
			t.Errorf("option %q error=%v, want clear unsupported-option error", option, err)
		}
	}
}
