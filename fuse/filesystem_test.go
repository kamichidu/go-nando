package fusefs

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/kamichidu/go-nando/controlrpc"
)

type stubSession struct {
	path      string
	file      string
	read      bool
	followID  bool
	unlinked  bool
	committed bool
}

type stubDaemon struct {
	mu         sync.Mutex
	root       string
	entry      controlrpc.Entry
	path       string
	content    []byte
	exists     bool
	sessions   map[string]*stubSession
	calls      []string
	commits    int
	begins     int
	commitErr  error
	releaseErr error
}

func newStub(t *testing.T, content []byte) *stubDaemon {
	t.Helper()
	return &stubDaemon{
		root: t.TempDir(), entry: controlrpc.Entry{ID: "entry-1", Name: "file", Kind: "file", Size: int64(len(content)), Mode: 0644, Revision: 1, Mtime: time.Now().UnixNano(), ObjectID: "hash"},
		path: "/file", content: append([]byte(nil), content...), exists: true, sessions: make(map[string]*stubSession),
	}
}

func (d *stubDaemon) Call(_ context.Context, method string, params, result any) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, method)
	var p map[string]any
	encoded, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(encoded, &p); err != nil {
		return err
	}
	id, ok := p["session_id"].(string)
	if !ok {
		id = ""
	}
	switch method {
	case controlrpc.MethodResolveEntry:
		if !d.exists || p["entry_id"] != d.entry.ID {
			return &controlrpc.RemoteError{Code: controlrpc.CodeEntryNotFound, Message: "missing"}
		}
		return assign(result, controlrpc.ResolveEntryResult{Entry: d.entry, Path: d.path})
	case controlrpc.MethodStat:
		if !d.exists || p["path"] != d.path {
			return &controlrpc.RemoteError{Code: controlrpc.CodeEntryNotFound, Message: "missing"}
		}
		return assign(result, d.entry)
	case controlrpc.MethodBeginRead:
		if !d.exists || p["path"] != d.path {
			return &controlrpc.RemoteError{Code: controlrpc.CodeEntryNotFound, Message: "missing"}
		}
		file := filepath.Join(d.root, "read-resource")
		if err := os.WriteFile(file, d.content, 0600); err != nil {
			return err
		}
		sid := "read-session"
		d.sessions[sid] = &stubSession{path: d.path, file: file, read: true}
		return assign(result, controlrpc.BeginReadResult{SessionID: sid, ResourceURI: (&url.URL{Scheme: "file", Path: file}).String(), Entry: d.entry})
	case controlrpc.MethodBeginWrite:
		d.begins++
		file := filepath.Join(d.root, "write-"+time.Now().Format("150405.000000000"))
		if err := os.WriteFile(file, nil, 0600); err != nil {
			return err
		}
		sid := "write-session"
		d.sessions[sid] = &stubSession{path: d.path, file: file, followID: p["follow_entry_id"] == true}
		return assign(result, controlrpc.BeginWriteResult{SessionID: sid, ResourceURI: (&url.URL{Scheme: "file", Path: file}).String()})
	case controlrpc.MethodRenew:
		return assign(result, controlrpc.RenewResult{ExpiresAt: time.Now().Add(time.Minute)})
	case controlrpc.MethodCommit:
		s := d.sessions[id]
		if s == nil || s.read || s.unlinked {
			return &controlrpc.RemoteError{Code: controlrpc.CodeEntryConflict, Message: "closed"}
		}
		if d.commitErr != nil {
			return d.commitErr
		}
		if s.followID {
			if !d.exists || d.entry.ID != "entry-1" {
				return &controlrpc.RemoteError{Code: controlrpc.CodeEntryConflict, Message: "identity changed"}
			}
			s.path = d.path
		} else if s.path != d.path {
			return &controlrpc.RemoteError{Code: controlrpc.CodeEntryConflict, Message: "path changed"}
		}
		bytes, err := os.ReadFile(s.file)
		if err != nil {
			return err
		}
		d.content = bytes
		d.entry.Size = int64(len(bytes))
		d.entry.Revision++
		d.commits++
		s.committed = true
		return nil
	case controlrpc.MethodMarkUnlinked:
		s := d.sessions[id]
		if s == nil {
			return errors.New("missing session")
		}
		s.unlinked = true
		return nil
	case controlrpc.MethodRelease:
		if d.releaseErr != nil {
			return d.releaseErr
		}
		if s := d.sessions[id]; s != nil {
			if s.unlinked || s.committed || s.read {
				_ = os.Remove(s.file)
				delete(d.sessions, id)
			}
		}
		return nil
	case controlrpc.MethodAbort:
		if s := d.sessions[id]; s != nil {
			_ = os.Remove(s.file)
			delete(d.sessions, id)
		}
		return nil
	case controlrpc.MethodRemove:
		d.exists = false
		return nil
	case controlrpc.MethodRemoveEntry:
		if p["entry_id"] != d.entry.ID {
			return &controlrpc.RemoteError{Code: controlrpc.CodeEntryNotFound, Message: "wrong Entry"}
		}
		d.exists = false
		return nil
	case controlrpc.MethodMove, controlrpc.MethodMoveEntry:
		destination, ok := p["destination"].(string)
		if !ok {
			return errors.New("missing destination")
		}
		d.path = destination
		d.entry.Name = filepath.Base(d.path)
		d.entry.Revision++
		return nil
	default:
		return errors.New("unsupported test RPC: " + method)
	}
}

