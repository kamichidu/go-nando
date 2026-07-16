package commands_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/cli"
	"github.com/kamichidu/go-nando/internal/cli/commands"
)

func executeCLI(ctx context.Context, stdout, stderr *bytes.Buffer, args []string) int {
	subs := []flagshim.Command{
		&commands.InitCommand{},
		&commands.PutCommand{},
		&commands.GetCommand{},
		&commands.CatCommand{},
		&commands.LsCommand{},
		&commands.StatCommand{},
		&commands.MvCommand{},
		&commands.RmCommand{},
		&commands.GcCommand{},
		&commands.SyncCommand{},
		&commands.DoctorCommand{},
	}
	root := cli.NewRootCommand(subs)

	// Prepend "nando" as argv[0] which flagshim.Run treats as program name and ignores.
	argv := append([]string{"nando"}, args...)

	return flagshim.Run(
		ctx,
		bytes.NewReader(nil),
		stdout,
		stderr,
		root,
		argv,
	)
}

func TestCLI(t *testing.T) {
	ctx := context.Background()

	t.Run("invalid arguments returns exit code 2", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := executeCLI(ctx, &stdout, &stderr, []string{"put"}) // missing both arguments
		if code != 2 {
			t.Errorf("expected exit code 2, got %d. Stderr: %q", code, stderr.String())
		}
	})

	t.Run("non-existent path returns exit code 3", func(t *testing.T) {
		tempRepo := t.TempDir()
		var stdout, stderr bytes.Buffer

		// Initialize first
		initCode := executeCLI(ctx, &stdout, &stderr, []string{"--repository", tempRepo, "init"})
		if initCode != 0 {
			t.Fatalf("failed to init repo: %s", stderr.String())
		}

		stdout.Reset()
		stderr.Reset()

		// Query stat of non-existent path
		code := executeCLI(ctx, &stdout, &stderr, []string{"--repository", tempRepo, "stat", "/missing.txt"})
		if code != 3 {
			t.Errorf("expected exit code 3 for non-existent path, got %d. Stderr: %q", code, stderr.String())
		}
	})

	t.Run("--repository flag is reflected correctly", func(t *testing.T) {
		tempRepo := t.TempDir()
		var stdout, stderr bytes.Buffer

		code := executeCLI(ctx, &stdout, &stderr, []string{"--repository", tempRepo, "init"})
		if code != 0 {
			t.Fatalf("failed to init repository with custom --repository: %s", stderr.String())
		}

		dbPath := filepath.Join(tempRepo, "metadata.sqlite")
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			t.Errorf("expected metadata.sqlite to exist at %s, but it was not found", dbPath)
		}
	})
}
