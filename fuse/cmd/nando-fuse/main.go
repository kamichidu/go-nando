package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/kamichidu/go-nando/controlrpc"
	fusefs "github.com/kamichidu/go-nando/fuse"
)

func main() {
	var endpoint, repoDir, mountpoint, tokenFile, caFile string
	flag.StringVar(&endpoint, "endpoint", "", "daemon control endpoint")
	flag.StringVar(&repoDir, "repository", "", "repository path used to derive the default daemon endpoint")
	flag.StringVar(&mountpoint, "mountpoint", "", "directory to mount")
	flag.StringVar(&tokenFile, "token-file", "", "optional bearer token file")
	flag.StringVar(&caFile, "ca-file", "", "optional daemon CA certificate PEM for TCP")
	flag.Parse()
	if mountpoint == "" {
		log.Fatal("-mountpoint is required")
	}
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
	filesystem := fusefs.New(client)
	server, err := filesystem.Mount(mountpoint, nil)
	if err != nil {
		log.Fatalf("mount FUSE filesystem: %v", err)
	}
	stopped := make(chan os.Signal, 1)
	signal.Notify(stopped, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stopped
		if err := server.Unmount(); err != nil {
			log.Printf("unmount FUSE filesystem: %v", err)
		}
	}()
	server.Wait()
	fmt.Fprintln(os.Stderr, "FUSE filesystem unmounted")
}
