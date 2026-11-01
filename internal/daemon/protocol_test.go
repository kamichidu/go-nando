package daemon

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kamichidu/go-nando/controlrpc"
)

type echoAPI struct{}

func (echoAPI) Call(_ context.Context, method string, params json.RawMessage) (any, error) {
	if method != "echo" {
		return nil, errors.New("unknown method")
	}
	var value map[string]string
	if err := json.Unmarshal(params, &value); err != nil {
		return nil, err
	}
	return value["value"], nil
}

func TestUnixJSONRPCClientRequiresBearerTokenWhenConfigured(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "socket")
	socketPath := filepath.Join(parent, "nando.sock")
	endpoint := "unix://" + socketPath
	listener, err := listen(ListenConfig{Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	assertPermissionMode(t, parent, 0700)
	assertPermissionMode(t, socketPath, 0600)
	server := &http.Server{Handler: newHandler(echoAPI{}, "secret")}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	unauthorized, err := controlrpc.NewClient(controlrpc.ClientConfig{Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	if err := unauthorized.Call(context.Background(), "echo", map[string]string{"value": "no"}, nil); err == nil {
		t.Fatal("request without bearer token succeeded")
	}

	client, err := controlrpc.NewClient(controlrpc.ClientConfig{Endpoint: endpoint, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	var got string
	if err := client.Call(context.Background(), "echo", map[string]string{"value": "ok"}, &got); err != nil || got != "ok" {
		t.Fatalf("JSON-RPC call: got=%q err=%v", got, err)
	}

	if err := server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Errorf("close HTTP server: %v", err)
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Errorf("remove test socket: %v", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Errorf("serve: %v", err)
	}
}

func TestUnixListenerRejectsSharedParentDirectory(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := listen(ListenConfig{Endpoint: "unix://" + filepath.Join(parent, "nando.sock")}); err == nil || !strings.Contains(err.Error(), "must be private") {
		t.Fatalf("listener accepted a non-private parent directory: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(parent, "nando.sock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket was created under a shared parent: %v", err)
	}
}

func TestDefaultEndpointRoundTripsThroughListenerAndClient(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	appDir := filepath.Join(runtimeDir, "nando")
	if err := os.Mkdir(appDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(appDir, 0755); err != nil {
		t.Fatal(err)
	}
	endpoint, err := controlrpc.DefaultEndpoint(filepath.Join(t.TempDir(), "repo with spaces"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(endpoint, "unix:") {
		t.Fatalf("default endpoint is not a Unix URI: %q", endpoint)
	}
	assertPermissionMode(t, appDir, 0700)
	listener, err := listen(ListenConfig{Endpoint: endpoint})
	if err != nil {
		t.Fatalf("listen on DefaultEndpoint result %q: %v", endpoint, err)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	socketPath := parsed.Opaque
	if socketPath == "" {
		socketPath = parsed.Path
	}
	assertPermissionMode(t, socketPath, 0600)
	server := &http.Server{Handler: newHandler(echoAPI{}, "")}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	client, err := controlrpc.NewClient(controlrpc.ClientConfig{Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	var got string
	if err := client.Call(context.Background(), "echo", map[string]string{"value": "default-endpoint"}, &got); err != nil || got != "default-endpoint" {
		t.Fatalf("JSON-RPC call through default endpoint: got=%q err=%v", got, err)
	}
	if err := server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Errorf("close HTTP server: %v", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Errorf("serve: %v", err)
	}
}

func assertPermissionMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode=%04o, want %04o", path, got, want)
	}
}

func TestTCPListenerRefusesMissingTLSAndAuthentication(t *testing.T) {
	_, err := listen(ListenConfig{Endpoint: "tcp://127.0.0.1:0"})
	if err == nil || !strings.Contains(fmt.Sprint(err), "requires") {
		t.Fatalf("expected TCP security configuration error, got %v", err)
	}
}

func TestTCPJSONRPCUsesTLSAndBearerAuthentication(t *testing.T) {
	certPEM, certPath, keyPath := writeTestCertificate(t)
	listener, err := listen(ListenConfig{Endpoint: "tcp://127.0.0.1:0", CertFile: certPath, KeyFile: keyPath, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: newHandler(echoAPI{}, "secret")}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	if _, err := controlrpc.NewClient(controlrpc.ClientConfig{Endpoint: "tcp://" + listener.Addr().String()}); err == nil {
		t.Fatal("TCP client without bearer token was accepted")
	}
	client, err := controlrpc.NewClient(controlrpc.ClientConfig{Endpoint: "tcp://" + listener.Addr().String(), Token: "secret", CACertPEM: certPEM})
	if err != nil {
		t.Fatal(err)
	}
	var got string
	if err := client.Call(context.Background(), "echo", map[string]string{"value": "tls-ok"}, &got); err != nil || got != "tls-ok" {
		t.Fatalf("TLS JSON-RPC call: got=%q err=%v", got, err)
	}
	if err := server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Errorf("close HTTP server: %v", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Errorf("serve: %v", err)
	}
}

func writeTestCertificate(t *testing.T) ([]byte, string, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "server.pem"), filepath.Join(dir, "server-key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	return certPEM, certPath, keyPath
}
