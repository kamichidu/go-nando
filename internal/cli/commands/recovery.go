package commands

import (
	"context"
	"fmt"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/cli"
	"github.com/kamichidu/go-nando/pkg/controlrpc"
)

// RecoveryCommand inspects and explicitly retries or discards failed write Resources.
type RecoveryCommand struct{}

func (c *RecoveryCommand) Name() string { return "recovery" }

func (c *RecoveryCommand) RegisterFlags(ctx context.Context, _ *flagshim.FlagSet) context.Context {
	return ctx
}

func (c *RecoveryCommand) Run(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("%w: nando recovery <list|retry|discard> [session-id]", cli.ErrInvalidArguments)
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return fmt.Errorf("%w: nando recovery list accepts no session ID", cli.ErrInvalidArguments)
		}
		client, err := cli.NewDaemonClientFromContext(ctx)
		if err != nil {
			return err
		}
		var records []controlrpc.RecoveryRecord
		if err := client.Call(ctx, controlrpc.MethodRecoveryList, struct{}{}, &records); err != nil {
			return err
		}
		out := flagshim.MustStdoutFromContext(ctx)
		for _, record := range records {
			if _, err := fmt.Fprintf(out, "%s\t%s\tentry=%s revision=%d absent=%t size=%d created=%s updated=%s\t%s\n", record.SessionID, record.RepositoryPath, record.ExpectedEntryID, record.ExpectedRevision, record.MustBeAbsent, record.Size, record.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), record.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), record.FailureReason); err != nil {
				return err
			}
		}
		return nil
	case "retry", "discard":
		if len(args) != 2 || args[1] == "" {
			return fmt.Errorf("%w: nando recovery %s <session-id>", cli.ErrInvalidArguments, args[0])
		}
		client, err := cli.NewDaemonClientFromContext(ctx)
		if err != nil {
			return err
		}
		method := controlrpc.MethodRecoveryRetry
		if args[0] == "discard" {
			method = controlrpc.MethodRecoveryDiscard
		}
		return client.Call(ctx, method, map[string]string{"session_id": args[1]}, nil)
	default:
		return fmt.Errorf("%w: unknown recovery operation %q", cli.ErrInvalidArguments, args[0])
	}
}
