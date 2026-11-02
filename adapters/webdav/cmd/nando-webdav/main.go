package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/adapters/webdav/internal/adapter"
	"github.com/kamichidu/go-nando/controlrpc"
	"github.com/kamichidu/go-nando/runtimeconfig"
)

type flags struct{ Config, Repository, DaemonListen, DaemonTokenFile, DaemonCACert, Listen string }
type command struct{ options flags }

func (*command) Name() string { return "nando-webdav" }
func (c *command) RegisterFlags(ctx context.Context, fs *flagshim.FlagSet) context.Context {
	fs.StringVar(&c.options.Config, "config", "", "configuration file path")
	fs.StringVar(&c.options.Repository, "repository", "", "repository directory")
	fs.StringVar(&c.options.DaemonListen, "daemon-listen", "", "daemon control listen address")
	fs.StringVar(&c.options.DaemonTokenFile, "daemon-token-file", "", "private bearer-token file")
	fs.StringVar(&c.options.DaemonCACert, "daemon-ca-cert", "", "CA certificate PEM for daemon TLS")
	fs.StringVar(&c.options.Listen, "webdav-listen", "", "loopback HTTP listen address")
	return ctx
}
func (c *command) Run(_ context.Context, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("nando-webdav accepts no positional arguments")
	}
	o := c.options
	cfg, _, err := runtimeconfig.Load(o.Config)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	repoDir, err := runtimeconfig.ResolveRepositoryPath(cfg, o.Repository)
	if err != nil {
		return err
	}
	address := o.Listen
	if address == "" {
		address = cfg.WebDAV.Listen
	}
	if address == "" {
		address = "127.0.0.1:8080"
	}
	if err := validateLoopbackAddress(address); err != nil {
		return err
	}
	clientConfig, err := runtimeconfig.ResolveClientConfig(cfg, repoDir, runtimeconfig.ClientOptions{Endpoint: o.DaemonListen, TokenFile: o.DaemonTokenFile, CACert: o.DaemonCACert})
	if err != nil {
		return err
	}
	client, err := controlrpc.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("create daemon client: %w", err)
	}
	server := &http.Server{Addr: address, Handler: adapter.New(client), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	stopped := make(chan os.Signal, 1)
	signal.Notify(stopped, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stopped
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("shutdown WebDAV server: %v", err)
		}
	}()
	log.Printf("WebDAV listening on %s", address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
func main() {
	os.Exit(flagshim.Run(context.Background(), os.Stdin, os.Stdout, os.Stderr, &command{}, os.Args))
}

func validateLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid WebDAV listen address %q: %w", address, err)
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("phase A WebDAV must bind to a loopback address")
	}
	if !strings.EqualFold(host, "localhost") {
		return fmt.Errorf("phase A WebDAV must bind to a loopback address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve WebDAV loopback host %q: %w", host, err)
	}
	if len(addresses) == 0 {
		return fmt.Errorf("WebDAV host %q has no addresses", host)
	}
	for _, address := range addresses {
		if !address.IP.IsLoopback() {
			return fmt.Errorf("phase A WebDAV host %q resolves outside loopback", host)
		}
	}
	return nil
}
