package commands

import (
	"context"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/cli"
	"github.com/kamichidu/go-nando/pkg/controlrpc"
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
	client, err := cli.NewDaemonClientFromContext(ctx)
	if err != nil {
		return err
	}

	cli.LogVerbose(ctx, "Starting repository synchronization (currently unimplemented)...")

	return client.Call(ctx, controlrpc.MethodSync, struct{}{}, nil)
}
