// Package runtimeconfig loads and resolves the per-user configuration shared by
// the nando, nando-fuse, and nando-webdav executables.
package runtimeconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/kamichidu/go-nando/controlrpc"
)

// Config represents settings shared by all binaries.
type Config struct {
	Repository            RepositoryConfig `toml:"repository"`
	Logging               LoggingConfig    `toml:"logging"`
	Daemon                DaemonConfig     `toml:"daemon"`
	Remote                RemoteConfig     `toml:"remote"`
	Recovery              RecoveryConfig   `toml:"recovery"`
	Fuse                  FuseConfig       `toml:"fuse"`
	WebDAV                WebDAVConfig     `toml:"webdav"`
	LegacyDaemonTokenFile string           `toml:"daemon_token_file"`
	LegacyDaemonCACert    string           `toml:"daemon_ca_cert"`
	LegacyDaemonTLSCert   string           `toml:"daemon_tls_cert"`
	LegacyDaemonTLSKey    string           `toml:"daemon_tls_key"`
}
type RepositoryConfig struct {
	Path string `toml:"path"`
}
type LoggingConfig struct {
	Verbose bool `toml:"verbose"`
}
type DaemonConfig struct {
	Listen        string `toml:"listen"`
	Token         string `toml:"token"`
	TokenFromFile string `toml:"token_from_file"`
	TokenFromEnv  string `toml:"token_from_env"`
	TokenFile     string `toml:"token_file"`
	CACert        string `toml:"ca_cert"`
	TLSCert       string `toml:"tls_cert"`
	TLSKey        string `toml:"tls_key"`
}
type RemoteConfig struct {
	SyncIdleInterval     string `toml:"sync_idle_interval"`
	SyncPeriodicInterval string `toml:"sync_periodic_interval"`
	MaxDeliveryDelay     string `toml:"max_delivery_delay"`
	SyncParallelism      int    `toml:"sync_parallelism"`
}
type RecoveryConfig struct {
	Directory string `toml:"directory"`
}
type FuseConfig struct {
	Mountpoint string `toml:"mountpoint"`
}
type WebDAVConfig struct {
	Listen string `toml:"listen"`
}
type RemoteSchedulerSettings struct {
	IdleInterval, PeriodicInterval, MaxDeliveryDelay time.Duration
	Parallelism                                      int
}
type GCSSchedulerSettings = RemoteSchedulerSettings

func (c Config) HasRemoteSchedulerSettings() bool {
	return c.Remote.SyncIdleInterval != "" || c.Remote.SyncPeriodicInterval != "" || c.Remote.MaxDeliveryDelay != "" || c.Remote.SyncParallelism != 0
}
func (c Config) RemoteRecoveryDirectory() string {
	if c.Recovery.Directory == "" {
		return "/recyclebin"
	}
	return c.Recovery.Directory
}
func (c Config) GCSRecoveryDirectory() string                      { return c.RemoteRecoveryDirectory() }
func (c Config) HasGCSSchedulerSettings() bool                     { return c.HasRemoteSchedulerSettings() }
func (c Config) GCSSchedulerPolicy() (GCSSchedulerSettings, error) { return c.RemoteSchedulerPolicy() }

func (c Config) RemoteSchedulerPolicy() (RemoteSchedulerSettings, error) {
	parse := func(name, value string) (time.Duration, error) {
		if value == "" {
			return 0, fmt.Errorf("%s is required when configuring the remote delivery scheduler", name)
		}
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return 0, fmt.Errorf("%s must be a positive duration", name)
		}
		return d, nil
	}
	idle, err := parse("sync_idle_interval", c.Remote.SyncIdleInterval)
	if err != nil {
		return RemoteSchedulerSettings{}, err
	}
	periodic, err := parse("sync_periodic_interval", c.Remote.SyncPeriodicInterval)
	if err != nil {
		return RemoteSchedulerSettings{}, err
	}
	maximum, err := parse("max_delivery_delay", c.Remote.MaxDeliveryDelay)
	if err != nil {
		return RemoteSchedulerSettings{}, err
	}
	if c.Remote.SyncParallelism <= 0 {
		return RemoteSchedulerSettings{}, fmt.Errorf("sync_parallelism must be positive")
	}
	return RemoteSchedulerSettings{IdleInterval: idle, PeriodicInterval: periodic, MaxDeliveryDelay: maximum, Parallelism: c.Remote.SyncParallelism}, nil
}

