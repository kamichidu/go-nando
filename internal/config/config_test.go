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
	if err := os.WriteFile(bad, []byte("[repository]\npath = ["), 0600); err != nil {
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
		t.Fatalf("sample config=%+v, want empty configuration defaults %+v", *cfg, want)
	}
	sampleBytes, err := os.ReadFile(samplePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, section := range []string{"[repository]", "[logging]", "[remote]", "[daemon]", "[recovery]", "[fuse]", "[webdav]"} {
		if !strings.Contains(string(sampleBytes), section) {
			t.Errorf("sample is missing config section %q", section)
		}
	}
	for _, key := range []string{"path", "verbose"} {
		if !strings.Contains(string(sampleBytes), key+" =") {
			t.Errorf("sample is missing active config key %q", key)
		}
	}
	for _, key := range []string{"path", "verbose", "listen", "token", "token_from_file", "token_from_env", "ca_cert", "tls_cert", "tls_key", "sync_idle_interval", "sync_periodic_interval", "max_delivery_delay", "sync_parallelism", "directory", "mountpoint"} {
		if !strings.Contains(string(sampleBytes), "# "+key+" =") {
			t.Errorf("sample is missing commented supported config key %q", key)
		}
	}
	if !strings.Contains(string(sampleBytes), "# listen = \"127.0.0.1:8080\"") {
		t.Error("sample is missing the commented WebDAV listen setting")
	}
	// Enable every sample value that can be used together. Token sources are
	// mutually exclusive, so choose token_from_env and leave the other examples
	// commented. This checks the documented options decode in their shown tables.
	enabledSample := string(sampleBytes)
	for _, key := range []string{"path", "verbose", "listen", "token_from_env", "ca_cert", "tls_cert", "tls_key", "sync_idle_interval", "sync_periodic_interval", "max_delivery_delay", "sync_parallelism", "directory", "mountpoint"} {
		enabledSample = strings.ReplaceAll(enabledSample, "# "+key+" =", key+" =")
	}
	enabledPath := filepath.Join(t.TempDir(), "enabled-config.toml")
	if err := os.WriteFile(enabledPath, []byte(enabledSample), 0600); err != nil {
		t.Fatal(err)
	}
	enabled, err := LoadConfig(enabledPath)
	if err != nil {
		t.Fatalf("sample options enabled in their documented sections: %v", err)
	}
	if enabled.Repository.Path != "/home/alex/nando-data" || enabled.Logging.Verbose || enabled.Daemon.Listen != "tcp://127.0.0.1:7443" || enabled.Daemon.TokenFromEnv != "NANDO_DAEMON_TOKEN" || enabled.Daemon.CACert != "/etc/nando/ca.pem" || enabled.Daemon.TLSCert != "/etc/nando/server.pem" || enabled.Daemon.TLSKey != "/etc/nando/server-key.pem" || enabled.Remote.SyncIdleInterval != "30s" || enabled.Remote.SyncPeriodicInterval != "5m" || enabled.Remote.MaxDeliveryDelay != "30m" || enabled.Remote.SyncParallelism != 2 || enabled.Recovery.Directory != "/recyclebin" {
		t.Fatalf("enabled sample options decoded into unexpected fields: %+v", enabled)
	}
	if _, err := enabled.RemoteSchedulerPolicy(); err != nil {
		t.Fatalf("enabled sample remote scheduler values are invalid: %v", err)
	}
	for _, key := range []string{"sync_idle_interval", "sync_periodic_interval", "max_delivery_delay", "sync_parallelism"} {
		if strings.Index(string(sampleBytes), "# "+key+" =") < strings.Index(string(sampleBytes), "[remote]") || strings.Index(string(sampleBytes), "# "+key+" =") > strings.Index(string(sampleBytes), "[daemon]") {
			t.Errorf("remote key %q must appear inside [remote] before [daemon]", key)
		}
	}
	for key, section := range map[string]string{"path": "[repository]", "verbose": "[logging]", "listen": "[daemon]", "directory": "[recovery]"} {
		if strings.Index(string(sampleBytes), "# "+key+" =") < strings.Index(string(sampleBytes), section) {
			t.Errorf("config key %q must appear inside %s", key, section)
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
	if cfg.Repository.Path != "" || cfg.Daemon.Listen != "" || resolved.DataDir == filepath.Dir(resolved.ConfigFile) {
		t.Fatalf("sample does not select active default data/transport paths: cfg=%+v paths=%+v", cfg, resolved)
	}
}

func TestLoadConfigDaemonSectionAndLegacyKeys(t *testing.T) {
	tests := []struct {
		name string
		text string
		want DaemonConfig
	}{
		{"nested token sources", "[daemon]\nlisten = \"tcp://host:7443\"\ntoken = \"literal\"\nca_cert = \"ca.pem\"\ntls_cert = \"cert.pem\"\ntls_key = \"key.pem\"\n", DaemonConfig{Listen: "tcp://host:7443", Token: "literal", CACert: "ca.pem", TLSCert: "cert.pem", TLSKey: "key.pem"}},
		{"nested file alias", "[daemon]\ntoken_file = \"token\"\n", DaemonConfig{TokenFromFile: "token", TokenFile: "token"}},
		{"legacy token and TLS keys", "daemon_token_file = \"token\"\ndaemon_ca_cert = \"ca.pem\"\ndaemon_tls_cert = \"cert.pem\"\ndaemon_tls_key = \"key.pem\"\n", DaemonConfig{TokenFromFile: "token", TokenFile: "token", CACert: "ca.pem", TLSCert: "cert.pem", TLSKey: "key.pem"}},
		{"mixed distinct fields", "[daemon]\nlisten = \"tcp://host:7443\"\ntoken_from_file = \"token\"\n", DaemonConfig{Listen: "tcp://host:7443", TokenFromFile: "token"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(test.text), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path)
			if err != nil || cfg.Daemon != test.want {
				t.Fatalf("daemon config=%+v err=%v, want %+v", cfg, err, test.want)
			}
		})
	}
	for _, text := range []string{
		"daemon_endpoint = \"tcp://host:7443\"\n",
		"[daemon]\nendpoint = \"tcp://host:7443\"\n",
	} {
		path := filepath.Join(t.TempDir(), "ambiguous.toml")
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "unknown configuration keys") {
			t.Fatalf("removed or ambiguous daemon key accepted: %q", text)
		}
	}
	path := filepath.Join(t.TempDir(), "ambiguous.toml")
	if err := os.WriteFile(path, []byte("daemon_tls_key = \"key.pem\"\n[daemon]\ntls_key = \"\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "both set") {
		t.Fatalf("duplicate old/new daemon TLS keys accepted: %v", err)
	}
	for _, text := range []string{
		"[daemon]\ntoken = \"secret-value\"\ntoken_from_file = \"path\"\n",
		"[daemon]\ntoken_from_file = \"path\"\ntoken_from_env = \"SECRET_NAME\"\n",
		"[daemon]\ntoken = \"secret-value\"\ntoken_from_env = \"SECRET_NAME\"\n",
		"daemon_token_file = \"legacy-path\"\n[daemon]\ntoken_from_env = \"SECRET_NAME\"\n",
		"[daemon]\ntoken = \"\"\ntoken_from_env = \"SECRET_NAME\"\n",
		"[daemon]\ntoken_from_file = \"\"\ntoken_from_env = \"SECRET_NAME\"\n",
		"[daemon]\ntoken_file = \"\"\ntoken = \"secret-value\"\n",
		"daemon_token_file = \"\"\n[daemon]\ntoken = \"secret-value\"\n",
	} {
		path := filepath.Join(t.TempDir(), "conflicting-token-sources.toml")
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil || strings.Contains(err.Error(), "secret-value") || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("conflicting token sources were not safely rejected: %v", err)
		}
	}
	for _, text := range []string{
		"[daemon]\ntoken = \"\"\n",
		"[daemon]\ntoken_from_file = \"\"\n",
		"[daemon]\ntoken_from_env = \"\"\n",
		"[daemon]\ntoken_file = \"\"\n",
		"daemon_token_file = \"\"\n",
	} {
		path := filepath.Join(t.TempDir(), "empty-token-source.toml")
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err != nil {
			t.Fatalf("single empty token source %q rejected: %v", text, err)
		}
	}
}

