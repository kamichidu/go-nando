package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/kamichidu/go-nando/controlrpc"
	fusefs "github.com/kamichidu/go-nando/fuse"
	"github.com/kamichidu/go-nando/runtimeconfig"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error { return runWithOutput(args, os.Stderr) }

func runWithOutput(args []string, output io.Writer) error {
	var endpoint, repoDir, mountpoint, tokenFile, caFile, configFile string
	fs := flag.NewFlagSet("nando-fuse", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.StringVar(&endpoint, "endpoint", "", "daemon control endpoint")
	fs.StringVar(&repoDir, "repository", "", "repository path used to derive the default daemon endpoint")
	fs.StringVar(&mountpoint, "mountpoint", "", "directory to mount")
	fs.StringVar(&tokenFile, "token-file", "", "optional bearer token file")
	fs.StringVar(&caFile, "ca-file", "", "optional daemon CA certificate PEM for TCP")
	fs.StringVar(&configFile, "config", "", "configuration file path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if mountpoint == "" {
		return fmt.Errorf("-mountpoint is required")
	}
	cfg, _, err := runtimeconfig.Load(configFile)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	repoDir, err = runtimeconfig.ResolveRepositoryPath(cfg, repoDir)
	if err != nil {
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
	filesystem := fusefs.New(client)
	server, err := filesystem.Mount(mountpoint, nil)
	if err != nil {
		return fmt.Errorf("mount FUSE filesystem: %w", err)
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
	fmt.Fprintln(output, "FUSE filesystem unmounted")
	return nil
}
