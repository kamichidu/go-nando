package commands

import (
	"context"
	"errors"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/cli"
)

// SyncCommand represents the "nando sync" subcommand.
type SyncCommand struct{}

// Name returns the subcommand name.
func (c *SyncCommand) Name() string {
	return "sync"
}

// RegisterFlags defines subcommand-specific options.
func (c *SyncCommand) RegisterFlags(ctx context.Context, fs *flagshim.FlagSet) context.Context {
	return ctx
}

// Run executes sync use case (currently planned and returns not implemented).
func (c *SyncCommand) Run(ctx context.Context, args []string) (runErr error) {
	application, _, cleanup, err := cli.NewAppFromContext(ctx)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, cleanup()) }()

	cli.LogVerbose(ctx, "Starting repository synchronization (currently unimplemented)...")

	return application.Sync(ctx)
}
