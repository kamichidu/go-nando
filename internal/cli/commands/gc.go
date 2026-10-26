package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/cli"
)

// GcCommand represents the "nando gc" subcommand.
type GcCommand struct{}

// Name returns the subcommand name.
func (c *GcCommand) Name() string {
	return "gc"
}

// RegisterFlags defines subcommand-specific options.
func (c *GcCommand) RegisterFlags(ctx context.Context, fs *flagshim.FlagSet) context.Context {
	return ctx
}

// Run executes garbage collection on the repository.
func (c *GcCommand) Run(ctx context.Context, args []string) (runErr error) {
	application, _, cleanup, err := cli.NewAppFromContext(ctx)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, cleanup()) }()

	cli.LogVerbose(ctx, "Running garbage collection...")

	count, err := application.GC(ctx)
	if err != nil {
		return err
	}

	stdout := flagshim.MustStdoutFromContext(ctx)
	fmt.Fprintf(stdout, "Garbage collection completed. Removed %d orphan objects.\n", count)
	return nil
}
