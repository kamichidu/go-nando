package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/cli"
	"github.com/kamichidu/go-nando/internal/cli/commands"
)

func main() {
	// 1. Build child commands
	subs := []flagshim.Command{
		&commands.InitCommand{},
		&commands.PutCommand{},
		&commands.GetCommand{},
		&commands.CatCommand{},
		&commands.LsCommand{},
		&commands.StatCommand{},
		&commands.MvCommand{},
		&commands.RmCommand{},
		&commands.GcCommand{},
		&commands.RemoteOrphansCommand{},
		&commands.MaintenanceCommand{},
		&commands.SyncCommand{},
		&commands.DaemonCommand{},
		&commands.DoctorCommand{},
		&commands.RecoveryCommand{},
	}

	// 2. Instantiate Root Command
	root := cli.NewRootCommand(subs)
	if root.IsExternalSubcommand(os.Args[1:]) {
		if err := cli.RunExternalSubcommand(os.Args[1:]); err != nil {
			if status, ok := cli.ExternalExitCode(err); ok {
				os.Exit(status)
			}
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	// 3. Run flagshim execution loop
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	exitCode := flagshim.Run(
		ctx,
		os.Stdin,
		os.Stdout,
		os.Stderr,
		root,
		os.Args,
	)

	os.Exit(exitCode)
}
