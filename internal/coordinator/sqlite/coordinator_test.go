package sqlite_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/kamichidu/go-nando/internal/coordinator/sqlite"
)

func TestExclusiveSerializesInstancesAndHonorsCancellation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "coordination.db")
	first := openCoordinator(t, dbPath)
	second := openCoordinator(t, dbPath)

	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- first.Exclusive(context.Background(), func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	secondCalled := false
	err := second.Exclusive(ctx, func() error {
		secondCalled = true
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline while waiting for SQLite writer lock, got %v", err)
	}
	if !errors.Is(err, sqlite.ErrAcquire) {
		t.Fatalf("expected lock acquisition category, got %v", err)
	}
	if secondCalled {
		t.Fatal("operation ran while another instance held the coordination lock")
	}
	cancelCtx, cancelWait := context.WithCancel(context.Background())
	cancelDone := make(chan error, 1)
	go func() {
		cancelDone <- second.Exclusive(cancelCtx, func() error {
			t.Error("operation ran after its waiter context was canceled")
			return nil
		})
	}()
	time.Sleep(40 * time.Millisecond)
	cancelWait()
	if err := <-cancelDone; !errors.Is(err, context.Canceled) || !errors.Is(err, sqlite.ErrAcquire) {
		t.Fatalf("expected canceled lock wait, got %v", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first operation failed: %v", err)
	}
	if err := second.Exclusive(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("second operation did not acquire released lock: %v", err)
	}
}

func TestExclusiveReleasesAfterErrorAndPanic(t *testing.T) {
	coordinator := openCoordinator(t, filepath.Join(t.TempDir(), "coordination.db"))
	wantErr := errors.New("operation failed")
	if err := coordinator.Exclusive(context.Background(), func() error { return wantErr }); !errors.Is(err, wantErr) || !errors.Is(err, sqlite.ErrOperation) {
		t.Fatalf("expected operation error, got %v", err)
	}

	func() {
		defer func() {
			if got := recover(); got != "operation panic" {
				t.Fatalf("expected operation panic to propagate, got %v", got)
			}
		}()
		if err := coordinator.Exclusive(context.Background(), func() error { panic("operation panic") }); err != nil {
			t.Fatalf("unexpected return before panic: %v", err)
		}
	}()

	if err := coordinator.Exclusive(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("lock was not released after rollback: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := coordinator.Exclusive(ctx, func() error {
		cancel()
		return nil
	}); err != nil {
		t.Fatalf("successful callback should remain successful when context is canceled afterwards: %v", err)
	}
	if err := coordinator.Exclusive(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("lock was not released after cancellation: %v", err)
	}

	callbackCtx, cancelCallback := context.WithCancel(context.Background())
	if err := coordinator.Exclusive(callbackCtx, func() error {
		cancelCallback()
		return callbackCtx.Err()
	}); !errors.Is(err, context.Canceled) || !errors.Is(err, sqlite.ErrOperation) {
		t.Fatalf("expected callback cancellation to be an operation failure, got %v", err)
	}
	if err := coordinator.Exclusive(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("lock was not released after callback cancellation: %v", err)
	}
}

func TestExclusiveAcrossProcessesAndProcessTermination(t *testing.T) {
	if os.Getenv("GO_NANDO_COORDINATOR_HELPER") == "1" {
		runCoordinatorProcessHelper(t)
		return
	}

	dbPath := filepath.Join(t.TempDir(), "coordination.db")
	readyPath := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestExclusiveAcrossProcessesAndProcessTermination$", "-test.v")
	cmd.Env = append(os.Environ(), "GO_NANDO_COORDINATOR_HELPER=1", "GO_NANDO_COORDINATOR_DB="+dbPath, "GO_NANDO_COORDINATOR_READY="+readyPath)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start coordinator helper process: %v", err)
	}
	defer func() {
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("kill helper process during cleanup: %v", err)
		}
	}()

	childExited := make(chan error, 1)
	go func() { childExited <- cmd.Wait() }()
	waitForFile(t, readyPath)

	second := openCoordinator(t, dbPath)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := second.Exclusive(ctx, func() error { return nil }); !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, sqlite.ErrAcquire) {
		t.Fatalf("separate process did not hold SQLite writer lock, got %v", err)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill lock-holding process: %v", err)
	}
	if err := <-childExited; err == nil {
		t.Fatal("expected helper process to terminate abnormally")
	}
	if err := second.Exclusive(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("SQLite did not release coordination lock after process termination: %v", err)
	}
}

func runCoordinatorProcessHelper(t *testing.T) {
	coordinator := openCoordinator(t, os.Getenv("GO_NANDO_COORDINATOR_DB"))
	err := coordinator.Exclusive(context.Background(), func() error {
		if err := os.WriteFile(os.Getenv("GO_NANDO_COORDINATOR_READY"), []byte("ready"), 0600); err != nil {
			return err
		}
		select {}
	})
	if err != nil {
		t.Fatalf("hold coordination lock: %v", err)
	}
}

func openCoordinator(t *testing.T, dbPath string) *sqlite.Coordinator {
	t.Helper()
	coordinator, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open coordinator: %v", err)
	}
	t.Cleanup(func() {
		if err := coordinator.Close(); err != nil {
			t.Errorf("close coordinator: %v", err)
		}
	})
	return coordinator
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for helper process to acquire coordinator lock")
}
