package commands

import (
	"context"
	"fmt"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/app"
	"github.com/kamichidu/go-nando/internal/cli"
	"github.com/kamichidu/go-nando/internal/repository"
)

// InitCommand represents the "nando init" subcommand.
type InitCommand struct {
	deliveryMode  string
	remoteBackend string
	remoteURI     string
	maxLocalBytes int64
}

// Name returns the subcommand name.
func (c *InitCommand) Name() string {
	return "init"
}

// RegisterFlags binds repository-owned delivery settings for a new repository.
func (c *InitCommand) RegisterFlags(ctx context.Context, fs *flagshim.FlagSet) context.Context {
	fs.StringVar(&c.deliveryMode, "delivery-mode", "local", "repository delivery mode (local or remote)")
	fs.StringVar(&c.remoteBackend, "remote-backend", "gcs", "selected remote object storage backend (gcs or s3)")
	fs.StringVar(&c.remoteURI, "remote-uri", "", "remote URI (gs://bucket[/prefix] or s3://bucket[/prefix])")
	fs.Int64Var(&c.maxLocalBytes, "max-local-bytes", 0, "maximum Local object bytes before Capacity GC (0 disables capacity eviction)")
	return ctx
}

// Run executes the initialization use case.
func (c *InitCommand) Run(ctx context.Context, args []string) error {
	repoDir, _, err := cli.ResolveRepositoryAndVerbose(ctx)
	if err != nil {
		return err
	}

	cli.LogVerbose(ctx, "Starting repository initialization...")
	cli.LogVerbose(ctx, "Target directory: %s", repoDir)

	settings := repository.DefaultSettings()
	settings.DeliveryMode = repository.DeliveryMode(c.deliveryMode)
	settings.RemoteBackend = c.remoteBackend
	settings.RemoteURI = c.remoteURI
	if settings.DeliveryMode == repository.DeliveryModeLocal {
		settings.RemoteBackend = ""
	}
	settings.MaxLocalBytes = c.maxLocalBytes
	if err := app.InitRepositoryWithSettings(ctx, repoDir, settings); err != nil {
		return err
	}

	out := flagshim.MustStdoutFromContext(ctx)
	fmt.Fprintf(out, "Initialized empty nando repository in %s\n", repoDir)
	return nil
}
