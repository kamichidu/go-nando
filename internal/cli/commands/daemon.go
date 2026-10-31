package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/cli"
	"github.com/kamichidu/go-nando/internal/daemon"
	"github.com/kamichidu/go-nando/pkg/controlrpc"
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
	endpoint := c.listen
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

	var token string
	if c.tokenFile != "" {
		token, err = controlrpc.ReadTokenFile(c.tokenFile)
		if err != nil {
			return fmt.Errorf("read daemon bearer token: %w", err)
		}
	}
	return daemon.Serve(ctx, application, repoDir, controlrpc.ListenConfig{
		Endpoint: endpoint,
		CertFile: c.tlsCert,
		KeyFile:  c.tlsKey,
		Token:    token,
	})
}
