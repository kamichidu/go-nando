package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/cli"
	"github.com/kamichidu/go-nando/internal/paths"
)

// RmCommand represents the "nando rm" subcommand.
type RmCommand struct{}

// Name returns the subcommand name.
func (c *RmCommand) Name() string {
	return "rm"
}

// RegisterFlags defines subcommand-specific options.
func (c *RmCommand) RegisterFlags(ctx context.Context, fs *flagshim.FlagSet) context.Context {
	return ctx
}

// Run executes removing (soft delete) logical path from metadata.
func (c *RmCommand) Run(ctx context.Context, args []string) (runErr error) {
	if len(args) < 1 {
		return fmt.Errorf("%w: nando rm <repository-path>", cli.ErrInvalidArguments)
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

	cli.LogVerbose(ctx, "Soft deleting logical path %q", repoPath)

	err = application.Remove(ctx, repoPath)
	if err != nil {
		return err
	}

	return nil
}
