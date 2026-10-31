package commands_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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
