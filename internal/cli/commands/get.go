package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/controlrpc"
	"github.com/kamichidu/go-nando/internal/cli"
	"github.com/kamichidu/go-nando/internal/paths"
)

// GetCommand represents the "nando get" subcommand.
type GetCommand struct{}

// Name returns the subcommand name.
func (c *GetCommand) Name() string {
	return "get"
}

// RegisterFlags defines subcommand-specific options.
func (c *GetCommand) RegisterFlags(ctx context.Context, fs *flagshim.FlagSet) context.Context {
	return ctx
}

// Run executes exporting a logical file from the repository back to local filesystem.
func (c *GetCommand) Run(ctx context.Context, args []string) (runErr error) {
	if len(args) < 2 {
		return fmt.Errorf("%w: nando get <repository-path> <local-file>", cli.ErrInvalidArguments)
	}

	rawRepoPath := args[0]
	localPath := args[1]

	repoPath, err := paths.NormalizeLogicalPath(rawRepoPath)
	if err != nil {
		return fmt.Errorf("%w: %v", cli.ErrInvalidArguments, err)
	}

	client, err := cli.NewDaemonClientFromContext(ctx)
	if err != nil {
		return err
	}

	cli.LogVerbose(ctx, "Exporting logical repository path %q to local file %q", repoPath, localPath)

	read, err := beginReadResource(ctx, client, repoPath)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, releaseResource(client, read.SessionID)) }()
	sessionCtx, stopRenewal := startSessionRenewal(ctx, client, read.SessionID)
	defer func() { runErr = errors.Join(runErr, stopRenewal()) }()
	u, err := url.Parse(read.ResourceURI)
	if err != nil || u.Scheme != "file" || u.Host != "" || u.Path == "" {
		return fmt.Errorf("daemon returned unsupported or inaccessible Resource URI %q", read.ResourceURI)
	}
	parent := filepath.Dir(localPath)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}
	if info, err := os.Lstat(localPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || info.IsDir() || !info.Mode().IsRegular() {
			return fmt.Errorf("export destination %q is not a replaceable regular file", localPath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect export destination: %w", err)
	}
	source, err := os.Open(u.Path)
	if err != nil {
		return fmt.Errorf("open daemon read Resource: %w", err)
	}
	temp, err := os.CreateTemp(parent, ".nando-get-*")
	if err != nil {
		return errors.Join(err, source.Close())
	}
	tempPath := temp.Name()
	published := false
	defer func() {
		if !published {
			runErr = errors.Join(runErr, os.Remove(tempPath))
		}
	}()
	_, copyErr := io.Copy(temp, contextReader{ctx: sessionCtx, reader: source})
	var finalizeErr error
	if copyErr == nil {
		finalizeErr = errors.Join(temp.Chmod(os.FileMode(read.Entry.Mode).Perm()), os.Chtimes(tempPath, time.Now(), time.Unix(0, read.Entry.Mtime)))
		if finalizeErr == nil {
			finalizeErr = temp.Sync()
		}
	}
	closeErr := errors.Join(temp.Close(), source.Close())
	if err := errors.Join(copyErr, finalizeErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(tempPath, localPath); err != nil {
		return fmt.Errorf("publish exported file: %w", err)
	}
	published = true
	return nil
}

func beginReadResource(ctx context.Context, client *controlrpc.Client, repositoryPath string) (controlrpc.BeginReadResult, error) {
	var result controlrpc.BeginReadResult
	err := client.Call(ctx, controlrpc.MethodBeginRead, map[string]any{"path": repositoryPath, "resource_schemes": []string{"file"}}, &result)
	return result, err
}

func releaseResource(client *controlrpc.Client, sessionID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return client.Call(ctx, controlrpc.MethodRelease, map[string]string{"session_id": sessionID}, nil)
}
