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

func TestLoadConfigReadsS3RemoteSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	contents := "[remote]\nremote_uri = \"s3://archive/prefix/\"\n[remote.s3]\nendpoint = \"http://127.0.0.1:9000\"\nregion = \"us-east-1\"\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Remote.URI != "s3://archive/prefix/" || cfg.Remote.S3.Endpoint != "http://127.0.0.1:9000" || cfg.Remote.S3.Region != "us-east-1" {
		t.Fatalf("S3 remote settings=%+v", cfg.Remote)
	}
}

func TestLoadConfigRejectsLegacyS3Settings(t *testing.T) {
	for _, contents := range []string{
		"[remote]\nendpoint = \"http://127.0.0.1:9000\"\n",
		"[remote]\naccess_key_id = \"access\"\n",
		"[remote]\nsecret_access_key = \"secret\"\n",
		"[remote]\nsession_token = \"session\"\n",
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Errorf("legacy S3 setting accepted: %s", contents)
		}
	}
}

func TestS3SettingsAreRejectedForGCSBackend(t *testing.T) {
	cfg := Config{Remote: RemoteConfig{S3: S3Config{Endpoint: "http://localhost:9000"}}}
	if err := cfg.ValidateRemoteBackend("gcs"); err == nil {
		t.Fatal("S3 settings were accepted for GCS backend")
	}
	if err := cfg.ValidateRemoteBackend("s3"); err != nil {
		t.Fatalf("S3 settings rejected for S3 backend: %v", err)
	}
	if err := (Config{}).ValidateRemoteBackend("gcs"); err != nil {
		t.Fatalf("empty S3 settings rejected for GCS backend: %v", err)
	}
}
