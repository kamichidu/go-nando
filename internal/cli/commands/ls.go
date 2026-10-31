package commands

import (
	"context"
	"fmt"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/cli"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/paths"
	"github.com/kamichidu/go-nando/pkg/controlrpc"
)

// LsCommand represents the "nando ls" subcommand.
type LsCommand struct{}

// Name returns the subcommand name.
func (c *LsCommand) Name() string {
	return "ls"
}

// RegisterFlags defines subcommand-specific options.
func (c *LsCommand) RegisterFlags(ctx context.Context, fs *flagshim.FlagSet) context.Context {
	return ctx
}

// Run executes the list command to display child entries under the path.
func (c *LsCommand) Run(ctx context.Context, args []string) (runErr error) {
	rawRepoPath := "/"
	if len(args) > 0 {
		rawRepoPath = args[0]
	}

	repoPath, err := paths.NormalizeLogicalPath(rawRepoPath)
	if err != nil {
		return fmt.Errorf("%w: %v", cli.ErrInvalidArguments, err)
	}

	client, err := cli.NewDaemonClientFromContext(ctx)
	if err != nil {
		return err
	}

	cli.LogVerbose(ctx, "Listing entries under logical path %q", repoPath)

	var entries []controlrpc.Entry
	if err := client.Call(ctx, controlrpc.MethodList, map[string]string{"path": repoPath}, &entries); err != nil {
		return err
	}

	stdout := flagshim.MustStdoutFromContext(ctx)
	for _, entry := range entries {
		name := entry.Name
		if entry.Kind == string(metadata.EntryKindDirectory) {
			name += "/"
		}
		fmt.Fprintln(stdout, name)
	}

	return nil
}
