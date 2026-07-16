package commands

import (
	"context"
	"fmt"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/app"
	"github.com/kamichidu/go-nando/internal/cli"
)

// InitCommand represents the "nando init" subcommand.
type InitCommand struct{}

// Name returns the subcommand name.
func (c *InitCommand) Name() string {
	return "init"
}

// RegisterFlags binds flags. None needed for init.
func (c *InitCommand) RegisterFlags(ctx context.Context, fs *flagshim.FlagSet) context.Context {
	return ctx
}

// Run executes the initialization use case.
func (c *InitCommand) Run(ctx context.Context, args []string) error {
	repoDir, _, err := cli.ResolveRepositoryAndVerbose(ctx)
	if err != nil {
		return err
	}

	cli.LogVerbose(ctx, "Starting repository initialization...")
	cli.LogVerbose(ctx, "Target directory: %s", repoDir)

	if err := app.InitRepository(ctx, repoDir); err != nil {
		return err
	}

	out := flagshim.MustStdoutFromContext(ctx)
	fmt.Fprintf(out, "Initialized empty nando repository in %s\n", repoDir)
	return nil
}
