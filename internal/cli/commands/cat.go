package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/cli"
	"github.com/kamichidu/go-nando/internal/paths"
)

// CatCommand represents the "nando cat" subcommand.
type CatCommand struct{}

// Name returns the subcommand name.
func (c *CatCommand) Name() string {
	return "cat"
}

// RegisterFlags defines subcommand-specific options.
func (c *CatCommand) RegisterFlags(ctx context.Context, fs *flagshim.FlagSet) context.Context {
	return ctx
}

// Run executes streaming a logical file from the repository to stdout.
func (c *CatCommand) Run(ctx context.Context, args []string) (runErr error) {
	if len(args) < 1 {
		return fmt.Errorf("%w: nando cat <repository-path>", cli.ErrInvalidArguments)
	}

	rawRepoPath := args[0]
	repoPath, err := paths.NormalizeLogicalPath(rawRepoPath)
	if err != nil {
		return fmt.Errorf("%w: %v", cli.ErrInvalidArguments, err)
	}

	client, err := cli.NewDaemonClientFromContext(ctx)
	if err != nil {
		return err
	}

	cli.LogVerbose(ctx, "Streaming content of logical path %q to stdout", repoPath)

	stdout := flagshim.MustStdoutFromContext(ctx)
	read, err := beginReadResource(ctx, client, repoPath)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, releaseResource(client, read.SessionID)) }()
	u, err := url.Parse(read.ResourceURI)
	if err != nil || u.Scheme != "file" || u.Host != "" || u.Path == "" {
		return fmt.Errorf("daemon returned unsupported or inaccessible Resource URI %q", read.ResourceURI)
	}
	file, err := os.Open(u.Path)
	if err != nil {
		return fmt.Errorf("open daemon read Resource: %w", err)
	}
	_, copyErr := io.Copy(stdout, contextReader{ctx: ctx, reader: file})
	return errors.Join(copyErr, file.Close())
}
