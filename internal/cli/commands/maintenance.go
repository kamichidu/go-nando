package commands

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/controlrpc"
	"github.com/kamichidu/go-nando/internal/cli"
)

// MaintenanceCommand runs the repository's single periodic maintenance job.
type MaintenanceCommand struct{ interval time.Duration }

func (c *MaintenanceCommand) Name() string { return "maintenance" }

func (c *MaintenanceCommand) RegisterFlags(ctx context.Context, fs *flagshim.FlagSet) context.Context {
	fs.DurationVar(&c.interval, "interval", 0, "required interval between maintenance runs")
	return ctx
}

func (c *MaintenanceCommand) Run(ctx context.Context, args []string) (runErr error) {
	if len(args) != 0 || c.interval <= 0 {
		return fmt.Errorf("%w: nando maintenance --interval <duration>", cli.ErrInvalidArguments)
	}
	client, err := cli.NewDaemonClientFromContext(ctx)
	if err != nil {
		return err
	}
	stderr := flagshim.MustStderrFromContext(ctx)
	if err := client.Call(ctx, controlrpc.MethodMaintenance, struct{}{}, nil); err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "[maintenance] %v\n", err); writeErr != nil {
			return errors.Join(err, writeErr)
		}
	}
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := client.Call(ctx, controlrpc.MethodMaintenance, struct{}{}, nil); err != nil {
				if _, writeErr := fmt.Fprintf(stderr, "[maintenance] %v\n", err); writeErr != nil {
					return errors.Join(err, writeErr)
				}
			}
		}
	}
}
