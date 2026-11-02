package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/cli"
	"github.com/kamichidu/go-nando/internal/content"
)

// GCSOrphansCommand is the only CLI workflow that can delete remote orphan objects.
type GCSOrphansCommand struct {
	deleteID   string
	generation int64
	dryRun     bool
}

func (c *GCSOrphansCommand) Name() string { return "gcs-orphans" }

func (c *GCSOrphansCommand) RegisterFlags(ctx context.Context, fs *flagshim.FlagSet) context.Context {
	fs.StringVar(&c.deleteID, "delete", "", "explicitly delete this candidate Content ID")
	fs.Int64Var(&c.generation, "generation", 0, "observed GCS generation required with --delete")
	fs.BoolVar(&c.dryRun, "dry-run", false, "list candidates without deleting (the default)")
	return ctx
}

func (c *GCSOrphansCommand) Run(ctx context.Context, args []string) (runErr error) {
	if len(args) != 0 || (c.deleteID == "" && c.generation != 0) || (c.deleteID != "" && c.generation <= 0) {
		return fmt.Errorf("%w: nando gcs-orphans [--dry-run] or --delete <content-id> --generation <generation>", cli.ErrInvalidArguments)
	}
	application, _, cleanup, err := cli.NewAppFromContext(ctx)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, cleanup()) }()
	candidates, err := application.GCSOrphanCandidates(ctx)
	if err != nil {
		return err
	}
	stdout := flagshim.MustStdoutFromContext(ctx)
	if c.deleteID != "" {
		id, err := content.ParseID(c.deleteID)
		if err != nil {
			return fmt.Errorf("invalid Content ID: %w", err)
		}
		found := false
		for _, candidate := range candidates {
			if candidate.ID == id && candidate.Generation == c.generation {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("candidate %s generation %d is absent from the current verified dry-run", id, c.generation)
		}
		if c.dryRun {
			_, err := fmt.Fprintf(stdout, "Dry run: would delete GCS object %s generation %d.\n", id, c.generation)
			return err
		}
		if err := application.DeleteGCSOrphan(ctx, id, c.generation); err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "Deleted GCS object %s generation %d.\n", id, c.generation)
		return err
	}
	if len(candidates) == 0 {
		_, err := fmt.Fprintln(stdout, "No GCS orphan candidates.")
		return err
	}
	for _, candidate := range candidates {
		if _, err := fmt.Fprintf(stdout, "candidate content_id=%s generation=%d size=%d\n", candidate.ID, candidate.Generation, candidate.Size); err != nil {
			return err
		}
	}
	return nil
}
