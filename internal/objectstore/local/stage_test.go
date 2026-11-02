package local

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/objectstore"
)

func TestOperationStageIsolationAndCleanupFencing(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "objects"), filepath.Join(root, "temp"), content.NewHasher())
	if err != nil {
		t.Fatal(err)
	}
	operationID := "c52be9aa-d581-42c3-93e3-d890c704f416"
	otherOperationID := "82bc61bf-8685-4f8d-895b-a40e4b894f9a"

	active, err := store.BeginStage(ctx, operationID)
	if err != nil {
		t.Fatal(err)
	}
	reader := &blockingStageReader{entered: make(chan struct{}), release: make(chan struct{})}
	type stageResult struct {
		staged objectstore.StagedObject
		err    error
	}
	stageDone := make(chan stageResult, 1)
	go func() {
		staged, err := active.Stage(ctx, reader)
		stageDone <- stageResult{staged: staged, err: err}
	}()
	<-reader.entered
	if err := store.CleanupStaging(ctx, operationID); err != nil {
		t.Fatalf("cleanup disposable active Stage: %v", err)
	}
	close(reader.release)
	stage := <-stageDone
	if stage.err != nil {
		t.Fatalf("finish already-running Stage after unlink: %v", stage.err)
	}
	if err := active.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Publish(ctx, stage.staged); !errors.Is(err, objectstore.ErrStagingNotFound) {
		t.Fatalf("removed Stage path was published or recreated: %v", err)
	}
	second, err := store.BeginStage(ctx, otherOperationID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := second.Stage(ctx, strings.NewReader("operation two"))
	if err != nil {
		t.Fatalf("stage operation two: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if stage.staged.ID() == other.ID() {
		t.Fatal("different staged content unexpectedly has the same ID")
	}
	if _, err := os.Stat(store.operationStagePath(otherOperationID)); err != nil {
		t.Fatalf("operation two staging file missing: %v", err)
	}

	if err := store.CleanupStaging(ctx, otherOperationID); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(store.tempDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging artifacts remain: entries=%v err=%v", entries, err)
	}
}

func TestStageFailureLeavesDisposableFileForCleanup(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "objects"), filepath.Join(root, "temp"), content.NewHasher())
	if err != nil {
		t.Fatal(err)
	}
	operationID := "fb681bd7-e72c-40aa-a94b-1b85092c24ba"
	session, err := store.BeginStage(ctx, operationID)
	if err != nil {
		t.Fatal(err)
	}
	_, stageErr := session.Stage(ctx, failingStageReader{err: errors.New("injected stage read failure")})
	closeErr := session.Close()
	if stageErr == nil || !strings.Contains(stageErr.Error(), "injected stage read failure") {
		t.Fatalf("expected injected Stage read error, got %v", stageErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if _, err := os.Stat(store.operationStagePath(operationID)); err != nil {
		t.Fatalf("failed Stage removed file before coordinated cleanup: %v", err)
	}
	if err := store.CleanupStaging(ctx, operationID); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(store.tempDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed Stage left artifacts: entries=%v err=%v", entries, err)
	}
}

func TestStageComputesSHA256AndMD5InSamePass(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(filepath.Join(t.TempDir(), "objects"), filepath.Join(t.TempDir(), "temp"), content.NewHasher())
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.BeginStage(ctx, "03d08be2-9c06-48a3-9f81-57b3b4ff8001")
	if err != nil {
		t.Fatal(err)
	}
	staged, err := session.Stage(ctx, strings.NewReader("stage digests"))
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	wantMD5 := md5.Sum([]byte("stage digests"))
	if !bytes.Equal(staged.MD5(), wantMD5[:]) {
		t.Fatalf("Stage MD5=%x want=%x", staged.MD5(), wantMD5)
	}
	wantID := content.NewHasher()
	id, err := wantID.Sum(ctx, strings.NewReader("stage digests"))
	if err != nil || staged.ID() != id {
		t.Fatalf("Stage ID=%s want=%s err=%v", staged.ID(), id, err)
	}
}

func TestManualStagingRemovalCannotBeRecreated(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "objects"), filepath.Join(root, "temp"), content.NewHasher())
	if err != nil {
		t.Fatal(err)
	}
	const operationID = "f320abf8-626d-4cec-943c-2abff6b29b5f"
	session, err := store.BeginStage(ctx, operationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.operationStagePath(operationID)); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Stage(ctx, strings.NewReader("must not be restaged")); !errors.Is(err, objectstore.ErrStagingNotFound) {
		t.Fatalf("Stage did not fail after manual removal: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.operationStagePath(operationID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("manual staging removal was undone: %v", err)
	}
}

