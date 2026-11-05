package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/kamichidu/go-flagshim"
	fusefs "github.com/kamichidu/go-nando/adapters/fuse"
	"github.com/kamichidu/go-nando/controlrpc"
	"github.com/kamichidu/go-nando/runtimeconfig"
)

type flags struct {
	Config, Repository, DaemonListen, DaemonTokenFile, DaemonCACert, Mountpoint string
	MountOptions                                                                []string
}
type command struct{ options flags }

type stringListValue []string

func (v *stringListValue) String() string { return strings.Join(*v, ",") }
func (v *stringListValue) Set(value string) error {
	*v = append(*v, value)
	return nil
}

var _ flag.Value = (*stringListValue)(nil)

func resolveMountOptions(configured, commandLine []string) []string {
	if commandLine != nil {
		return append([]string(nil), commandLine...)
	}
	return append([]string(nil), configured...)
}

func (*command) Name() string { return "nando-fuse" }
func (c *command) RegisterFlags(ctx context.Context, fs *flagshim.FlagSet) context.Context {
	fs.StringVar(&c.options.Config, "config", "", "configuration file path")
	fs.StringVar(&c.options.Repository, "repository", "", "repository directory")
	fs.StringVar(&c.options.DaemonListen, "daemon-listen", "", "daemon control listen address")
	fs.StringVar(&c.options.DaemonTokenFile, "daemon-token-file", "", "private bearer-token file")
	fs.StringVar(&c.options.DaemonCACert, "daemon-ca-cert", "", "CA certificate PEM for daemon TLS")
	fs.StringVar(&c.options.Mountpoint, "fuse-mountpoint", "", "directory to mount")
	fs.Var((*stringListValue)(&c.options.MountOptions), "fuse-mount-option", "FUSE mount option (repeatable)")
	return ctx
}
func (c *command) Run(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("nando-fuse accepts no positional arguments")
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
	mountpoint := o.Mountpoint
	if mountpoint == "" {
		mountpoint = cfg.Fuse.Mountpoint
	}
	if mountpoint == "" {
		return fmt.Errorf("--fuse-mountpoint is required (or set [fuse].mountpoint)")
	}
	clientConfig, err := runtimeconfig.ResolveClientConfig(cfg, repoDir, runtimeconfig.ClientOptions{Endpoint: o.DaemonListen, TokenFile: o.DaemonTokenFile, CACert: o.DaemonCACert})
	if err != nil {
		return err
	}
	client, err := controlrpc.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("create daemon client: %w", err)
	}
	mountOptions := resolveMountOptions(cfg.Fuse.MountOptions, o.MountOptions)
	server, err := fusefs.New(client).Mount(mountpoint, &fs.Options{MountOptions: fuse.MountOptions{Options: mountOptions}})
	if err != nil {
		for _, option := range mountOptions {
			if option == "allow_other" {
				return fmt.Errorf("mount FUSE filesystem: %w (allow_other may require user_allow_other in /etc/fuse.conf)", err)
			}
		}
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
	fmt.Fprintln(flagshim.MustStdoutFromContext(ctx), "FUSE filesystem unmounted")
	return nil
}
func main() {
	os.Exit(flagshim.Run(context.Background(), os.Stdin, os.Stdout, os.Stderr, &command{}, os.Args))
}
