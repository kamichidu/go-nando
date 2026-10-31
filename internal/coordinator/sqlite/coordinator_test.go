package sqlite_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
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

func TestResourceRLockSharingAndWLockExclusionAcrossInstances(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "coordination.db")
	first, second := openCoordinator(t, dbPath), openCoordinator(t, dbPath)
	ctx := context.Background()
	if err := first.AcquireResourceLock(ctx, "content:abc", "reader-1", sqlite.RLock, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := second.AcquireResourceLock(ctx, "content:abc", "reader-2", sqlite.RLock, time.Second); err != nil {
		t.Fatalf("shared RLock should coexist: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Millisecond)
	defer cancel()
	if err := second.AcquireResourceLock(waitCtx, "content:abc", "gc", sqlite.WLock, time.Second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WLock should wait for active readers, got %v", err)
	}
	if err := first.ReleaseResourceLocks(ctx, "reader-1"); err != nil {
		t.Fatal(err)
	}
	if err := second.ReleaseResourceLocks(ctx, "reader-2"); err != nil {
		t.Fatal(err)
	}
	if err := second.AcquireResourceLock(ctx, "content:abc", "gc", sqlite.WLock, time.Second); err != nil {
		t.Fatalf("WLock after readers release: %v", err)
	}
	waitCtx2, cancel2 := context.WithTimeout(ctx, 60*time.Millisecond)
	defer cancel2()
	if err := first.AcquireResourceLock(waitCtx2, "content:abc", "late-reader", sqlite.RLock, time.Second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RLock should wait for WLock, got %v", err)
	}
	if err := second.ReleaseResourceLocks(ctx, "gc"); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredResourceLockCannotBeRenewed(t *testing.T) {
	coordinator := openCoordinator(t, filepath.Join(t.TempDir(), "coordination.db"))
	ctx := context.Background()
	if err := coordinator.AcquireResourceLock(ctx, "temp:one", "put-one", sqlite.RLock, 40*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if active, err := coordinator.RenewResourceLocks(ctx, "put-one", time.Second); err != nil || active {
		t.Fatalf("expired lock renewal = %v, %v", active, err)
	}
	if valid, err := coordinator.ValidateResourceLocks(ctx, "put-one"); err != nil || valid {
		t.Fatalf("expired lock validation = %v, %v", valid, err)
	}
	if err := coordinator.AcquireResourceLock(ctx, "temp:one", "put-one", sqlite.RLock, time.Second); !errors.Is(err, sqlite.ErrResourceLockExpired) {
		t.Fatalf("expired operation must not reacquire and resurrect its lock: %v", err)
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

func TestPutLeaseRenewalInvalidationAndCrashRetry(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "coordination.db")
	first := openCoordinator(t, dbPath)
	second := openCoordinator(t, dbPath)
	const operationID = "9f3eaa4e-2fcf-43f5-a09e-a5f41003f44c"

	if err := first.AcquireLease(context.Background(), operationID, 40*time.Millisecond); err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	if renewed, err := second.RenewLease(context.Background(), operationID, 80*time.Millisecond); err != nil || !renewed {
		t.Fatalf("renew active lease across instances: renewed=%v err=%v", renewed, err)
	}
	if err := first.ExclusiveWithLease(context.Background(), operationID, func() error { return nil }); err != nil {
		t.Fatalf("active lease failed fenced Commit: %v", err)
	}

	time.Sleep(120 * time.Millisecond)
	ids, err := first.InvalidateExpiredLeases(context.Background())
	if err != nil || !slices.Contains(ids, operationID) {
		t.Fatalf("invalidate expired lease: ids=%v err=%v", ids, err)
	}
	if renewed, err := second.RenewLease(context.Background(), operationID, time.Second); err != nil || renewed {
		t.Fatalf("invalidated lease was resurrected: renewed=%v err=%v", renewed, err)
	}
	callbackRan := false
	err = second.ExclusiveWithLease(context.Background(), operationID, func() error {
		callbackRan = true
		return nil
	})
	if !errors.Is(err, sqlite.ErrLeaseExpired) || callbackRan {
		t.Fatalf("expired lease was allowed to Commit: callback=%v err=%v", callbackRan, err)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("close coordinator before cleanup retry: %v", err)
	}
	first = openCoordinator(t, dbPath)
	defer func() {
		if err := first.Close(); err != nil {
			t.Errorf("close coordinator: %v", err)
		}
	}()
	ids, err = first.InvalidateExpiredLeases(context.Background())
	if err != nil || !slices.Contains(ids, operationID) {
		t.Fatalf("invalidated lease was not retained for crash retry: ids=%v err=%v", ids, err)
	}
	if err := first.ForgetExpiredLease(context.Background(), operationID); err != nil {
		t.Fatalf("forget cleaned lease: %v", err)
	}
}

func TestPutLeaseRenewalRacingExpirationCannotResurrectLease(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "coordination.db")
	first := openCoordinator(t, dbPath)
	second := openCoordinator(t, dbPath)
	defer func() {
		if err := first.Close(); err != nil {
			t.Errorf("close first coordinator: %v", err)
		}
	}()
	defer func() {
		if err := second.Close(); err != nil {
			t.Errorf("close second coordinator: %v", err)
		}
	}()
	const operationID = "63f5ef21-e2dc-49c1-9c8d-4d6b71b4118f"
	if err := first.AcquireLease(context.Background(), operationID, 25*time.Millisecond); err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	time.Sleep(40 * time.Millisecond)

	start := make(chan struct{})
	var wait sync.WaitGroup
	var renewed bool
	var renewalErr error
	var invalidated []string
	var invalidationErr error
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		renewed, renewalErr = second.RenewLease(context.Background(), operationID, time.Second)
	}()
	go func() {
		defer wait.Done()
		<-start
		invalidated, invalidationErr = first.InvalidateExpiredLeases(context.Background())
	}()
	close(start)
	wait.Wait()
	if renewalErr != nil || invalidationErr != nil {
		t.Fatalf("race operations failed: renew=%v invalidate=%v", renewalErr, invalidationErr)
	}
	if renewed == slices.Contains(invalidated, operationID) {
		t.Fatalf("lease has inconsistent race outcome: renewed=%v invalidated=%v", renewed, invalidated)
	}
	if renewed {
		if err := first.ExclusiveWithLease(context.Background(), operationID, func() error { return nil }); err != nil {
			t.Fatalf("renewed lease should remain valid: %v", err)
		}
		return
	}
	if active, err := second.RenewLease(context.Background(), operationID, time.Second); err != nil || active {
		t.Fatalf("lease renewed after expiration invalidation: active=%v err=%v", active, err)
	}
	if err := first.ExclusiveWithLease(context.Background(), operationID, func() error { return nil }); !errors.Is(err, sqlite.ErrLeaseExpired) {
		t.Fatalf("invalidated lease unexpectedly allowed Commit: %v", err)
	}
}

func TestResourceLeaseRenewalDoesNotHoldRepositoryWideLock(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "coordination.db")
	reader := openCoordinator(t, dbPath)
	renewer := openCoordinator(t, dbPath)
	t.Cleanup(func() {
		if err := reader.Close(); err != nil {
			t.Errorf("close reader Coordinator: %v", err)
		}
	})
	t.Cleanup(func() {
		if err := renewer.Close(); err != nil {
			t.Errorf("close renewer Coordinator: %v", err)
		}
	})
	const operationID = "2697db17-50fb-4499-a01a-25f6b47ba7cd"
	if err := reader.AcquireResourceLock(context.Background(), "content:read", "reader-op", sqlite.RLock, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := reader.AcquireResourceLock(context.Background(), "temp:put", operationID, sqlite.RLock, time.Second); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		active, err := renewer.RenewResourceLocks(ctx, operationID, time.Second)
		if err == nil && !active {
			err = errors.New("Lease was not active during renewal")
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Lease renewal alongside unrelated read: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("resource Lease renewal was blocked by unrelated read activity")
	}
}

func TestPutLeaseAcrossProcesses(t *testing.T) {
	if os.Getenv("GO_NANDO_LEASE_HELPER") == "1" {
		coordinator, err := sqlite.Open(os.Getenv("GO_NANDO_LEASE_DB"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := coordinator.Close(); err != nil {
				t.Errorf("close helper coordinator: %v", err)
			}
		}()
		active, err := coordinator.RenewLease(context.Background(), os.Getenv("GO_NANDO_LEASE_OPERATION"), time.Second)
		if err != nil || !active {
			t.Fatalf("child process could not renew shared Put Lease: active=%v err=%v", active, err)
		}
		return
	}

	dbPath := filepath.Join(t.TempDir(), "coordination.db")
	coordinator := openCoordinator(t, dbPath)
	const operationID = "32635c02-5633-4611-a0b4-26b118df5ac4"
	if err := coordinator.AcquireLease(context.Background(), operationID, time.Second); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestPutLeaseAcrossProcesses$")
	command.Env = append(os.Environ(),
		"GO_NANDO_LEASE_HELPER=1",
		"GO_NANDO_LEASE_DB="+dbPath,
		"GO_NANDO_LEASE_OPERATION="+operationID,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("child process Lease renewal failed: %v\n%s", err, output)
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
