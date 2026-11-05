package runtimeconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadUsesSharedXDGPathAndExplicitMissingIsError(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, "shared-config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	if cfg, path, err := Load(""); err != nil || !reflect.DeepEqual(*cfg, Config{}) || path != filepath.Join(configHome, "nando", "config.toml") {
		t.Fatalf("default config cfg=%+v path=%q err=%v", cfg, path, err)
	}
	missing := filepath.Join(home, "missing.toml")
	if _, _, err := Load(missing); err == nil {
		t.Fatal("explicit nonexistent config was accepted")
	}
}

func TestResolveClientConfigPrecedenceTokenAndCA(t *testing.T) {
	t.Setenv("RUNTIME_CONFIG_TEST_TOKEN", "from-env")
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, []byte("ca-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		cfg        Config
		options    ClientOptions
		wantListen string
		wantToken  string
		wantCA     string
	}{
		{"TOML literal", Config{Daemon: DaemonConfig{Listen: "unix:/config", Token: "literal"}}, ClientOptions{}, "unix:/config", "literal", ""},
		{"TOML env and CA", Config{Daemon: DaemonConfig{Listen: "unix:/config", TokenFromEnv: "RUNTIME_CONFIG_TEST_TOKEN", CACert: caFile}}, ClientOptions{}, "unix:/config", "from-env", "ca-bytes"},
		{"CLI overrides", Config{Daemon: DaemonConfig{Listen: "unix:/config", Token: "old"}}, ClientOptions{Endpoint: "unix:/cli", TokenFile: tokenFile, CACert: caFile}, "unix:/cli", "from-file", "ca-bytes"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveClientConfig(&test.cfg, t.TempDir(), test.options)
			if err != nil {
				t.Fatal(err)
			}
			if got.Endpoint != test.wantListen || got.Token != test.wantToken || string(got.CACertPEM) != test.wantCA {
				t.Fatalf("client config=%+v, want endpoint=%q token=%q CA=%q", got, test.wantListen, test.wantToken, test.wantCA)
			}
		})
	}
}

func TestResolveClientConfigDefaultsToRepositorySocket(t *testing.T) {
	repo := t.TempDir()
	got, err := ResolveClientConfig(&Config{}, repo, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want, err := ResolveEndpoint(nil, repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Endpoint != want {
		t.Fatalf("default endpoint=%q, want %q", got.Endpoint, want)
	}
}

func TestLoadConfigReadsFUSEMountOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[fuse]\nmount_options = [\"allow_other\", \"ro\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"allow_other", "ro"}; !reflect.DeepEqual(cfg.Fuse.MountOptions, want) {
		t.Fatalf("mount options=%q, want %q", cfg.Fuse.MountOptions, want)
	}
}
