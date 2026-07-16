package commands

import (
	"context"
	"fmt"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/app"
	"github.com/kamichidu/go-nando/internal/cli"
	"github.com/kamichidu/go-nando/internal/paths"
)

// PutCommand represents the "nando put" subcommand.
type PutCommand struct{}

// Name returns the subcommand name.
func (c *PutCommand) Name() string {
	return "put"
}

// RegisterFlags defines subcommand-specific options. None for put.
func (c *PutCommand) RegisterFlags(ctx context.Context, fs *flagshim.FlagSet) context.Context {
	return ctx
}

// Run executes importing a local file into the logical repository.
func (c *PutCommand) Run(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("%w: nando put <local-file> <repository-path>", cli.ErrInvalidArguments)
	}

	localPath := args[0]
	rawRepoPath := args[1]

	repoPath, err := paths.NormalizeLogicalPath(rawRepoPath)
	if err != nil {
		return fmt.Errorf("%w: %v", cli.ErrInvalidArguments, err)
	}

	application, _, cleanup, err := cli.NewAppFromContext(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	cli.LogVerbose(ctx, "Importing file %q to logical repository path %q", localPath, repoPath)

	err = application.Put(ctx, app.PutInput{
		LocalPath:      localPath,
		RepositoryPath: repoPath,
	})
	if err != nil {
		return err
	}

	return nil
}
