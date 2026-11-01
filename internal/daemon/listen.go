package daemon

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
)

// ListenConfig describes a daemon control listener. TCP is secure by default:
// it cannot be opened without TLS certificate/key and bearer authentication.
type ListenConfig struct {
	Endpoint string
	CertFile string
	KeyFile  string
	Token    string
}

func listen(config ListenConfig) (net.Listener, error) {
	u, err := url.Parse(config.Endpoint)
	if err != nil || u.Scheme == "" {
		return nil, fmt.Errorf("invalid daemon listen endpoint %q", config.Endpoint)
	}
	switch u.Scheme {
	case "unix":
		socketPath, err := unixSocketPath(u)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(socketPath), 0700); err != nil {
			return nil, fmt.Errorf("create socket directory: %w", err)
		}
		listener, err := net.Listen("unix", socketPath)
		if err != nil {
			return nil, fmt.Errorf("listen on Unix socket: %w", err)
		}
		if err := os.Chmod(socketPath, 0600); err != nil {
			return nil, errors.Join(fmt.Errorf("restrict Unix socket permissions: %w", err), listener.Close(), os.Remove(socketPath))
		}
		return listener, nil
	case "tcp":
		if u.Host == "" || config.CertFile == "" || config.KeyFile == "" || config.Token == "" {
			return nil, fmt.Errorf("tcp listener requires host, TLS certificate, TLS key, and bearer token")
		}
		certificate, err := tls.LoadX509KeyPair(config.CertFile, config.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load daemon TLS certificate: %w", err)
		}
		listener, err := net.Listen("tcp", u.Host)
		if err != nil {
			return nil, fmt.Errorf("listen on TCP: %w", err)
		}
		return tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}), nil
	default:
		return nil, fmt.Errorf("unsupported daemon listen scheme %q", u.Scheme)
	}
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
