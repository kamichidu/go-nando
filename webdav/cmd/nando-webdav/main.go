package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kamichidu/go-nando/controlrpc"
	"github.com/kamichidu/go-nando/runtimeconfig"
	"github.com/kamichidu/go-nando/webdav/internal/adapter"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	var endpoint, repoDir, address, tokenFile, caFile, configFile string
	fs := flag.NewFlagSet("nando-webdav", flag.ContinueOnError)
	fs.StringVar(&endpoint, "endpoint", "", "daemon control endpoint")
	fs.StringVar(&repoDir, "repository", "", "repository path used to derive the default daemon endpoint")
	fs.StringVar(&address, "listen", "127.0.0.1:8080", "HTTP listen address (loopback only in Phase A)")
	fs.StringVar(&tokenFile, "token-file", "", "optional daemon bearer token file")
	fs.StringVar(&caFile, "ca-file", "", "optional daemon CA certificate PEM for TCP")
	fs.StringVar(&configFile, "config", "", "configuration file path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, _, err := runtimeconfig.Load(configFile)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	repoDir, err = runtimeconfig.ResolveRepositoryPath(cfg, repoDir)
	if err != nil {
		return err
	}
	if address == "" {
		return fmt.Errorf("-listen is required")
	}
	if err := validateLoopbackAddress(address); err != nil {
		return err
	}
	clientConfig, err := runtimeconfig.ResolveClientConfig(cfg, repoDir, runtimeconfig.ClientOptions{Endpoint: endpoint, TokenFile: tokenFile, CACert: caFile})
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
