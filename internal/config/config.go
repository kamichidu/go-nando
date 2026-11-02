package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config represents per-user runtime configuration. Repository delivery
// settings remain in the repository-owned repository.json file.
type Config struct {
	Repository                 string         `toml:"repository"`
	Verbose                    bool           `toml:"verbose"`
	Daemon                     DaemonConfig   `toml:"daemon"`
	LegacyDaemonEndpoint       string         `toml:"daemon_endpoint"`
	LegacyDaemonTokenFile      string         `toml:"daemon_token_file"`
	LegacyDaemonCACert         string         `toml:"daemon_ca_cert"`
	LegacyDaemonTLSCert        string         `toml:"daemon_tls_cert"`
	LegacyDaemonTLSKey         string         `toml:"daemon_tls_key"`
	RemoteSyncIdleInterval     string         `toml:"remote_sync_idle_interval"`
	RemoteSyncPeriodicInterval string         `toml:"remote_sync_periodic_interval"`
	RemoteMaxDeliveryDelay     string         `toml:"remote_max_delivery_delay"`
	RemoteSyncParallelism      int            `toml:"remote_sync_parallelism"`
	GCSSyncIdleInterval        string         `toml:"gcs_sync_idle_interval"`
	GCSSyncPeriodicInterval    string         `toml:"gcs_sync_periodic_interval"`
	GCSMaxDeliveryDelay        string         `toml:"gcs_max_delivery_delay"`
	GCSSyncParallelism         int            `toml:"gcs_sync_parallelism"`
	Recovery                   RecoveryConfig `toml:"recovery"`
}

// DaemonConfig contains daemon listener and client transport settings.
type DaemonConfig struct {
	Endpoint      string `toml:"endpoint"`
	Token         string `toml:"token"`
	TokenFromFile string `toml:"token_from_file"`
	TokenFromEnv  string `toml:"token_from_env"`
	TokenFile     string `toml:"token_file"`
	CACert        string `toml:"ca_cert"`
	TLSCert       string `toml:"tls_cert"`
	TLSKey        string `toml:"tls_key"`
}

// RecoveryConfig configures the logical destination for restored remote objects.
type RecoveryConfig struct {
	Directory string `toml:"directory"`
}

// RemoteRecoveryDirectory returns the configured logical directory or its default.
func (c Config) RemoteRecoveryDirectory() string {
	if c.Recovery.Directory == "" {
		return "/recyclebin"
	}
	return c.Recovery.Directory
}

// GCSRecoveryDirectory is a compatibility alias for RemoteRecoveryDirectory.
func (c Config) GCSRecoveryDirectory() string { return c.RemoteRecoveryDirectory() }

// RemoteSchedulerSettings contains explicit daemon scheduler policy. Durations
// remain strings in Config so missing values are distinguishable from zero.
type RemoteSchedulerSettings struct {
	IdleInterval     time.Duration
	PeriodicInterval time.Duration
	MaxDeliveryDelay time.Duration
	Parallelism      int
}

// GCSSchedulerSettings is retained as a source compatibility alias.
type GCSSchedulerSettings = RemoteSchedulerSettings

func (c Config) remoteValues() (string, string, string, int) {
	idle, periodic, maximum, parallelism := c.RemoteSyncIdleInterval, c.RemoteSyncPeriodicInterval, c.RemoteMaxDeliveryDelay, c.RemoteSyncParallelism
	if idle == "" {
		idle = c.GCSSyncIdleInterval
	}
	if periodic == "" {
		periodic = c.GCSSyncPeriodicInterval
	}
	if maximum == "" {
		maximum = c.GCSMaxDeliveryDelay
	}
	if parallelism == 0 {
		parallelism = c.GCSSyncParallelism
	}
	return idle, periodic, maximum, parallelism
}

func (c Config) validateRemoteAliases() error {
	pairs := [][2]string{{c.RemoteSyncIdleInterval, c.GCSSyncIdleInterval}, {c.RemoteSyncPeriodicInterval, c.GCSSyncPeriodicInterval}, {c.RemoteMaxDeliveryDelay, c.GCSMaxDeliveryDelay}}
	for _, pair := range pairs {
		if pair[0] != "" && pair[1] != "" && pair[0] != pair[1] {
			return fmt.Errorf("generic remote scheduler setting conflicts with its legacy GCS alias")
		}
	}
	if c.RemoteSyncParallelism != 0 && c.GCSSyncParallelism != 0 && c.RemoteSyncParallelism != c.GCSSyncParallelism {
		return fmt.Errorf("remote_sync_parallelism conflicts with legacy gcs_sync_parallelism")
	}
	return nil
}

