package commands

import (
	"context"
	"fmt"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/cli"
)

// DoctorCommand represents the "nando doctor" subcommand.
type DoctorCommand struct{}

// Name returns the subcommand name.
func (c *DoctorCommand) Name() string {
	return "doctor"
}

// RegisterFlags defines subcommand-specific options.
func (c *DoctorCommand) RegisterFlags(ctx context.Context, fs *flagshim.FlagSet) context.Context {
	return ctx
}

// Run executes repository integrity checks.
func (c *DoctorCommand) Run(ctx context.Context, args []string) error {
	application, _, cleanup, err := cli.NewAppFromContext(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	cli.LogVerbose(ctx, "Starting repository verification...")

	if err := application.Doctor(ctx); err != nil {
		return err
	}

	stdout := flagshim.MustStdoutFromContext(ctx)
	fmt.Fprintln(stdout, "Repository is healthy. No corruption detected.")
	return nil
}
