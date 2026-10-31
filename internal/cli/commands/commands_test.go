package commands_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/cli"
	"github.com/kamichidu/go-nando/internal/cli/commands"
	"github.com/kamichidu/go-nando/pkg/controlrpc"
)

func executeCLI(ctx context.Context, stdout, stderr *bytes.Buffer, args []string) int {
	subs := []flagshim.Command{
		&commands.InitCommand{},
		&commands.PutCommand{},
		&commands.GetCommand{},
		&commands.CatCommand{},
		&commands.LsCommand{},
		&commands.StatCommand{},
		&commands.MvCommand{},
		&commands.RmCommand{},
		&commands.GcCommand{},
		&commands.SyncCommand{},
		&commands.DaemonCommand{},
		&commands.DoctorCommand{},
		&commands.RecoveryCommand{},
	}
	root := cli.NewRootCommand(subs)

	// Prepend "nando" as argv[0] which flagshim.Run treats as program name and ignores.
	argv := append([]string{"nando"}, args...)

	return flagshim.Run(
		ctx,
		bytes.NewReader(nil),
		stdout,
		stderr,
		root,
		argv,
	)
}

func TestPutUsesDaemonResourceSession(t *testing.T) {
	repoDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := executeCLI(context.Background(), &stdout, &stderr, []string{"--repository", repoDir, "init"}); code != 0 {
		t.Fatalf("init repository: code=%d stderr=%q", code, stderr.String())
	}
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	endpoint, err := controlrpc.DefaultEndpoint(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	daemonCtx, cancelDaemon := context.WithCancel(context.Background())
	daemonResult := make(chan int, 1)
	go func() {
		var daemonOut, daemonErr bytes.Buffer
		daemonResult <- executeCLI(daemonCtx, &daemonOut, &daemonErr, []string{"--repository", repoDir, "daemon"})
	}()
	daemonStopped := false
	t.Cleanup(func() {
		if daemonStopped {
			return
		}
		cancelDaemon()
		select {
		case <-daemonResult:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	client, err := controlrpc.NewClient(controlrpc.ClientConfig{Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	readyOK := false
	for time.Now().Before(deadline) {
		var ready struct {
			Version string `json:"version"`
		}
		if err := client.Call(context.Background(), controlrpc.MethodPing, struct{}{}, &ready); err == nil && ready.Version != "" {
			readyOK = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !readyOK {
		t.Fatal("daemon did not become ready")
	}

	source := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(source, []byte("CLI daemon client payload"), 0600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := executeCLI(context.Background(), &stdout, &stderr, []string{"--repository", repoDir, "put", source, "/from-cli.txt"}); code != 0 {
		t.Fatalf("daemon-backed put: code=%d stderr=%q", code, stderr.String())
	}
	var entry struct {
		Revision uint64 `json:"revision"`
		Size     int64  `json:"size"`
	}
	if err := client.Call(context.Background(), controlrpc.MethodStat, map[string]string{"path": "/from-cli.txt"}, &entry); err != nil || entry.Revision != 1 || entry.Size != int64(len("CLI daemon client payload")) {
		t.Fatalf("daemon metadata after CLI put: entry=%+v err=%v", entry, err)
	}
	stdout.Reset()
	if code := executeCLI(context.Background(), &stdout, &stderr, []string{"--repository", repoDir, "stat", "/from-cli.txt"}); code != 0 || !strings.Contains(stdout.String(), "Revision:     1") {
		t.Fatalf("daemon-backed stat: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	if code := executeCLI(context.Background(), &stdout, &stderr, []string{"--repository", repoDir, "cat", "/from-cli.txt"}); code != 0 || stdout.String() != "CLI daemon client payload" {
		t.Fatalf("daemon-backed cat: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	exportPath := filepath.Join(t.TempDir(), "exported.txt")
	if code := executeCLI(context.Background(), &stdout, &stderr, []string{"--repository", repoDir, "get", "/from-cli.txt", exportPath}); code != 0 {
		t.Fatalf("daemon-backed get: code=%d stderr=%q", code, stderr.String())
	}
	exported, err := os.ReadFile(exportPath)
	if err != nil || string(exported) != "CLI daemon client payload" {
		t.Fatalf("exported bytes=%q err=%v", exported, err)
	}
	var stale controlrpc.BeginWriteResult
	if err := client.Call(context.Background(), controlrpc.MethodBeginWrite, map[string]any{"path": "/from-cli.txt", "resource_schemes": []string{"file"}}, &stale); err != nil {
		t.Fatal(err)
	}
	staleURL, err := url.Parse(stale.ResourceURI)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staleURL.Path, []byte("stale recoverable edit"), 0600); err != nil {
		t.Fatal(err)
	}
	winner := filepath.Join(t.TempDir(), "winner")
	if err := os.WriteFile(winner, []byte("new committed revision"), 0600); err != nil {
		t.Fatal(err)
	}
	if code := executeCLI(context.Background(), &stdout, &stderr, []string{"--repository", repoDir, "put", winner, "/from-cli.txt"}); code != 0 {
		t.Fatalf("create concurrent winner: code=%d stderr=%q", code, stderr.String())
	}
	if err := client.Call(context.Background(), controlrpc.MethodCommit, map[string]string{"session_id": stale.SessionID}, nil); err == nil {
		t.Fatal("stale write unexpectedly committed")
	}
	stdout.Reset()
	if code := executeCLI(context.Background(), &stdout, &stderr, []string{"--repository", repoDir, "recovery", "list"}); code != 0 || !strings.Contains(stdout.String(), stale.SessionID) || !strings.Contains(stdout.String(), "/from-cli.txt") {
		t.Fatalf("recovery list did not expose failed staging: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if code := executeCLI(context.Background(), &stdout, &stderr, []string{"--repository", repoDir, "recovery", "retry", stale.SessionID}); code == 0 {
		t.Fatal("recovery retry silently bypassed stale CAS")
	}
	if data, err := os.ReadFile(staleURL.Path); err != nil || string(data) != "stale recoverable edit" {
		t.Fatalf("failed recovery retry lost staging: data=%q err=%v", data, err)
	}
	if code := executeCLI(context.Background(), &stdout, &stderr, []string{"--repository", repoDir, "recovery", "discard", stale.SessionID}); code != 0 {
		t.Fatalf("explicit recovery discard: code=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(staleURL.Path); !os.IsNotExist(err) {
		t.Fatalf("explicit recovery discard retained staging: %v", err)
	}
	stdout.Reset()
	if code := executeCLI(context.Background(), &stdout, &stderr, []string{"--repository", repoDir, "ls", "/"}); code != 0 || !strings.Contains(stdout.String(), "from-cli.txt") {
		t.Fatalf("daemon-backed ls: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if code := executeCLI(context.Background(), &stdout, &stderr, []string{"--repository", repoDir, "mv", "/from-cli.txt", "/renamed.txt"}); code != 0 {
		t.Fatalf("daemon-backed mv: code=%d stderr=%q", code, stderr.String())
	}
	if code := executeCLI(context.Background(), &stdout, &stderr, []string{"--repository", repoDir, "rm", "/renamed.txt"}); code != 0 {
		t.Fatalf("daemon-backed rm: code=%d stderr=%q", code, stderr.String())
	}
	if code := executeCLI(context.Background(), &stdout, &stderr, []string{"--repository", repoDir, "gc"}); code != 0 {
		t.Fatalf("daemon-backed gc: code=%d stderr=%q", code, stderr.String())
	}
	if code := executeCLI(context.Background(), &stdout, &stderr, []string{"--repository", repoDir, "doctor"}); code != 0 {
		t.Fatalf("daemon-backed doctor: code=%d stderr=%q", code, stderr.String())
	}
	cancelDaemon()
	select {
	case code := <-daemonResult:
		daemonStopped = true
		if code != 0 {
			t.Fatalf("daemon exited with code %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not exit after cancellation")
	}
}

func TestTOMLConfigSelectsRepositoryAndSharedDaemonEndpoint(t *testing.T) {
	repoDir := filepath.Join(t.TempDir(), "configured-repository")
	socketDir := t.TempDir()
	// Use a test-owned absolute socket path as the configured endpoint.
	endpoint := "unix://" + filepath.Join(socketDir, "configured.sock")
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configText := fmt.Sprintf("repository = %q\nverbose = true\ndaemon_endpoint = %q\ndaemon_token_file = \"\"\ndaemon_ca_cert = \"\"\ndaemon_tls_cert = \"\"\ndaemon_tls_key = \"\"\n", repoDir, endpoint)
	if err := os.WriteFile(configPath, []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := executeCLI(context.Background(), &stdout, &stderr, []string{"--config", configPath, "init"}); code != 0 {
		t.Fatalf("TOML-selected init: code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "[verbose]") {
		t.Fatalf("TOML verbose=true did not affect CLI behavior: stderr=%q", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(repoDir, "metadata.sqlite")); err != nil {
		t.Fatalf("TOML repository path was not used: %v", err)
	}

	daemonCtx, cancelDaemon := context.WithCancel(context.Background())
	daemonResult := make(chan int, 1)
	go func() {
		var daemonOut, daemonErr bytes.Buffer
		daemonResult <- executeCLI(daemonCtx, &daemonOut, &daemonErr, []string{"--config", configPath, "daemon"})
	}()
	defer func() {
		cancelDaemon()
		select {
		case <-daemonResult:
		case <-time.After(5 * time.Second):
			t.Error("configured daemon did not stop")
		}
	}()
	client, err := controlrpc.NewClient(controlrpc.ClientConfig{Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var ready struct {
			Version string `json:"version"`
		}
		if err := client.Call(context.Background(), controlrpc.MethodPing, struct{}{}, &ready); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var ready struct {
		Version string `json:"version"`
	}
	if err := client.Call(context.Background(), controlrpc.MethodPing, struct{}{}, &ready); err != nil {
		t.Fatalf("daemon did not listen on TOML endpoint %q: %v", endpoint, err)
	}
	source := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(source, []byte("TOML config integration"), 0600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := executeCLI(context.Background(), &stdout, &stderr, []string{"--config", configPath, "put", source, "/configured.txt"}); code != 0 {
		t.Fatalf("CLI did not share TOML repository/endpoint: code=%d stderr=%q", code, stderr.String())
	}
	var entry controlrpc.Entry
	if err := client.Call(context.Background(), controlrpc.MethodStat, map[string]string{"path": "/configured.txt"}, &entry); err != nil || entry.Size != int64(len("TOML config integration")) {
		t.Fatalf("configured repository state: entry=%+v err=%v", entry, err)
	}
	overrideEndpoint := "unix://" + filepath.Join(t.TempDir(), "cli-override.sock")
	stdout.Reset()
	stderr.Reset()
	if code := executeCLI(context.Background(), &stdout, &stderr, []string{"--config", configPath, "--daemon-endpoint", overrideEndpoint, "stat", "/configured.txt"}); code == 0 || !strings.Contains(stderr.String(), strings.TrimPrefix(overrideEndpoint, "unix://")) {
		t.Fatalf("CLI endpoint did not override TOML endpoint: code=%d stderr=%q", code, stderr.String())
	}

	cliOverride := filepath.Join(t.TempDir(), "cli-override")
	stdout.Reset()
	stderr.Reset()
	if code := executeCLI(context.Background(), &stdout, &stderr, []string{"--config", configPath, "--repository", cliOverride, "--verbose=false", "init"}); code != 0 {
		t.Fatalf("CLI repository override over TOML: code=%d stderr=%q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "[verbose]") {
		t.Fatalf("CLI verbose=false did not override TOML verbose=true: stderr=%q", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(cliOverride, "metadata.sqlite")); err != nil {
		t.Fatalf("CLI repository override was ignored: %v", err)
	}
}

func TestLegacyDefaultRepositoryRequiresExplicitSelection(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, ".config")
	dataHome := filepath.Join(home, ".local", "share")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	legacyRepo := filepath.Join(configHome, "nando")
	var stdout, stderr bytes.Buffer
	if code := executeCLI(context.Background(), &stdout, &stderr, []string{"--repository", legacyRepo, "init"}); code != 0 {
		t.Fatalf("prepare legacy initialized repository: code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := executeCLI(context.Background(), &stdout, &stderr, []string{"stat", "/"}); code == 0 || !strings.Contains(stderr.String(), legacyRepo) {
		t.Fatalf("legacy repository was not guarded: code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := executeCLI(context.Background(), &stdout, &stderr, []string{"--repository", legacyRepo, "init"}); code != 0 {
		t.Fatalf("explicit legacy repository selection failed: code=%d stderr=%q", code, stderr.String())
	}
}

func TestTOMLConfigTCPAuthenticationAndTLS(t *testing.T) {
	repoDir := filepath.Join(t.TempDir(), "repo")
	certPEM, certPath, keyPath := writeCommandTestCertificate(t)
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("nando-test-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "tcp://" + probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "tcp-config.toml")
	configText := fmt.Sprintf("repository = %q\nverbose = false\ndaemon_endpoint = %q\ndaemon_token_file = %q\ndaemon_ca_cert = %q\ndaemon_tls_cert = %q\ndaemon_tls_key = %q\n", repoDir, endpoint, tokenPath, certPath, certPath, keyPath)
	if err := os.WriteFile(configPath, []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := executeCLI(context.Background(), &stdout, &stderr, []string{"--config", configPath, "init"}); code != 0 {
		t.Fatalf("init configured TCP repository: code=%d stderr=%q", code, stderr.String())
	}
	daemonCtx, cancelDaemon := context.WithCancel(context.Background())
	daemonResult := make(chan int, 1)
	go func() {
		var daemonOut, daemonErr bytes.Buffer
		daemonResult <- executeCLI(daemonCtx, &daemonOut, &daemonErr, []string{"--config", configPath, "daemon"})
	}()
	defer func() {
		cancelDaemon()
		select {
		case <-daemonResult:
		case <-time.After(5 * time.Second):
			t.Error("configured TCP daemon did not stop")
		}
	}()
	source := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(source, []byte("TLS configured Resource"), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		stdout.Reset()
		stderr.Reset()
		code := executeCLI(context.Background(), &stdout, &stderr, []string{"--config", configPath, "put", source, "/secure.txt"})
		if code == 0 {
			return
		}
		lastErr = fmt.Errorf("CLI put returned %d: %s", code, stderr.String())
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("TOML TLS/auth settings did not connect daemon and CLI: %v (CA bytes=%d)", lastErr, len(certPEM))
}

func writeCommandTestCertificate(t *testing.T) ([]byte, string, string) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key")
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	return certPEM, certPath, keyPath
}

func TestCLI(t *testing.T) {
	ctx := context.Background()

	t.Run("invalid arguments returns exit code 2", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := executeCLI(ctx, &stdout, &stderr, []string{"put"}) // missing both arguments
		if code != 2 {
			t.Errorf("expected exit code 2, got %d. Stderr: %q", code, stderr.String())
		}
	})

	t.Run("non-existent path returns exit code 3", func(t *testing.T) {
		tempRepo := t.TempDir()
		var stdout, stderr bytes.Buffer

		// Initialize first
		initCode := executeCLI(ctx, &stdout, &stderr, []string{"--repository", tempRepo, "init"})
		if initCode != 0 {
			t.Fatalf("failed to init repo: %s", stderr.String())
		}
		stopDaemon := startTestDaemon(t, tempRepo)
		defer stopDaemon()

		stdout.Reset()
		stderr.Reset()

		// Query stat of non-existent path
		code := executeCLI(ctx, &stdout, &stderr, []string{"--repository", tempRepo, "stat", "/missing.txt"})
		if code != 3 {
			t.Errorf("expected exit code 3 for non-existent path, got %d. Stderr: %q", code, stderr.String())
		}
	})

	t.Run("--repository flag is reflected correctly", func(t *testing.T) {
		tempRepo := t.TempDir()
		var stdout, stderr bytes.Buffer

		code := executeCLI(ctx, &stdout, &stderr, []string{"--repository", tempRepo, "init"})
		if code != 0 {
			t.Fatalf("failed to init repository with custom --repository: %s", stderr.String())
		}

		dbPath := filepath.Join(tempRepo, "metadata.sqlite")
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			t.Errorf("expected metadata.sqlite to exist at %s, but it was not found", dbPath)
		}
		coordinationDBPath := filepath.Join(tempRepo, "coordination.db")
		if _, err := os.Stat(coordinationDBPath); os.IsNotExist(err) {
			t.Errorf("expected coordination.db to exist at %s, but it was not found", coordinationDBPath)
		}
	})
}

func startTestDaemon(t *testing.T, repoDir string) func() {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "runtime"))
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan int, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		result <- executeCLI(ctx, &stdout, &stderr, []string{"--repository", repoDir, "daemon"})
	}()
	endpoint, err := controlrpc.DefaultEndpoint(repoDir)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	client, err := controlrpc.NewClient(controlrpc.ClientConfig{Endpoint: endpoint})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var ready struct {
			Version string `json:"version"`
		}
		if err := client.Call(context.Background(), controlrpc.MethodPing, struct{}{}, &ready); err == nil {
			return func() {
				cancel()
				select {
				case code := <-result:
					if code != 0 {
						t.Errorf("daemon stopped with exit code %d", code)
					}
				case <-time.After(5 * time.Second):
					t.Error("daemon did not stop")
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	t.Fatal("daemon did not become ready")
	return func() {}
}