func (c Config) HasRemoteSchedulerSettings() bool {
	idle, periodic, maximum, parallelism := c.remoteValues()
	return idle != "" || periodic != "" || maximum != "" || parallelism != 0
}

func (c Config) HasGCSSchedulerSettings() bool { return c.HasRemoteSchedulerSettings() }

// RemoteSchedulerPolicy resolves and validates every required policy value. No
// operational defaults are implied by this configuration API.
func (c Config) RemoteSchedulerPolicy() (RemoteSchedulerSettings, error) {
	if err := c.validateRemoteAliases(); err != nil {
		return RemoteSchedulerSettings{}, err
	}
	idleValue, periodicValue, maximumValue, parallelism := c.remoteValues()
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
	idle, err := parse("remote_sync_idle_interval", idleValue)
	if err != nil {
		return RemoteSchedulerSettings{}, err
	}
	periodic, err := parse("remote_sync_periodic_interval", periodicValue)
	if err != nil {
		return RemoteSchedulerSettings{}, err
	}
	maximum, err := parse("remote_max_delivery_delay", maximumValue)
	if err != nil {
		return RemoteSchedulerSettings{}, err
	}
	if parallelism <= 0 {
		return RemoteSchedulerSettings{}, fmt.Errorf("remote_sync_parallelism must be positive")
	}
	return RemoteSchedulerSettings{IdleInterval: idle, PeriodicInterval: periodic, MaxDeliveryDelay: maximum, Parallelism: parallelism}, nil
}

// GCSSchedulerPolicy is retained as a source compatibility alias.
func (c Config) GCSSchedulerPolicy() (GCSSchedulerSettings, error) { return c.RemoteSchedulerPolicy() }

// LoadConfig reads and strictly decodes a TOML configuration file.
func LoadConfig(path string) (*Config, error) {
	var cfg Config
	metadata, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return nil, err
	}
	if unknown := metadata.Undecoded(); len(unknown) != 0 {
		return nil, fmt.Errorf("unknown configuration keys: %s", strings.Join(keysToStrings(unknown), ", "))
	}
	if err := cfg.validateRemoteAliases(); err != nil {
		return nil, err
	}
	legacy := []struct {
		name, old string
		new       *string
	}{
		{"daemon_endpoint", cfg.LegacyDaemonEndpoint, &cfg.Daemon.Endpoint},
		{"daemon_ca_cert", cfg.LegacyDaemonCACert, &cfg.Daemon.CACert},
		{"daemon_tls_cert", cfg.LegacyDaemonTLSCert, &cfg.Daemon.TLSCert},
		{"daemon_tls_key", cfg.LegacyDaemonTLSKey, &cfg.Daemon.TLSKey},
	}
	for _, item := range legacy {
		if !metadata.IsDefined(item.name) {
			continue
		}
		key := strings.TrimPrefix(item.name, "daemon_")
		if metadata.IsDefined("daemon", key) {
			return nil, fmt.Errorf("configuration keys %q and %q are both set", item.name, "daemon."+key)
		}
		*item.new = item.old
	}
	if cfg.Daemon.TokenFile != "" && cfg.Daemon.TokenFromFile != "" {
		return nil, errors.New("daemon token sources are mutually exclusive")
	}
	fileDefined := metadata.IsDefined("daemon", "token_file")
	if fileDefined {
		cfg.Daemon.TokenFromFile = cfg.Daemon.TokenFile
	}
	if metadata.IsDefined("daemon_token_file") {
		if cfg.Daemon.TokenFromFile != "" || cfg.Daemon.Token != "" || cfg.Daemon.TokenFromEnv != "" {
			return nil, errors.New("daemon token sources are mutually exclusive")
		}
		cfg.Daemon.TokenFromFile = cfg.LegacyDaemonTokenFile
		cfg.Daemon.TokenFile = cfg.Daemon.TokenFromFile
	}
	if metadata.IsDefined("daemon", "token_file") {
		cfg.Daemon.TokenFile = cfg.Daemon.TokenFromFile
	}
	if cfg.Daemon.Token != "" && cfg.Daemon.TokenFromFile != "" || cfg.Daemon.Token != "" && cfg.Daemon.TokenFromEnv != "" || cfg.Daemon.TokenFromFile != "" && cfg.Daemon.TokenFromEnv != "" {
		return nil, errors.New("daemon token sources are mutually exclusive")
	}
	return &cfg, nil
}