func TestGCSSchedulerPolicyRequiresExplicitCompletePositiveSettings(t *testing.T) {
	if _, err := (Config{}).GCSSchedulerPolicy(); err == nil {
		t.Fatal("missing scheduler policy was accepted")
	}
	cfg := Config{Remote: RemoteConfig{SyncIdleInterval: "250ms", SyncPeriodicInterval: "1s", MaxDeliveryDelay: "5s", SyncParallelism: 3}}
	got, err := cfg.GCSSchedulerPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if got.IdleInterval != 250*time.Millisecond || got.PeriodicInterval != time.Second || got.MaxDeliveryDelay != 5*time.Second || got.Parallelism != 3 {
		t.Fatalf("scheduler policy=%+v", got)
	}
	cfg.Remote.SyncParallelism = 0
	if _, err := cfg.GCSSchedulerPolicy(); err == nil {
		t.Fatal("zero parallelism was accepted")
	}
}

func TestRemoteSchedulerConfigReadsCanonicalKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	contents := "[remote]\nsync_idle_interval = \"250ms\"\nsync_periodic_interval = \"1s\"\nmax_delivery_delay = \"5s\"\nsync_parallelism = 3\n"
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Remote.SyncIdleInterval != "250ms" || cfg.Remote.SyncPeriodicInterval != "1s" || cfg.Remote.MaxDeliveryDelay != "5s" || cfg.Remote.SyncParallelism != 3 {
		t.Fatalf("remote config=%+v", cfg.Remote)
	}
	policy, err := cfg.RemoteSchedulerPolicy()
	if err != nil || policy.Parallelism != 3 || policy.MaxDeliveryDelay != 5*time.Second {
		t.Fatalf("remote policy=%+v err=%v", policy, err)
	}
}

func TestRemoteSchedulerConfigRejectsLegacyKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	for _, contents := range []string{
		"remote_sync_idle_interval = \"250ms\"\n",
		"remote_sync_periodic_interval = \"1s\"\n",
		"remote_max_delivery_delay = \"5s\"\n",
		"remote_sync_parallelism = 2\n",
		"gcs_sync_idle_interval = \"250ms\"\n",
		"gcs_sync_periodic_interval = \"1s\"\n",
		"gcs_max_delivery_delay = \"5s\"\n",
		"gcs_sync_parallelism = 2\n",
	} {
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil || (!strings.Contains(err.Error(), "unknown configuration keys") && !strings.Contains(err.Error(), "type mismatch")) {
			t.Errorf("legacy scheduler key accepted: %s err=%v", contents, err)
		}
	}
}

func TestLoadConfigRejectsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unknown.toml")
	if err := os.WriteFile(path, []byte("[logging]\nverbose = true\ninert_key = 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "inert_key") {
		t.Fatalf("unknown/inert setting was not rejected: %v", err)
	}
}

func TestLoadConfigRequiresRepositoryAndLoggingTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	for _, contents := range []string{
		"repository = \"/old/path\"\n",
		"verbose = true\n",
		"repository = \"/old/path\"\nverbose = true\n",
	} {
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil || (!strings.Contains(err.Error(), "unknown configuration keys") && !strings.Contains(err.Error(), "type mismatch")) {
			t.Errorf("old top-level config keys accepted: %q err=%v", contents, err)
		}
	}
	if err := os.WriteFile(path, []byte("[repository]\npath = \"/new/path\"\n[logging]\nverbose = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil || cfg.Repository.Path != "/new/path" || !cfg.Logging.Verbose {
		t.Fatalf("nested repository/logging config=%+v err=%v", cfg, err)
	}
}

func TestGCSRecoveryDirectoryConfig(t *testing.T) {
	if got := (Config{}).GCSRecoveryDirectory(); got != "/recyclebin" {
		t.Fatalf("default recovery directory=%q", got)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[recovery]\ndirectory = \"/lost+found\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil || cfg.GCSRecoveryDirectory() != "/lost+found" {
		t.Fatalf("configured recovery directory=%q err=%v", cfg.GCSRecoveryDirectory(), err)
	}
}
