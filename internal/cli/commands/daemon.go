package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/controlrpc"
	"github.com/kamichidu/go-nando/internal/app"
	"github.com/kamichidu/go-nando/internal/cli"
	"github.com/kamichidu/go-nando/internal/daemon"
	"github.com/kamichidu/go-nando/internal/repository"
)

// DaemonCommand runs the single Repository-owning daemon process.
type DaemonCommand struct {
	listen    string
	tlsCert   string
	tlsKey    string
	tokenFile string
}

func (c *DaemonCommand) Name() string { return "daemon" }

func (c *DaemonCommand) RegisterFlags(ctx context.Context, fs *flagshim.FlagSet) context.Context {
	fs.StringVar(&c.listen, "listen", "", "control endpoint (unix:///path or tcp://host:port)")
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
	endpoint := c.listen
	if endpoint == "" {
		endpoint = globalFlags.DaemonEndpoint
	}
	if endpoint == "" {
		endpoint = cfg.Daemon.Endpoint
	}
	if endpoint == "" {
		endpoint, err = controlrpc.DefaultEndpoint(repoDir)
		if err != nil {
			return err
		}
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

	tokenFile := c.tokenFile
	if tokenFile == "" {
		tokenFile = globalFlags.DaemonTokenFile
	}
	if tokenFile == "" {
		tokenFile = cfg.Daemon.TokenFile
	}
	var token string
	if tokenFile != "" {
		token, err = controlrpc.ReadTokenFile(tokenFile)
		if err != nil {
			return fmt.Errorf("read daemon bearer token: %w", err)
		}
	}
	var scheduler *daemon.SchedulerConfig
	repositorySettings, err := app.LoadRepositorySettings(repoDir)
	if err != nil {
		return err
	}
	if repositorySettings.DeliveryMode == repository.DeliveryModeGCS || cfg.HasGCSSchedulerSettings() {
		policy, err := cfg.GCSSchedulerPolicy()
		if err != nil {
			return fmt.Errorf("configure daemon GCS scheduler: %w", err)
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