// LoadOptionalConfig uses defaults only when the default config file is absent.
// An explicitly named config file must exist.
func LoadOptionalConfig(explicitPath string) (*Config, string, error) {
	path := explicitPath
	if path == "" {
		resolved, err := ResolvePaths(runtime.GOOS, currentEnvironment())
		if err != nil {
			return nil, "", err
		}
		path = resolved.ConfigFile
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

// ResolvedPaths contains the user config file and independent repository data
// directory selected by the cross-platform XDG-style policy.
type ResolvedPaths struct {
	ConfigFile string
	DataDir    string
}

// DefaultPaths resolves config and data locations from the current process environment.
func DefaultPaths() (ResolvedPaths, error) {
	return ResolvePaths(runtime.GOOS, currentEnvironment())
}

// DefaultLegacyRepositoryDir resolves the pre-XDG repository location from the
// current environment for migration safety checks.
func DefaultLegacyRepositoryDir() (string, error) {
	return LegacyRepositoryDir(runtime.GOOS, currentEnvironment())
}

// ResolvePaths is a pure platform-policy function. It deliberately does not
// call os.UserConfigDir so the same XDG-style layout applies on all platforms.
func ResolvePaths(goos string, env map[string]string) (ResolvedPaths, error) {
	join := func(parts ...string) string {
		if goos == "windows" {
			if len(parts) == 0 {
				return ""
			}
			return windowsJoin(parts[0], parts[1:]...)
		}
		return filepath.Join(parts...)
	}
	configBase := env["XDG_CONFIG_HOME"]
	dataBase := env["XDG_DATA_HOME"]
	if configBase == "" || dataBase == "" {
		var home string
		if goos == "windows" {
			home = env["USERPROFILE"]
		} else {
			home = env["HOME"]
		}
		if home == "" {
			return ResolvedPaths{}, fmt.Errorf("home directory environment variable is required for %s", goos)
		}
		if configBase == "" {
			configBase = join(home, ".config")
		}
		if dataBase == "" {
			dataBase = join(home, ".local", "share")
		}
	}
	return ResolvedPaths{
		ConfigFile: join(configBase, "nando", "config.toml"),
		DataDir:    join(dataBase, "nando"),
	}, nil
}

// LegacyRepositoryDir returns the previous os.UserConfigDir-based repository
// location so callers can prevent silently opening a different repository.
func LegacyRepositoryDir(goos string, env map[string]string) (string, error) {
	join := func(parts ...string) string {
		if goos == "windows" {
			if len(parts) == 0 {
				return ""
			}
			return windowsJoin(parts[0], parts[1:]...)
		}
		return filepath.Join(parts...)
	}
	if goos == "windows" {
		base := env["APPDATA"]
		if base == "" {
			profile := env["USERPROFILE"]
			if profile == "" {
				return "", errors.New("USERPROFILE is required to resolve legacy Windows repository path")
			}
			base = join(profile, "AppData", "Roaming")
		}
		return join(base, "nando"), nil
	}
	home := env["HOME"]
	if home == "" {
		return "", errors.New("HOME is required to resolve legacy repository path")
	}
	if goos == "darwin" {
		return join(home, "Library", "Application Support", "nando"), nil
	}
	base := env["XDG_CONFIG_HOME"]
	if base == "" {
		base = join(home, ".config")
	}
	return join(base, "nando"), nil
}

func currentEnvironment() map[string]string {
	return map[string]string{
		"HOME":            os.Getenv("HOME"),
		"USERPROFILE":     os.Getenv("USERPROFILE"),
		"APPDATA":         os.Getenv("APPDATA"),
		"XDG_CONFIG_HOME": os.Getenv("XDG_CONFIG_HOME"),
		"XDG_DATA_HOME":   os.Getenv("XDG_DATA_HOME"),
	}
}

func windowsJoin(base string, parts ...string) string {
	result := strings.TrimRight(strings.ReplaceAll(base, "/", `\`), `\`)
	for _, part := range parts {
		result += `\` + strings.Trim(part, `\/`)
	}
	return result
}

func keysToStrings(keys []toml.Key) []string {
	result := make([]string, len(keys))
	for i, key := range keys {
		result[i] = key.String()
	}
	return result
}
