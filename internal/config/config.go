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
	Repository string       `toml:"repository"`
	Verbose    bool         `toml:"verbose"`
	Daemon     DaemonConfig `toml:"daemon"`
	// Legacy daemon_* keys remain accepted for compatibility. LoadConfig
	// merges them into Daemon and rejects duplicate old/new spellings.
	LegacyDaemonEndpoint    string         `toml:"daemon_endpoint"`
	LegacyDaemonTokenFile   string         `toml:"daemon_token_file"`
	LegacyDaemonCACert      string         `toml:"daemon_ca_cert"`
	LegacyDaemonTLSCert     string         `toml:"daemon_tls_cert"`
	LegacyDaemonTLSKey      string         `toml:"daemon_tls_key"`
	GCSSyncIdleInterval     string         `toml:"gcs_sync_idle_interval"`
	GCSSyncPeriodicInterval string         `toml:"gcs_sync_periodic_interval"`
	GCSMaxDeliveryDelay     string         `toml:"gcs_max_delivery_delay"`
	GCSSyncParallelism      int            `toml:"gcs_sync_parallelism"`
	Recovery                RecoveryConfig `toml:"recovery"`
}

// DaemonConfig contains daemon listener and client transport settings.
type DaemonConfig struct {
	Endpoint      string `toml:"endpoint"`
	Token         string `toml:"token"`
	TokenFromFile string `toml:"token_from_file"`
	TokenFromEnv  string `toml:"token_from_env"`
	// TokenFile accepts the previous nested spelling as an alias for
	// token_from_file while configurations are migrated.
	TokenFile string `toml:"token_file"`
	CACert    string `toml:"ca_cert"`
	TLSCert   string `toml:"tls_cert"`
	TLSKey    string `toml:"tls_key"`
}

// RecoveryConfig configures the logical destination for restored GCS metadata.
type RecoveryConfig struct {
	Directory string `toml:"directory"`
}

// GCSRecoveryDirectory returns the configured logical directory or its default.
func (c Config) GCSRecoveryDirectory() string {
	if c.Recovery.Directory == "" {
		return "/recyclebin"
	}
	return c.Recovery.Directory
}

// GCSSchedulerSettings contains explicit daemon scheduler policy. Durations
// remain strings in Config so missing values are distinguishable from zero.
type GCSSchedulerSettings struct {
	IdleInterval     time.Duration
	PeriodicInterval time.Duration
	MaxDeliveryDelay time.Duration
	Parallelism      int
}

func (c Config) HasGCSSchedulerSettings() bool {
	return c.GCSSyncIdleInterval != "" || c.GCSSyncPeriodicInterval != "" || c.GCSMaxDeliveryDelay != "" || c.GCSSyncParallelism != 0
}

// GCSSchedulerPolicy resolves and validates every required policy value. No
// operational defaults are implied by this configuration API.
func (c Config) GCSSchedulerPolicy() (GCSSchedulerSettings, error) {
	parse := func(name, value string) (time.Duration, error) {
		if value == "" {
			return 0, fmt.Errorf("%s is required when configuring the GCS scheduler", name)
		}
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return 0, fmt.Errorf("%s must be a positive duration", name)
		}
		return d, nil
	}
	idle, err := parse("gcs_sync_idle_interval", c.GCSSyncIdleInterval)
	if err != nil {
		return GCSSchedulerSettings{}, err
	}
	periodic, err := parse("gcs_sync_periodic_interval", c.GCSSyncPeriodicInterval)
	if err != nil {
		return GCSSchedulerSettings{}, err
	}
	maximum, err := parse("gcs_max_delivery_delay", c.GCSMaxDeliveryDelay)
	if err != nil {
		return GCSSchedulerSettings{}, err
	}
	if c.GCSSyncParallelism <= 0 {
		return GCSSchedulerSettings{}, fmt.Errorf("gcs_sync_parallelism must be positive")
	}
	return GCSSchedulerSettings{IdleInterval: idle, PeriodicInterval: periodic, MaxDeliveryDelay: maximum, Parallelism: c.GCSSyncParallelism}, nil
}

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
	legacy := []struct {
		name string
		old  string
		new  *string
	}{
		{"daemon_endpoint", cfg.LegacyDaemonEndpoint, &cfg.Daemon.Endpoint},
		{"daemon_token_file", cfg.LegacyDaemonTokenFile, &cfg.Daemon.TokenFile},
		{"daemon_ca_cert", cfg.LegacyDaemonCACert, &cfg.Daemon.CACert},
		{"daemon_tls_cert", cfg.LegacyDaemonTLSCert, &cfg.Daemon.TLSCert},
		{"daemon_tls_key", cfg.LegacyDaemonTLSKey, &cfg.Daemon.TLSKey},
	}
	for _, item := range legacy {
		if !metadata.IsDefined(item.name) {
			continue
		}
		newKey := "daemon." + strings.TrimPrefix(item.name, "daemon_")
		if item.name != "daemon_token_file" && metadata.IsDefined("daemon", strings.TrimPrefix(item.name, "daemon_")) {
			return nil, fmt.Errorf("configuration keys %q and %q are both set", item.name, newKey)
		}
		if item.name == "daemon_token_file" {
			if item.old != "" && (cfg.Daemon.TokenFromFile != "" || cfg.Daemon.TokenFile != "") {
				return nil, errors.New("daemon token sources are mutually exclusive")
			}
			if cfg.Daemon.TokenFromFile == "" {
				cfg.Daemon.TokenFromFile = item.old
			}
		} else {
			*item.new = item.old
		}
	}
	if cfg.Daemon.TokenFromFile == "" {
		cfg.Daemon.TokenFromFile = cfg.Daemon.TokenFile
	}
	if metadata.IsDefined("daemon", "token_file") || metadata.IsDefined("daemon_token_file") {
		cfg.Daemon.TokenFile = cfg.Daemon.TokenFromFile
	}
	if cfg.Daemon.Token != "" && cfg.Daemon.TokenFromFile != "" ||
		cfg.Daemon.Token != "" && cfg.Daemon.TokenFromEnv != "" ||
		cfg.Daemon.TokenFromFile != "" && cfg.Daemon.TokenFromEnv != "" {
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
