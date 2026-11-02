package controlrpc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const protocolVersion = "2.0"

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ClientConfig selects the control transport. TCP requires TLS and a bearer
// token; UDS is local and relies on socket permissions.
type ClientConfig struct {
	Endpoint  string
	Token     string
	CACertPEM []byte
}

type Client struct {
	httpClient *http.Client
	endpoint   string
	token      string
	nextID     atomic.Uint64
}

func NewClient(config ClientConfig) (*Client, error) {
	u, err := url.Parse(config.Endpoint)
	if err != nil || u.Scheme == "" {
		return nil, fmt.Errorf("invalid daemon endpoint %q", config.Endpoint)
	}
	transport := &http.Transport{ResponseHeaderTimeout: 30 * time.Second}
	endpoint := "http://nando/rpc"
	switch u.Scheme {
	case "unix":
		socketPath, err := unixSocketPath(u)
		if err != nil {
			return nil, err
		}
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		}
	case "tcp":
		if u.Host == "" || config.Token == "" {
			return nil, fmt.Errorf("tcp endpoint requires host and bearer token")
		}
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if len(config.CACertPEM) > 0 && !roots.AppendCertsFromPEM(config.CACertPEM) {
			return nil, fmt.Errorf("daemon CA file contains no certificates")
		}
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
		endpoint = "https://" + u.Host + "/rpc"
	default:
		return nil, fmt.Errorf("unsupported daemon endpoint scheme %q", u.Scheme)
	}
	return &Client{httpClient: &http.Client{Transport: transport}, endpoint: endpoint, token: config.Token}, nil
}

func (c *Client) Call(ctx context.Context, method string, params any, result any) (retErr error) {
	if method == "" {
		return fmt.Errorf("JSON-RPC method is empty")
	}
	id := c.nextID.Add(1)
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return fmt.Errorf("encode JSON-RPC request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("call daemon: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, resp.Body.Close()) }()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return &RemoteError{Code: CodePermissionDenied, Message: "daemon authorization denied"}
		}
		return fmt.Errorf("daemon returned HTTP %s", resp.Status)
	}
	var decoded struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *rpcError       `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&decoded); err != nil {
		return fmt.Errorf("decode JSON-RPC response: %w", err)
	}
	if decoded.JSONRPC != protocolVersion {
		return fmt.Errorf("daemon returned unsupported JSON-RPC version %q", decoded.JSONRPC)
	}
	if !bytes.Equal(bytes.TrimSpace(decoded.ID), []byte(strconv.FormatUint(id, 10))) {
		return fmt.Errorf("daemon returned mismatched JSON-RPC response ID %s", decoded.ID)
	}
	if decoded.Error != nil {
		return &RemoteError{Code: decoded.Error.Code, Message: decoded.Error.Message}
	}
	if result != nil && len(decoded.Result) > 0 {
		if err := json.Unmarshal(decoded.Result, result); err != nil {
			return fmt.Errorf("decode JSON-RPC result: %w", err)
		}
	}
	return nil
}

// ReadTokenFile reads a single-line bearer token without trimming interior
// characters; surrounding whitespace is removed.
func ReadTokenFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("bearer token file %q must not be accessible by group or others", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("bearer token file is empty")
	}
	return token, nil
}

// ResolveToken resolves one configured bearer-token source. An unset
// environment variable produces an empty token, preserving unauthenticated
// local-socket behavior. The caller must not include the returned token in
// diagnostics.
func ResolveToken(literal, file, envName string) (string, error) {
	count := 0
	for _, source := range []string{literal, file, envName} {
		if source != "" {
			count++
		}
	}
	if count > 1 {
		return "", errors.New("daemon token sources are mutually exclusive")
	}
	if literal != "" {
		return literal, nil
	}
	if file != "" {
		return ReadTokenFile(file)
	}
	if envName != "" {
		return os.Getenv(envName), nil
	}
	return "", nil
}

// DefaultEndpoint returns a per-user local socket endpoint for one repository.
// The opaque unix:/absolute/path form avoids URL authority parsing ambiguity.
func DefaultEndpoint(repoDir string) (string, error) {
	absolute, err := filepath.Abs(repoDir)
	if err != nil {
		return "", err
	}
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base, err = os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("resolve runtime directory: %w", err)
		}
		base = filepath.Join(base, "nando", "run")
	} else {
		base = filepath.Join(base, "nando")
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		return "", fmt.Errorf("create daemon socket directory: %w", err)
	}
	if err := os.Chmod(base, 0700); err != nil {
		return "", fmt.Errorf("restrict daemon socket directory permissions: %w", err)
	}
	digest := sha256.Sum256([]byte(filepath.Clean(absolute)))
	socketPath := filepath.Join(base, fmt.Sprintf("%x.sock", digest[:10]))
	return (&url.URL{Scheme: "unix", Opaque: socketPath}).String(), nil
}

func unixSocketPath(u *url.URL) (string, error) {
	if u.Opaque != "" {
		if u.Path != "" || u.Host != "" || !filepath.IsAbs(u.Opaque) {
			return "", fmt.Errorf("unix endpoint must contain an absolute socket path")
		}
		return u.Opaque, nil
	}
	if u.Host != "" || !filepath.IsAbs(u.Path) {
		return "", fmt.Errorf("unix endpoint must be unix:/absolute/path")
	}
	return u.Path, nil
}
