package commands

import (
	"context"
	"fmt"
	"path"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/cli"
	"github.com/kamichidu/go-nando/internal/paths"
)

// LsCommand represents the "nando ls" subcommand.
type LsCommand struct{}

// Name returns the subcommand name.
func (c *LsCommand) Name() string {
	return "ls"
}

// RegisterFlags defines subcommand-specific options.
func (c *LsCommand) RegisterFlags(ctx context.Context, fs *flagshim.FlagSet) context.Context {
	return ctx
}

// Run executes the list command to display child entries under the path.
func (c *LsCommand) Run(ctx context.Context, args []string) error {
	rawRepoPath := "/"
	if len(args) > 0 {
		rawRepoPath = args[0]
	}

	repoPath, err := paths.NormalizeLogicalPath(rawRepoPath)
	if err != nil {
		return fmt.Errorf("%w: %v", cli.ErrInvalidArguments, err)
	}

	application, _, cleanup, err := cli.NewAppFromContext(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	cli.LogVerbose(ctx, "Listing entries under logical path %q", repoPath)

	entries, err := application.List(ctx, repoPath)
	if err != nil {
		return err
	}

	stdout := flagshim.MustStdoutFromContext(ctx)
	for _, entry := range entries {
		name := path.Base(entry.Path)
		if entry.IsDir {
			name += "/"
		}
		fmt.Fprintln(stdout, name)
	}

	return nil
}
