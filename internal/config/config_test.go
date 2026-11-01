package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestResolvePathsUsesCrossPlatformXDGStyleLayout(t *testing.T) {
	tests := []struct {
		name, goos string
		env        map[string]string
		config     string
		data       string
	}{
		{"linux defaults", "linux", map[string]string{"HOME": "/home/alice"}, "/home/alice/.config/nando/config.toml", "/home/alice/.local/share/nando"},
		{"macOS defaults", "darwin", map[string]string{"HOME": "/Users/alice"}, "/Users/alice/.config/nando/config.toml", "/Users/alice/.local/share/nando"},
		{"Windows defaults", "windows", map[string]string{"USERPROFILE": `C:\Users\alice`}, `C:\Users\alice\.config\nando\config.toml`, `C:\Users\alice\.local\share\nando`},
		{"XDG overrides on Windows", "windows", map[string]string{"USERPROFILE": `C:\Users\alice`, "XDG_CONFIG_HOME": `D:\cfg`, "XDG_DATA_HOME": `E:\data`}, `D:\cfg\nando\config.toml`, `E:\data\nando`},
		{"XDG overrides on Unix", "linux", map[string]string{"HOME": "/home/alice", "XDG_CONFIG_HOME": "/mnt/config", "XDG_DATA_HOME": "/mnt/data"}, "/mnt/config/nando/config.toml", "/mnt/data/nando"},
		{"fully explicit XDG roots need no home", "linux", map[string]string{"XDG_CONFIG_HOME": "/mnt/config", "XDG_DATA_HOME": "/mnt/data"}, "/mnt/config/nando/config.toml", "/mnt/data/nando"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolvePaths(test.goos, test.env)
			if err != nil {
				t.Fatal(err)
			}
			if got.ConfigFile != test.config || got.DataDir != test.data {
				t.Fatalf("resolved paths=%+v, want config=%q data=%q", got, test.config, test.data)
			}
		})
	}
}

func TestLoadOptionalConfigMissingExplicitMalformedAndDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	if cfg, _, err := LoadOptionalConfig(""); err != nil || *cfg != (Config{}) {
		t.Fatalf("missing implicit config: config=%+v err=%v", cfg, err)
	}
	if _, _, err := LoadOptionalConfig(filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Fatal("missing explicit config was accepted")
	}
	bad := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(bad, []byte("repository = ["), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOptionalConfig(bad); err == nil {
		t.Fatal("malformed explicit config was accepted")
	}
}

func TestLoadConfigSampleHasEffectiveSupportedDefaults(t *testing.T) {
	samplePath := filepath.Join("..", "..", "samples", "config.toml")
	cfg, err := LoadConfig(samplePath)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{}
	if *cfg != want {
		t.Fatalf("sample config=%+v, want documented effective defaults %+v", *cfg, want)
	}
	sampleBytes, err := os.ReadFile(samplePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"repository", "verbose", "daemon_endpoint", "daemon_token_file", "daemon_ca_cert", "daemon_tls_cert", "daemon_tls_key"} {
		if !strings.Contains(string(sampleBytes), key+" =") {
			t.Errorf("sample is missing active config key %q", key)
		}
	}
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", t.TempDir())
	} else {
		t.Setenv("HOME", t.TempDir())
	}
	resolved, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repository != "" || cfg.DaemonEndpoint != "" || resolved.DataDir == filepath.Dir(resolved.ConfigFile) {
		t.Fatalf("sample does not select active default data/transport paths: cfg=%+v paths=%+v", cfg, resolved)
	}
}

func TestGCSSchedulerPolicyRequiresExplicitCompletePositiveSettings(t *testing.T) {
	if _, err := (Config{}).GCSSchedulerPolicy(); err == nil {
		t.Fatal("missing scheduler policy was accepted")
	}
	cfg := Config{GCSSyncIdleInterval: "250ms", GCSSyncPeriodicInterval: "1s", GCSMaxDeliveryDelay: "5s", GCSSyncParallelism: 3}
	got, err := cfg.GCSSchedulerPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if got.IdleInterval != 250*time.Millisecond || got.PeriodicInterval != time.Second || got.MaxDeliveryDelay != 5*time.Second || got.Parallelism != 3 {
		t.Fatalf("scheduler policy=%+v", got)
	}
	cfg.GCSSyncParallelism = 0
	if _, err := cfg.GCSSchedulerPolicy(); err == nil {
		t.Fatal("zero parallelism was accepted")
	}
}

func TestLoadConfigRejectsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unknown.toml")
	if err := os.WriteFile(path, []byte("verbose = true\ninert_key = 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "inert_key") {
		t.Fatalf("unknown/inert setting was not rejected: %v", err)
	}
}
