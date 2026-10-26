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

// GetCommand represents the "nando get" subcommand.
type GetCommand struct{}

// Name returns the subcommand name.
func (c *GetCommand) Name() string {
	return "get"
}

// RegisterFlags defines subcommand-specific options.
func (c *GetCommand) RegisterFlags(ctx context.Context, fs *flagshim.FlagSet) context.Context {
	return ctx
}

// Run executes exporting a logical file from the repository back to local filesystem.
func (c *GetCommand) Run(ctx context.Context, args []string) (runErr error) {
	if len(args) < 2 {
		return fmt.Errorf("%w: nando get <repository-path> <local-file>", cli.ErrInvalidArguments)
	}

	rawRepoPath := args[0]
	localPath := args[1]

	repoPath, err := paths.NormalizeLogicalPath(rawRepoPath)
	if err != nil {
		return fmt.Errorf("%w: %v", cli.ErrInvalidArguments, err)
	}

	application, _, cleanup, err := cli.NewAppFromContext(ctx)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, cleanup()) }()

	cli.LogVerbose(ctx, "Exporting logical repository path %q to local file %q", repoPath, localPath)

	err = application.Get(ctx, app.GetInput{
		RepositoryPath: repoPath,
		LocalPath:      localPath,
	})
	if err != nil {
		return err
	}

	return nil
}
