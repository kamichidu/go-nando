package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/cli"
)

// RemoteOrphansCommand restores Metadata for remote objects whose references
// are missing. Remote objects themselves are never modified.
type RemoteOrphansCommand struct{}

func (c *RemoteOrphansCommand) Name() string { return "admin" }

func (c *RemoteOrphansCommand) RegisterFlags(ctx context.Context, _ *flagshim.FlagSet) context.Context {
	return ctx
}

func (c *RemoteOrphansCommand) Run(ctx context.Context, args []string) (runErr error) {
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
	recovered, err := application.RestoreRemoteOrphansTo(ctx, cfg.RemoteRecoveryDirectory())
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(flagshim.MustStdoutFromContext(ctx), "Recovered %d remote object metadata entr%s under %s. Remote objects were left unchanged.\n", recovered, map[bool]string{true: "y", false: "ies"}[recovered == 1], cfg.RemoteRecoveryDirectory())
	return err
}