// Load decodes TOML and rejects unknown keys and ambiguous daemon tokens.
func LoadConfig(path string) (*Config, error) {
	var cfg Config
	metadata, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return nil, err
	}
	if unknown := metadata.Undecoded(); len(unknown) != 0 {
		keys := make([]string, len(unknown))
		for i, key := range unknown {
			keys[i] = key.String()
		}
		return nil, fmt.Errorf("unknown configuration keys: %s", strings.Join(keys, ", "))
	}
	for _, item := range []struct {
		name, old string
		value     *string
	}{
		{"daemon_ca_cert", "ca_cert", &cfg.Daemon.CACert},
		{"daemon_tls_cert", "tls_cert", &cfg.Daemon.TLSCert},
		{"daemon_tls_key", "tls_key", &cfg.Daemon.TLSKey},
	} {
		if metadata.IsDefined(item.name) {
			if metadata.IsDefined("daemon", item.old) {
				return nil, fmt.Errorf("configuration keys %q and %q are both set", item.name, "daemon."+item.old)
			}
			switch item.name {
			case "daemon_ca_cert":
				*item.value = cfg.LegacyDaemonCACert
			case "daemon_tls_cert":
				*item.value = cfg.LegacyDaemonTLSCert
			case "daemon_tls_key":
				*item.value = cfg.LegacyDaemonTLSKey
			}
		}
	}
	tokenSources := 0
	for _, key := range [][2]string{{"daemon", "token"}, {"daemon", "token_from_file"}, {"daemon", "token_from_env"}, {"daemon", "token_file"}, {"", "daemon_token_file"}} {
		if key[0] == "" {
			if metadata.IsDefined(key[1]) {
				tokenSources++
			}
		} else if metadata.IsDefined(key[0], key[1]) {
			tokenSources++
		}
	}
	if tokenSources > 1 {
		return nil, errors.New("daemon token sources are mutually exclusive")
	}
	if metadata.IsDefined("daemon", "token_file") {
		cfg.Daemon.TokenFromFile = cfg.Daemon.TokenFile
	}
	if metadata.IsDefined("daemon_token_file") {
		cfg.Daemon.TokenFromFile, cfg.Daemon.TokenFile = cfg.LegacyDaemonTokenFile, cfg.LegacyDaemonTokenFile
	}
	return &cfg, nil
}

// Load reads an explicitly selected TOML file, or the shared XDG default.
// A missing implicit default is allowed; a missing explicit file is an error.
func Load(explicitPath string) (*Config, string, error) {
	path := explicitPath
	if path == "" {
		var err error
		path, err = DefaultConfigPath()
		if err != nil {
			return nil, "", err
		}
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		if explicitPath == "" && errors.Is(err, os.ErrNotExist) {
			return &Config{}, path, nil
		}
		return nil, path, err
	}
	return cfg, path, nil
}

func DefaultConfigPath() (string, error) {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home := os.Getenv("HOME")
		if runtime.GOOS == "windows" {
			home = os.Getenv("USERPROFILE")
		}
		if home == "" {
			return "", fmt.Errorf("home directory environment variable is required for %s", runtime.GOOS)
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "nando", "config.toml"), nil
}

// ResolveRepositoryPath selects the CLI override, TOML path, or shared XDG
// data-directory default.
func ResolveRepositoryPath(cfg *Config, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if cfg != nil && cfg.Repository.Path != "" {
		return cfg.Repository.Path, nil
	}
	paths, err := resolveDataPath()
	if err != nil {
		return "", fmt.Errorf("resolve repository default: %w", err)
	}
	return paths, nil
}

// ClientOptions overrides common daemon client configuration from a CLI.
type ClientOptions struct {
	Endpoint  string
	TokenFile string
	CACert    string
}

// ResolveEndpoint selects the CLI override, configured daemon listener, or
// repository-derived Unix socket in that order.
func ResolveEndpoint(cfg *Config, repositoryPath, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if cfg != nil && cfg.Daemon.Listen != "" {
		return cfg.Daemon.Listen, nil
	}
	return controlrpc.DefaultEndpoint(repositoryPath)
}

// ResolveToken resolves a CLI file override or the configured token source.
func ResolveToken(cfg *Config, tokenFileOverride string) (string, error) {
	if cfg == nil {
		cfg = &Config{}
	}
	tokenFile := tokenFileOverride
	literal, envName := cfg.Daemon.Token, cfg.Daemon.TokenFromEnv
	if tokenFile == "" {
		tokenFile = cfg.Daemon.TokenFromFile
	} else {
		literal, envName = "", ""
	}
	token, err := controlrpc.ResolveToken(literal, tokenFile, envName)
	if err != nil {
		return "", fmt.Errorf("resolve daemon bearer token: %w", err)
	}
	return token, nil
}

// ResolveClientConfig applies CLI overrides, shared TOML settings, and finally
// derives the default Unix socket from repositoryPath.
func ResolveClientConfig(cfg *Config, repositoryPath string, options ClientOptions) (controlrpc.ClientConfig, error) {
	if cfg == nil {
		cfg = &Config{}
	}
	endpoint, err := ResolveEndpoint(cfg, repositoryPath, options.Endpoint)
	if err != nil {
		return controlrpc.ClientConfig{}, fmt.Errorf("resolve daemon endpoint: %w", err)
	}

	token, err := ResolveToken(cfg, options.TokenFile)
	if err != nil {
		return controlrpc.ClientConfig{}, err
	}

	caFile := options.CACert
	if caFile == "" {
		caFile = cfg.Daemon.CACert
	}
	var caPEM []byte
	if caFile != "" {
		caPEM, err = os.ReadFile(caFile)
		if err != nil {
			return controlrpc.ClientConfig{}, fmt.Errorf("read daemon CA certificate: %w", err)
		}
	}
	return controlrpc.ClientConfig{Endpoint: endpoint, Token: token, CACertPEM: caPEM}, nil
}

func resolveDataPath() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home := os.Getenv("HOME")
		if runtime.GOOS == "windows" {
			home = os.Getenv("USERPROFILE")
		}
		if home == "" {
			return "", fmt.Errorf("home directory environment variable is required for %s", runtime.GOOS)
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "nando"), nil
}
