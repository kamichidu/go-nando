package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRequiresExplicitConfigToExist(t *testing.T) {
	err := run([]string{"-config", filepath.Join(t.TempDir(), "missing.toml"), "-listen", "127.0.0.1:0"})
	if err == nil || !strings.Contains(err.Error(), "load configuration") {
		t.Fatalf("explicit missing config error=%v", err)
	}
}

func TestRunRepositoryFlagOverridesConfiguredRepository(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	configFile := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configFile, []byte("[repository]\npath = \"/configured/repository\"\n[daemon]\nlisten = \"unix:/configured/socket\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"-config", configFile, "-repository", t.TempDir(), "-listen", "invalid"})
	if err == nil || !strings.Contains(err.Error(), "invalid WebDAV listen address") {
		t.Fatalf("expected bind validation after repository/config resolution, got %v", err)
	}
}

func TestValidateLoopbackAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8080", "[::1]:8080", "localhost:8080"} {
		t.Run("accept_"+address, func(t *testing.T) {
			if err := validateLoopbackAddress(address); err != nil {
				t.Fatalf("loopback address rejected: %v", err)
			}
		})
	}
	for _, address := range []string{"0.0.0.0:8080", "192.0.2.1:8080", "example.com:8080", "localhost.evil:8080", "8080"} {
		t.Run("reject_"+address, func(t *testing.T) {
			if err := validateLoopbackAddress(address); err == nil {
				t.Fatalf("non-loopback address %q accepted", address)
			}
		})
	}
}
