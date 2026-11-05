package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kamichidu/go-flagshim"
)

func TestExternalSubcommandUsesExplicitDirectoryAndReturnsStatus(t *testing.T) {
	dir := t.TempDir()
	adapter := filepath.Join(dir, "nando-demo")
	if err := os.WriteFile(adapter, []byte("#!/bin/sh\nprintf '%s' \"$1\"\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NANDO_LIBEXEC_DIR", dir)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = writer
	err = RunExternalSubcommand([]string{"demo", "argument"})
	os.Stdout = previous
	if closeErr := writer.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	output, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if closeErr := reader.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if string(output) != "argument" {
		t.Fatalf("adapter stdout=%q", output)
	}
	status, ok := err.(externalExitError)
	if !ok || status.code != 7 {
		t.Fatalf("external status err=%v", err)
	}
}

func TestExternalSubcommandFindsSiblingAdapterForGoInstallLayout(t *testing.T) {
	binDir := t.TempDir()
	adapter := filepath.Join(binDir, "nando-demo")
	if err := os.WriteFile(adapter, []byte("#!/bin/sh\nprintf sibling\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := runExternalSubcommandInDirs([]string{"demo"}, []string{binDir}, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "sibling" {
		t.Fatalf("adapter stdout=%q", output.String())
	}
}

func TestExternalSubcommandFindsDebianLibexecLayout(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	libexecDir := filepath.Join(root, "libexec")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(libexecDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libexecDir, "nando-demo"), []byte("#!/bin/sh\nprintf deb\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := runExternalSubcommandInDirs([]string{"demo"}, []string{binDir, filepath.Join(binDir, "..", "libexec")}, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "deb" {
		t.Fatalf("adapter stdout=%q", output.String())
	}
}

func TestExternalSubcommandRejectsPathAndDoesNotUsePathSearch(t *testing.T) {
	if err := RunExternalSubcommand([]string{"../demo"}); err == nil {
		t.Fatal("path-like command accepted")
	}
	pathDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pathDir, "nando-demo"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)
	t.Setenv("NANDO_LIBEXEC_DIR", filepath.Join(t.TempDir(), "empty"))
	if err := RunExternalSubcommand([]string{"demo"}); err == nil {
		t.Fatal("PATH adapter was unexpectedly resolved")
	}
}

func TestBuiltInCommandTakesPriorityOverAdapter(t *testing.T) {
	root := NewRootCommand([]flagshim.Command{namedCommand("fuse"), namedCommand("daemon")})
	if root.IsExternalSubcommand([]string{"fuse"}) {
		t.Fatal("built-in command treated as external")
	}
	if !root.IsExternalSubcommand([]string{"webdav"}) {
		t.Fatal("unknown adapter command not recognized")
	}
	if root.IsExternalSubcommand([]string{"--repository", "/tmp/repo", "daemon"}) {
		t.Fatal("built-in after root flags treated as external")
	}
	if !root.IsExternalSubcommand([]string{"--repository", "/tmp/repo", "webdav", "--webdav-listen", "127.0.0.1:8080"}) {
		t.Fatal("adapter after root flags not recognized")
	}
}

type namedCommand string

func (c namedCommand) Name() string { return string(c) }
func (namedCommand) RegisterFlags(ctx context.Context, _ *flagshim.FlagSet) context.Context {
	return ctx
}
func (namedCommand) Run(context.Context, []string) error { return nil }
