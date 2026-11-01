package commands

import (
	"context"
	"fmt"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/controlrpc"
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
	client, err := cli.NewDaemonClientFromContext(ctx)
	if err != nil {
		return err
	}

	cli.LogVerbose(ctx, "Running garbage collection...")

	var result struct {
		Removed int `json:"removed"`
	}
	if err := client.Call(ctx, controlrpc.MethodGC, struct{}{}, &result); err != nil {
		return err
	}

	stdout := flagshim.MustStdoutFromContext(ctx)
	fmt.Fprintf(stdout, "Garbage collection completed. Removed %d orphan objects.\n", result.Removed)
	return nil
}
