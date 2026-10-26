package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/cli"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/paths"
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
func (c *StatCommand) Run(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("%w: nando stat <repository-path>", cli.ErrInvalidArguments)
	}

	rawRepoPath := args[0]
	repoPath, err := paths.NormalizeLogicalPath(rawRepoPath)
	if err != nil {
		return fmt.Errorf("%w: %v", cli.ErrInvalidArguments, err)
	}

	application, _, cleanup, err := cli.NewAppFromContext(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	cli.LogVerbose(ctx, "Retrieving stat for %q", repoPath)

	entry, err := application.Stat(ctx, repoPath)
	if err != nil {
		return err
	}

	canonicalPath, err := application.DeriveCanonicalPath(ctx, entry)
	if err != nil {
		return fmt.Errorf("failed to derive canonical path: %w", err)
	}

	stdout := flagshim.MustStdoutFromContext(ctx)
	fmt.Fprintf(stdout, "Path:         %s\n", canonicalPath)
	if entry.Kind == metadata.EntryKindFile {
		if entry.ObjectID != nil {
			fmt.Fprintf(stdout, "Hash:         %s\n", entry.ObjectID.String())
		} else {
			fmt.Fprintf(stdout, "Hash:         <nil>\n")
		}
	}
	fmt.Fprintf(stdout, "Size:         %d bytes\n", entry.Size)
	fmt.Fprintf(stdout, "Mtime:        %s\n", entry.Mtime.Time().Format(time.RFC3339))
	fmt.Fprintf(stdout, "Mode:         %o\n", entry.Mode)
	fmt.Fprintf(stdout, "Content-Type: %s\n", entry.ContentType)
	fmt.Fprintf(stdout, "Revision:     %d\n", entry.Revision)
	fmt.Fprintf(stdout, "Kind:         %s\n", string(entry.Kind))
	fmt.Fprintf(stdout, "Created At:   %s\n", entry.CreatedAt.Time().Format(time.RFC3339))
	fmt.Fprintf(stdout, "Updated At:   %s\n", entry.UpdatedAt.Time().Format(time.RFC3339))

	return nil
}
