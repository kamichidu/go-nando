package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/cli"
	"github.com/kamichidu/go-nando/internal/paths"
)

// CatCommand represents the "nando cat" subcommand.
type CatCommand struct{}

// Name returns the subcommand name.
func (c *CatCommand) Name() string {
	return "cat"
}

// RegisterFlags defines subcommand-specific options.
func (c *CatCommand) RegisterFlags(ctx context.Context, fs *flagshim.FlagSet) context.Context {
	return ctx
}

// Run executes streaming a logical file from the repository to stdout.
func (c *CatCommand) Run(ctx context.Context, args []string) (runErr error) {
	if len(args) < 1 {
		return fmt.Errorf("%w: nando cat <repository-path>", cli.ErrInvalidArguments)
	}

	rawRepoPath := args[0]
	repoPath, err := paths.NormalizeLogicalPath(rawRepoPath)
	if err != nil {
		return fmt.Errorf("%w: %v", cli.ErrInvalidArguments, err)
	}

	application, _, cleanup, err := cli.NewAppFromContext(ctx)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, cleanup()) }()

	cli.LogVerbose(ctx, "Streaming content of logical path %q to stdout", repoPath)

	stdout := flagshim.MustStdoutFromContext(ctx)
	err = application.Cat(ctx, repoPath, stdout)
	if err != nil {
		return err
	}

	return nil
}
