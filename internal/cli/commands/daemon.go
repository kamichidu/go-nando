package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/app"
	"github.com/kamichidu/go-nando/internal/cli"
	"github.com/kamichidu/go-nando/internal/daemon"
	"github.com/kamichidu/go-nando/internal/repository"
	"github.com/kamichidu/go-nando/runtimeconfig"
)

// DaemonCommand runs the single Repository-owning daemon process.
type DaemonCommand struct {
	tlsCert   string
	tlsKey    string
	tokenFile string
}

func (c *DaemonCommand) Name() string { return "daemon" }

func (c *DaemonCommand) RegisterFlags(ctx context.Context, fs *flagshim.FlagSet) context.Context {
	fs.StringVar(&c.tlsCert, "tls-cert", "", "TLS certificate for TCP control transport")
	fs.StringVar(&c.tlsKey, "tls-key", "", "TLS private key for TCP control transport")
	fs.StringVar(&c.tokenFile, "token-file", "", "private bearer-token file for TCP control transport")
	return ctx
}

func (c *DaemonCommand) Run(ctx context.Context, args []string) (runErr error) {
	if len(args) != 0 {
		return fmt.Errorf("%w: nando daemon accepts no positional arguments", cli.ErrInvalidArguments)
	}
	repoDir, _, err := cli.ResolveRepositoryAndVerbose(ctx)
	if err != nil {
		return err
	}
	cfg, err := cli.ResolveConfigFromContext(ctx)
	if err != nil {
		return err
	}
	globalFlags := flagshim.MustFlagFromContext[cli.GlobalFlags](ctx)
	endpoint, err := runtimeconfig.ResolveEndpoint(cfg, repoDir, globalFlags.DaemonEndpoint)
	if err != nil {
		return err
	}
	unlock, err := daemon.AcquireRepositoryLock(repoDir)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, unlock()) }()

	application, _, cleanup, err := cli.NewAppFromContext(ctx)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, cleanup()) }()

	token, err := runtimeconfig.ResolveToken(cfg, firstNonEmpty(c.tokenFile, globalFlags.DaemonTokenFile))
	if err != nil {
		return fmt.Errorf("resolve daemon bearer token: %w", err)
	}
	var scheduler *daemon.SchedulerConfig
	repositorySettings, err := app.LoadRepositorySettings(repoDir)
	if err != nil {
		return err
	}
	if repositorySettings.DeliveryMode == repository.DeliveryModeRemote || cfg.HasRemoteSchedulerSettings() {
		policy, err := cfg.RemoteSchedulerPolicy()
		if err != nil {
			return fmt.Errorf("configure daemon remote delivery scheduler: %w", err)
		}
		scheduler = &daemon.SchedulerConfig{IdleInterval: policy.IdleInterval, PeriodicInterval: policy.PeriodicInterval, MaxDeliveryDelay: policy.MaxDeliveryDelay, Parallelism: policy.Parallelism}
	}
	return daemon.ServeWithScheduler(ctx, application, repoDir, daemon.ListenConfig{
		Endpoint: endpoint,
		CertFile: firstNonEmpty(c.tlsCert, cfg.Daemon.TLSCert),
		KeyFile:  firstNonEmpty(c.tlsKey, cfg.Daemon.TLSKey),
		Token:    token,
	}, scheduler)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
