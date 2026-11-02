package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/cli"
)

// GCSOrphansCommand restores Metadata for GCS objects whose references are
// missing. The GCS objects themselves are never modified.
type GCSOrphansCommand struct{}

func (c *GCSOrphansCommand) Name() string { return "admin" }

func (c *GCSOrphansCommand) RegisterFlags(ctx context.Context, _ *flagshim.FlagSet) context.Context {
	return ctx
}

func (c *GCSOrphansCommand) Run(ctx context.Context, args []string) (runErr error) {
	if len(args) != 1 || args[0] != "recover" {
		return fmt.Errorf("%w: nando admin recover", cli.ErrInvalidArguments)
	}
	cfg, err := cli.ResolveConfigFromContext(ctx)
	if err != nil {
		return err
	}
	application, _, cleanup, err := cli.NewAppFromContext(ctx)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, cleanup()) }()
	recovered, err := application.RestoreGCSOrphansTo(ctx, cfg.GCSRecoveryDirectory())
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(flagshim.MustStdoutFromContext(ctx), "Recovered %d GCS object metadata entr%s under %s. GCS objects were left unchanged.\n", recovered, map[bool]string{true: "y", false: "ies"}[recovered == 1], cfg.GCSRecoveryDirectory())
	return err
}
