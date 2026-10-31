package commands_test

import (
	"bytes"
	"context"
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
