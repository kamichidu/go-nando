package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kamichidu/go-nando/controlrpc"
	"github.com/kamichidu/go-nando/webdav/internal/adapter"
)

func main() {
	var endpoint, repoDir, address, tokenFile, caFile string
	flag.StringVar(&endpoint, "endpoint", "", "daemon control endpoint")
	flag.StringVar(&repoDir, "repository", "", "repository path used to derive the default daemon endpoint")
	flag.StringVar(&address, "listen", "127.0.0.1:8080", "HTTP listen address (loopback only in Phase A)")
	flag.StringVar(&tokenFile, "token-file", "", "optional daemon bearer token file")
	flag.StringVar(&caFile, "ca-file", "", "optional daemon CA certificate PEM for TCP")
	flag.Parse()
	if endpoint == "" {
		if repoDir == "" {
			log.Fatal("provide -endpoint or -repository")
		}
		var err error
		endpoint, err = controlrpc.DefaultEndpoint(repoDir)
		if err != nil {
			log.Fatalf("resolve daemon endpoint: %v", err)
		}
	}
	if address == "" {
		log.Fatal("-listen is required")
	}
	host, _, splitErr := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	if splitErr != nil || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
		log.Fatal("Phase A WebDAV must bind to a loopback address")
	}
	config := controlrpc.ClientConfig{Endpoint: endpoint}
	if tokenFile != "" {
		token, err := controlrpc.ReadTokenFile(tokenFile)
		if err != nil {
			log.Fatalf("read daemon token: %v", err)
		}
		config.Token = token
	}
	if caFile != "" {
		data, err := os.ReadFile(caFile)
		if err != nil {
			log.Fatalf("read daemon CA: %v", err)
		}
		config.CACertPEM = data
	}
	client, err := controlrpc.NewClient(config)
	if err != nil {
		log.Fatalf("create daemon client: %v", err)
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
		log.Fatal(err)
	}
}
