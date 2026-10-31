package main

import (
	"context"
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
		&commands.MaintenanceCommand{},
		&commands.SyncCommand{},
		&commands.DaemonCommand{},
		&commands.DoctorCommand{},
	}

	// 2. Instantiate Root Command
	root := cli.NewRootCommand(subs)

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
