package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/app"
	"github.com/kamichidu/go-nando/internal/cli"
	"github.com/kamichidu/go-nando/internal/paths"
)

// MvCommand represents the "nando mv" subcommand.
type MvCommand struct{}

// Name returns the subcommand name.
func (c *MvCommand) Name() string {
	return "mv"
}

// RegisterFlags defines subcommand-specific options.
func (c *MvCommand) RegisterFlags(ctx context.Context, fs *flagshim.FlagSet) context.Context {
	return ctx
}

// Run executes renaming or moving logical path.
func (c *MvCommand) Run(ctx context.Context, args []string) (runErr error) {
	if len(args) < 2 {
		return fmt.Errorf("%w: nando mv <source-path> <destination-path>", cli.ErrInvalidArguments)
	}

	rawSrc := args[0]
	rawDst := args[1]

	src, err := paths.NormalizeLogicalPath(rawSrc)
	if err != nil {
		return fmt.Errorf("%w: source path: %v", cli.ErrInvalidArguments, err)
	}

	dst, err := paths.NormalizeLogicalPath(rawDst)
	if err != nil {
		return fmt.Errorf("%w: destination path: %v", cli.ErrInvalidArguments, err)
	}

	application, _, cleanup, err := cli.NewAppFromContext(ctx)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, cleanup()) }()

	cli.LogVerbose(ctx, "Moving logical path %q to %q", src, dst)

	err = application.Move(ctx, app.MoveInput{
		SourcePath:      src,
		DestinationPath: dst,
	})
	if err != nil {
		return err
	}

	return nil
}