func assign(target, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func TestSharedWriterHandlesFsyncAndContinueAfterCommit(t *testing.T) {
	ctx := context.Background()
	daemon := newStub(t, []byte("old"))
	adapter := New(daemon)
	node := adapter.nodeFor(ctx, daemon.entry, daemon.path)
	state, errno := adapter.startWrite(ctx, node, false)
	if errno != 0 {
		t.Fatal(errno)
	}
	state.handles = 2
	adapter.writes[node.entryID()] = state
	first := &fileHandle{node: node, write: state, writable: true}
	second := &fileHandle{node: node, write: state, writable: true}
	if _, errno := first.Write(ctx, []byte("new"), 0); errno != 0 {
		t.Fatal(errno)
	}
	if errno := first.Flush(ctx); errno != 0 {
		t.Fatal(errno)
	}
	if daemon.commits != 0 {
		t.Fatal("Flush committed shared dirty staging")
	}
	if errno := second.Fsync(ctx, 0); errno != 0 {
		t.Fatalf("first Fsync: %v", errno)
	}
	if string(daemon.content) != "new" || daemon.commits != 1 {
		t.Fatalf("first commit content=%q commits=%d", daemon.content, daemon.commits)
	}
	if _, errno := first.Write(ctx, []byte("!"), 3); errno != 0 {
		t.Fatalf("write after fsync: %v", errno)
	}
	if errno := first.Fsync(ctx, 0); errno != 0 {
		t.Fatalf("second Fsync: %v", errno)
	}
	if string(daemon.content) != "new!" || daemon.commits != 2 {
		t.Fatalf("second commit content=%q commits=%d", daemon.content, daemon.commits)
	}
	if errno := first.Release(ctx); errno != 0 {
		t.Fatal(errno)
	}
	if errno := second.Release(ctx); errno != 0 {
		t.Fatal(errno)
	}
}

func TestUnlinkMarksIntentAndKeepsOpenHandlePrivate(t *testing.T) {
	ctx := context.Background()
	daemon := newStub(t, []byte("old"))
	adapter := New(daemon)
	root := &entryNode{owner: adapter, entry: controlrpc.Entry{ID: "root", Kind: "directory"}, path: "/"}
	_ = fs.NewNodeFS(root, &fs.Options{})
	var out fuse.EntryOut
	_, errno := root.Lookup(ctx, "file", &out)
	if errno != 0 {
		t.Fatal(errno)
	}
	node := adapter.byID["entry-1"]
	state, errno := adapter.startWrite(ctx, node, false)
	if errno != 0 {
		t.Fatal(errno)
	}
	state.handles = 1
	adapter.writes[node.entryID()] = state
	handle := &fileHandle{node: node, write: state, writable: true}
	if errno := root.Unlink(ctx, "file"); errno != 0 {
		t.Fatalf("unlink: %v", errno)
	}
	if _, errno := handle.Write(ctx, []byte("private"), 0); errno != 0 {
		t.Fatalf("write through unlinked open handle: %v", errno)
	}
	if errno := handle.Fsync(ctx, 0); errno != 0 {
		t.Fatalf("fsync unlinked handle: %v", errno)
	}
	if daemon.commits != 0 || daemon.exists {
		t.Fatalf("unlink resurrected or committed file: commits=%d exists=%v", daemon.commits, daemon.exists)
	}
	if errno := handle.Release(ctx); errno != 0 {
		t.Fatalf("final release: %v", errno)
	}
	if len(daemon.sessions) != 0 {
		t.Fatalf("unlink left session state: %+v", daemon.sessions)
	}
}

func TestErrnoMapsRevisionConflictAndMissingEntry(t *testing.T) {
	if got := errnoFor(&controlrpc.RemoteError{Code: controlrpc.CodeEntryConflict}); got != syscall.EAGAIN {
		t.Fatalf("conflict errno=%v, want EAGAIN", got)
	}
	if got := errnoFor(&controlrpc.RemoteError{Code: controlrpc.CodeEntryNotFound}); got != syscall.ENOENT {
		t.Fatalf("not-found errno=%v, want ENOENT", got)
	}
	if got := errnoFor(&controlrpc.RemoteError{Code: controlrpc.CodeEntryExists}); got != syscall.EEXIST {
		t.Fatalf("entry exists errno=%v", got)
	}
	if got := errnoFor(&controlrpc.RemoteError{Code: controlrpc.CodeDirectoryNotEmpty}); got != syscall.ENOTEMPTY {
		t.Fatalf("directory-not-empty errno=%v", got)
	}
}

func TestCASFailureMakesSharedWriterTerminal(t *testing.T) {
	ctx := context.Background()
	daemon := newStub(t, []byte("old"))
	adapter := New(daemon)
	node := adapter.nodeFor(ctx, daemon.entry, daemon.path)
	state, errno := adapter.startWrite(ctx, node, false)
	if errno != 0 {
		t.Fatal(errno)
	}
	state.handles = 1
	adapter.writes[node.entryID()] = state
	handle := &fileHandle{node: node, write: state, writable: true}
	if _, errno := handle.Write(ctx, []byte("winner?"), 0); errno != 0 {
		t.Fatal(errno)
	}
	daemon.commitErr = &controlrpc.RemoteError{Code: controlrpc.CodeEntryConflict, Message: "revision changed"}
	if errno := handle.Fsync(ctx, 0); errno != syscall.EAGAIN {
		t.Fatalf("CAS failure errno=%v", errno)
	}
	if _, errno := handle.Write(ctx, []byte("overwrite"), 0); errno != syscall.EAGAIN {
		t.Fatalf("post-conflict write errno=%v, want terminal EAGAIN", errno)
	}
	if daemon.begins != 1 {
		t.Fatalf("CAS failure silently began a new session: begins=%d", daemon.begins)
	}
	stage := daemon.sessions[state.recoveryID].file
	if data, err := os.ReadFile(stage); err != nil || string(data) != "winner?" {
		t.Fatalf("failed CAS staging was not retained unchanged: %q err=%v", data, err)
	}
	if errno := handle.Release(ctx); errno != 0 {
		t.Fatalf("close terminal writer: %v", errno)
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("failed CAS staging was discarded: %v", err)
	}
}

func TestWritableOpenUsesDirectIOAndCleanCloseAborts(t *testing.T) {
	ctx := context.Background()
	daemon := newStub(t, []byte("unchanged"))
	adapter := New(daemon)
	node := adapter.nodeFor(ctx, daemon.entry, daemon.path)
	_ = fs.NewNodeFS(adapter.Root(), &fs.Options{})
	handle, flags, errno := node.Open(ctx, uint32(os.O_RDWR))
	if errno != 0 {
		t.Fatal(errno)
	}
	if flags&fuse.FOPEN_DIRECT_IO == 0 {
		t.Fatalf("open flags=%#x, want direct I/O", flags)
	}
	file, ok := handle.(*fileHandle)
	if !ok {
		t.Fatal("open returned an unexpected handle type")
	}
	releaseErrno := file.Release(ctx)
	if releaseErrno != 0 {
		t.Fatal(releaseErrno)
	}
	if daemon.commits != 0 || len(daemon.sessions) != 0 {
		t.Fatalf("clean close committed or leaked a write session: commits=%d sessions=%d", daemon.commits, len(daemon.sessions))
	}
}

func TestSharedAppendHandlesAppendAtomically(t *testing.T) {
	ctx := context.Background()
	daemon := newStub(t, nil)
	adapter := New(daemon)
	node := adapter.nodeFor(ctx, daemon.entry, daemon.path)
	state, errno := adapter.startWrite(ctx, node, true)
	if errno != 0 {
		t.Fatal(errno)
	}
	state.handles = 2
	first := &fileHandle{node: node, write: state, writable: true, append: true}
	second := &fileHandle{node: node, write: state, writable: true, append: true}
	if _, errno := first.Write(ctx, []byte("a"), 0); errno != 0 {
		t.Fatal(errno)
	}
	if _, errno := second.Write(ctx, []byte("b"), 0); errno != 0 {
		t.Fatal(errno)
	}
	if _, errno := first.Write(ctx, []byte("c"), 0); errno != 0 {
		t.Fatal(errno)
	}
	if errno := first.Fsync(ctx, 0); errno != 0 {
		t.Fatal(errno)
	}
	if string(daemon.content) != "abc" {
		t.Fatalf("shared append result=%q", daemon.content)
	}
	if errno := first.Release(ctx); errno != 0 {
		t.Fatal(errno)
	}
	if errno := second.Release(ctx); errno != 0 {
		t.Fatal(errno)
	}
}

func TestWriteAfterCommitReleaseFailureStartsFreshStaging(t *testing.T) {
	ctx := context.Background()
	daemon := newStub(t, []byte("old"))
	adapter := New(daemon)
	node := adapter.nodeFor(ctx, daemon.entry, daemon.path)
	state, errno := adapter.startWrite(ctx, node, false)
	if errno != 0 {
		t.Fatal(errno)
	}
	state.handles = 1
	adapter.writes[node.entryID()] = state
	handle := &fileHandle{node: node, write: state, writable: true}
	if _, errno := handle.Write(ctx, []byte("new"), 0); errno != 0 {
		t.Fatal(errno)
	}
	daemon.releaseErr = errors.New("temporary release transport failure")
	if errno := handle.Fsync(ctx, 0); errno != syscall.EIO {
		t.Fatalf("Fsync release failure errno=%v, want EIO", errno)
	}
	if string(daemon.content) != "new" || state.pendingReleaseID == "" {
		t.Fatalf("Commit success was not recorded before failed Release: content=%q pending=%q", daemon.content, state.pendingReleaseID)
	}
	daemon.releaseErr = nil
	if _, errno := handle.Write(ctx, []byte("!"), 3); errno != 0 {
		t.Fatalf("write after Release failure: %v", errno)
	}
	if errno := handle.Fsync(ctx, 0); errno != 0 {
		t.Fatalf("commit fresh staging after Release failure: %v", errno)
	}
	if string(daemon.content) != "new!" || daemon.commits != 2 {
		t.Fatalf("post-release-failure bytes were lost: content=%q commits=%d", daemon.content, daemon.commits)
	}
	if errno := handle.Release(ctx); errno != 0 {
		t.Fatal(errno)
	}
}
