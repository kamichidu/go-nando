package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/cli"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/paths"
	"github.com/kamichidu/go-nando/pkg/controlrpc"
)

// StatCommand represents the "nando stat" subcommand.
type StatCommand struct{}

// Name returns the subcommand name.
func (c *StatCommand) Name() string {
	return "stat"
}

// RegisterFlags defines subcommand-specific options.
func (c *StatCommand) RegisterFlags(ctx context.Context, fs *flagshim.FlagSet) context.Context {
	return ctx
}

// Run executes retrieving details of the repository path and outputs it.
func (c *StatCommand) Run(ctx context.Context, args []string) (runErr error) {
	if len(args) < 1 {
		return fmt.Errorf("%w: nando stat <repository-path>", cli.ErrInvalidArguments)
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

	cli.LogVerbose(ctx, "Retrieving stat for %q", repoPath)

	var entry controlrpc.Entry
	if err := client.Call(ctx, controlrpc.MethodStat, map[string]string{"path": repoPath}, &entry); err != nil {
		return err
	}

	stdout := flagshim.MustStdoutFromContext(ctx)
	fmt.Fprintf(stdout, "Path:         %s\n", repoPath)
	if entry.Kind == string(metadata.EntryKindFile) {
		if entry.ObjectID != "" {
			fmt.Fprintf(stdout, "Hash:         %s\n", entry.ObjectID)
		} else {
			fmt.Fprintf(stdout, "Hash:         <nil>\n")
		}
	}
	fmt.Fprintf(stdout, "Size:         %d bytes\n", entry.Size)
	fmt.Fprintf(stdout, "Mtime:        %s\n", time.Unix(0, entry.Mtime).UTC().Format(time.RFC3339))
	fmt.Fprintf(stdout, "Mode:         %o\n", entry.Mode)
	fmt.Fprintf(stdout, "Content-Type: %s\n", entry.ContentType)
	fmt.Fprintf(stdout, "Revision:     %d\n", entry.Revision)
	fmt.Fprintf(stdout, "Kind:         %s\n", string(entry.Kind))
	fmt.Fprintf(stdout, "Created At:   %s\n", time.Unix(0, entry.CreatedAt).UTC().Format(time.RFC3339))
	fmt.Fprintf(stdout, "Updated At:   %s\n", time.Unix(0, entry.UpdatedAt).UTC().Format(time.RFC3339))

	return nil
}
