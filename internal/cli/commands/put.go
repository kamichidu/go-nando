package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"

	"github.com/kamichidu/go-flagshim"
	"github.com/kamichidu/go-nando/internal/cli"
	"github.com/kamichidu/go-nando/internal/paths"
	"github.com/kamichidu/go-nando/pkg/controlrpc"
)

// PutCommand represents the "nando put" subcommand.
type PutCommand struct{}

// Name returns the subcommand name.
func (c *PutCommand) Name() string {
	return "put"
}

// RegisterFlags defines subcommand-specific options. None for put.
func (c *PutCommand) RegisterFlags(ctx context.Context, fs *flagshim.FlagSet) context.Context {
	return ctx
}

// Run executes importing a local file into the logical repository.
func (c *PutCommand) Run(ctx context.Context, args []string) (runErr error) {
	if len(args) < 2 {
		return fmt.Errorf("%w: nando put <local-file> <repository-path>", cli.ErrInvalidArguments)
	}

	localPath := args[0]
	rawRepoPath := args[1]

	repoPath, err := paths.NormalizeLogicalPath(rawRepoPath)
	if err != nil {
		return fmt.Errorf("%w: %v", cli.ErrInvalidArguments, err)
	}

	client, err := cli.NewDaemonClientFromContext(ctx)
	if err != nil {
		return err
	}

	cli.LogVerbose(ctx, "Importing file %q to logical repository path %q", localPath, repoPath)
	var begin controlrpc.BeginWriteResult
	if err := client.Call(ctx, controlrpc.MethodBeginWrite, map[string]any{"path": repoPath, "resource_schemes": []string{"file"}}, &begin); err != nil {
		return err
	}
	sessionCtx, stopRenewal := startSessionRenewal(ctx, client, begin.SessionID)
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		runErr = errors.Join(runErr, client.Call(cleanupCtx, controlrpc.MethodRelease, map[string]string{"session_id": begin.SessionID}, nil))
	}()
	defer func() { runErr = errors.Join(runErr, stopRenewal()) }()
	if err := copyToResource(sessionCtx, localPath, begin.ResourceURI); err != nil {
		renewalErr := stopRenewal()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return errors.Join(err, renewalErr, client.Call(cleanupCtx, controlrpc.MethodAbort, map[string]string{"session_id": begin.SessionID}, nil))
	}
	if err := stopRenewal(); err != nil {
		return err
	}
	if err := client.Call(ctx, controlrpc.MethodCommit, map[string]string{"session_id": begin.SessionID}, nil); err != nil {
		// Commit errors retain the daemon's durable staging record for explicit
		// recovery. Deferred Release closes the client session without discarding it.
		return err
	}
	return nil
}

func copyToResource(ctx context.Context, sourcePath, resourceURI string) (retErr error) {
	u, err := url.Parse(resourceURI)
	if err != nil || u.Scheme != "file" || u.Host != "" || u.Path == "" {
		return fmt.Errorf("daemon returned unsupported or inaccessible Resource URI %q", resourceURI)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open source file: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, source.Close()) }()
	target, err := os.OpenFile(u.Path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return fmt.Errorf("open daemon staging Resource: %w", err)
	}
	_, copyErr := io.Copy(target, contextReader{ctx: ctx, reader: source})
	var syncErr error
	if copyErr == nil {
		syncErr = target.Sync()
	}
	closeErr := target.Close()
	if copyErr != nil {
		copyErr = fmt.Errorf("copy source into daemon Resource: %w", copyErr)
	}
	if syncErr != nil {
		syncErr = fmt.Errorf("sync daemon Resource: %w", syncErr)
	}
	return errors.Join(copyErr, syncErr, closeErr)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
