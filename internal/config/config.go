package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config represents per-user runtime configuration. Repository delivery
// settings remain in the repository-owned repository.json file.
type Config struct {
	Repository      string `toml:"repository"`
	Verbose         bool   `toml:"verbose"`
	DaemonEndpoint  string `toml:"daemon_endpoint"`
	DaemonTokenFile string `toml:"daemon_token_file"`
	DaemonCACert    string `toml:"daemon_ca_cert"`
	DaemonTLSCert   string `toml:"daemon_tls_cert"`
	DaemonTLSKey    string `toml:"daemon_tls_key"`
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
