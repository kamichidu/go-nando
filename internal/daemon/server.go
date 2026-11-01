package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/kamichidu/go-nando/internal/app"
)

// Serve runs the daemon control plane. The caller must hold the repository
// process lock for the full lifetime of Serve.
func Serve(ctx context.Context, application *app.App, repoDir string, listenConfig ListenConfig) (retErr error) {
	sessions, err := newSessionManager(application, repoDir)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, sessions.close()) }()

	listener, err := listen(listenConfig)
	if err != nil {
		return err
	}
	endpoint, err := url.Parse(listenConfig.Endpoint)
	if err != nil {
		return errors.Join(err, listener.Close())
	}
	defer func() {
		closeErr := listener.Close()
		if errors.Is(closeErr, net.ErrClosed) {
			closeErr = nil
		}
		if endpoint.Scheme == "unix" {
			socketPath := endpoint.Path
			if endpoint.Opaque != "" {
				socketPath = endpoint.Opaque
			}
			closeErr = errors.Join(closeErr, removeUnixSocket(socketPath))
		}
		retErr = errors.Join(retErr, closeErr)
	}()

	httpServer := &http.Server{
		Handler:           newHandler(newService(application, sessions), listenConfig.Token),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- httpServer.Serve(listener) }()
	select {
	case err := <-serveResult:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("daemon control server: %w", err)
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr := httpServer.Shutdown(shutdownCtx)
		if shutdownErr != nil {
			shutdownErr = errors.Join(shutdownErr, httpServer.Close())
		}
		serveErr := <-serveResult
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(shutdownErr, serveErr)
	}
}

func removeUnixSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to remove non-socket daemon endpoint %q", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove daemon socket: %w", err)
	}
	return nil
}

func IsTCPListenEndpoint(endpoint string) bool {
	return strings.HasPrefix(endpoint, "tcp://")
}