func TestCleanupCanRemoveCrossProcessStageWithoutRecreation(t *testing.T) {
	if os.Getenv("GO_NANDO_STAGE_HELPER") == "1" {
		store, err := NewStore(os.Getenv("GO_NANDO_STAGE_OBJECTS"), os.Getenv("GO_NANDO_STAGE_TEMP"), content.NewHasher())
		if err != nil {
			t.Fatal(err)
		}
		session, err := store.BeginStage(context.Background(), os.Getenv("GO_NANDO_STAGE_OPERATION"))
		if err != nil {
			t.Fatal(err)
		}
		staged, stageErr := session.Stage(context.Background(), signalStageReader{
			readyPath:   os.Getenv("GO_NANDO_STAGE_READY"),
			releasePath: os.Getenv("GO_NANDO_STAGE_RELEASE"),
		})
		if stageErr != nil {
			t.Fatalf("active Stage failed after cleanup unlinked open file: %v", stageErr)
		}
		if _, err := os.Stat(store.operationStagePath(os.Getenv("GO_NANDO_STAGE_OPERATION"))); errors.Is(err, os.ErrNotExist) {
			if _, err := store.Publish(context.Background(), staged); !errors.Is(err, objectstore.ErrStagingNotFound) {
				t.Fatalf("cross-process cleanup allowed publish after unlink: %v", err)
			}
		} else if err != nil {
			t.Fatalf("stat staging path after Stage: %v", err)
		}
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}

	root := t.TempDir()
	objectsDir := filepath.Join(root, "objects")
	tempDir := filepath.Join(root, "temp")
	readyPath := filepath.Join(root, "ready")
	releasePath := filepath.Join(root, "release")
	const operationID = "77a29234-545c-4e36-ae47-8ce5ef4df052"
	command := exec.Command(os.Args[0], "-test.run=^TestCleanupCanRemoveCrossProcessStageWithoutRecreation$")
	command.Env = append(os.Environ(),
		"GO_NANDO_STAGE_HELPER=1",
		"GO_NANDO_STAGE_OBJECTS="+objectsDir,
		"GO_NANDO_STAGE_TEMP="+tempDir,
		"GO_NANDO_STAGE_READY="+readyPath,
		"GO_NANDO_STAGE_RELEASE="+releasePath,
		"GO_NANDO_STAGE_OPERATION="+operationID,
	)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			if err := command.Process.Kill(); err != nil {
				t.Errorf("kill stage helper after timeout: %v", err)
			}
			if err := command.Wait(); err != nil {
				t.Logf("stage helper exited after timeout cleanup: %v", err)
			}
			t.Fatal("timed out waiting for child Stage to begin reading")
		}
		time.Sleep(5 * time.Millisecond)
	}
	store, err := NewStore(objectsDir, tempDir, content.NewHasher())
	if err != nil {
		t.Fatal(err)
	}
	cleanupErr := store.CleanupStaging(context.Background(), operationID)
	if err := os.WriteFile(releasePath, []byte("continue"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("child Stage helper failed: %v\n%s", err, output.String())
	}
	if cleanupErr != nil {
		if err := store.CleanupStaging(context.Background(), operationID); err != nil {
			t.Fatalf("retry cleanup after open-file removal failed (%v): %v", cleanupErr, err)
		}
	}
}

type failingStageReader struct{ err error }

func (r failingStageReader) Read([]byte) (int, error) { return 0, r.err }

type blockingStageReader struct {
	entered chan struct{}
	release chan struct{}
}

type signalStageReader struct {
	readyPath   string
	releasePath string
}

func (r signalStageReader) Read(data []byte) (int, error) {
	if err := os.WriteFile(r.readyPath, []byte("writing"), 0o600); err != nil {
		return 0, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(r.releasePath); err == nil {
			copy(data, "cross process")
			return len("cross process"), io.EOF
		}
		if time.Now().After(deadline) {
			return 0, errors.New("timed out waiting for cleanup")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *blockingStageReader) Read(data []byte) (int, error) {
	select {
	case <-r.entered:
	default:
		close(r.entered)
	}
	<-r.release
	copy(data, "in flight")
	return len("in flight"), io.EOF
}
