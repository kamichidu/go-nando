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
	path        string
	file        string
	read        bool
	followID    bool
	unlinked    bool
	committed   bool
	logicalMode uint32
	modeSet     bool
}

func mustFileHandle(t testing.TB, handle fs.FileHandle) *fileHandle {
	t.Helper()
	file, ok := handle.(*fileHandle)
	if !ok {
		t.Fatalf("handle has unexpected type %T", handle)
	}
	return file
}

func mustEntryNode(t testing.TB, node fs.InodeEmbedder) *entryNode {
	t.Helper()
	entry, ok := node.(*entryNode)
	if !ok {
		t.Fatalf("node has unexpected type %T", node)
	}
	return entry
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
	resolveErr error
	chmodErr   error
	removeErr  error
}

func newStub(t *testing.T, content []byte) *stubDaemon {
	t.Helper()
	return &stubDaemon{
		root: t.TempDir(), entry: controlrpc.Entry{ID: "entry-1", Name: "file", Kind: "file", Size: int64(len(content)), Mode: 0o644, Revision: 1, Mtime: time.Now().UnixNano(), ObjectID: "hash"},
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
		if d.resolveErr != nil {
			return d.resolveErr
		}
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
		if err := os.WriteFile(file, d.content, 0o600); err != nil {
			return err
		}
		sid := "read-session"
		d.sessions[sid] = &stubSession{path: d.path, file: file, read: true}
		return assign(result, controlrpc.BeginReadResult{SessionID: sid, ResourceURI: (&url.URL{Scheme: "file", Path: file}).String(), Entry: d.entry})
	case controlrpc.MethodBeginWrite:
		d.begins++
		file := filepath.Join(d.root, "write-"+time.Now().Format("150405.000000000"))
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			return err
		}
		sid := "write-session"
		path, ok := p["path"].(string)
		if !ok {
			return errors.New("begin write path is not a string")
		}
		session := &stubSession{path: path, file: file, followID: p["follow_entry_id"] == true}
		if mode, ok := p["logical_mode"].(float64); ok {
			session.logicalMode, session.modeSet = uint32(mode), true
		}
		d.sessions[sid] = session
		return assign(result, controlrpc.BeginWriteResult{SessionID: sid, ResourceURI: (&url.URL{Scheme: "file", Path: file}).String()})
	case controlrpc.MethodRenew:
		return assign(result, controlrpc.RenewResult{ExpiresAt: time.Now().Add(time.Minute)})
	case controlrpc.MethodSetMode:
		s := d.sessions[id]
		if s == nil {
			return errors.New("missing session")
		}
		mode, ok := p["mode"].(float64)
		if !ok {
			return errors.New("missing mode")
		}
		s.logicalMode, s.modeSet = uint32(mode), true
		return nil
	case controlrpc.MethodChmodEntry:
		if d.chmodErr != nil {
			return d.chmodErr
		}
		if !d.exists || p["entry_id"] != d.entry.ID {
			return &controlrpc.RemoteError{Code: controlrpc.CodeEntryNotFound, Message: "missing"}
		}
		mode, ok := p["mode"].(float64)
		if !ok {
			return errors.New("missing mode")
		}
		d.entry.Mode = uint32(mode) & 0o7777
		d.entry.Revision++
		return nil
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
		if s.modeSet && !d.exists {
			d.entry.Mode = s.logicalMode
		}
		d.exists = true
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
		if d.removeErr != nil {
			return d.removeErr
		}
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

func TestLinkedAndDetachedChmodSharedAcrossReadWriteHandles(t *testing.T) {
	ctx := context.Background()
	d := newStub(t, []byte("data"))
	f := New(d)
	root := &entryNode{owner: f, entry: controlrpc.Entry{ID: "root", Kind: "directory"}, path: "/"}
	_ = fs.NewNodeFS(root, &fs.Options{})
	var ent fuse.EntryOut
	if _, e := root.Lookup(ctx, "file", &ent); e != 0 {
		t.Fatal(e)
	}
	n := f.byID[d.entry.ID]
	rh, _, e := n.Open(ctx, uint32(os.O_RDONLY))
	if e != 0 {
		t.Fatal(e)
	}
	wh, _, e := n.Open(ctx, uint32(os.O_WRONLY))
	if e != 0 {
		t.Fatal(e)
	}
	chmod := func(mode uint32) *fuse.SetAttrIn {
		return &fuse.SetAttrIn{SetAttrInCommon: fuse.SetAttrInCommon{Valid: fuse.FATTR_MODE, Mode: mode}}
	}
	var attr fuse.AttrOut
	if e := n.Setattr(ctx, rh, chmod(0o640), &attr); e != 0 {
		t.Fatalf("linked read fchmod: %v", e)
	}
	if d.entry.Mode&0o7777 != 0o640 {
		t.Fatalf("metadata mode=%o", d.entry.Mode&0o7777)
	}
	writer := mustFileHandle(t, wh)
	if e := writer.Getattr(ctx, &attr); e != 0 {
		t.Fatalf("writer fstat: %v", e)
	}
	if attr.Mode&0o7777 != 0o640 {
		t.Fatalf("writer fstat mode=%o, want 0640", attr.Mode&0o7777)
	}
	if e := n.Setattr(ctx, wh, chmod(0o604), &attr); e != 0 {
		t.Fatalf("linked write fchmod: %v", e)
	}
	reader := mustFileHandle(t, rh)
	if e := reader.Getattr(ctx, &attr); e != 0 {
		t.Fatalf("reader fstat: %v", e)
	}
	if attr.Mode&0o7777 != 0o604 {
		t.Fatalf("reader did not observe write-handle chmod: mode=%o, want 0604", attr.Mode&0o7777)
	}
	if e := n.Setattr(ctx, nil, chmod(0o620), &attr); e != 0 {
		t.Fatalf("linked path chmod: %v", e)
	}
	if d.entry.Mode&0o7777 != 0o620 {
		t.Fatalf("path chmod metadata=%o", d.entry.Mode&0o7777)
	}
	if e := root.Unlink(ctx, "file"); e != 0 {
		t.Fatal(e)
	}
	d.mu.Lock()
	calls := len(d.calls)
	d.mu.Unlock()
	if e := n.Setattr(ctx, wh, chmod(0o600), &attr); e != 0 {
		t.Fatalf("detached write fchmod: %v", e)
	}
	if e := reader.Getattr(ctx, &attr); e != 0 {
		t.Fatalf("reader fstat: %v", e)
	}
	if attr.Mode&0o7777 != 0o600 {
		t.Fatalf("reader fstat mode=%o, want 0600", attr.Mode&0o7777)
	}
	d.mu.Lock()
	if len(d.calls) != calls {
		t.Fatalf("detached chmod made daemon calls: %v", d.calls[calls:])
	}
	if d.entry.Mode&0o7777 != 0o620 {
		t.Fatalf("detached chmod changed metadata: %o", d.entry.Mode&0o7777)
	}
	d.mu.Unlock()
	if e := mustFileHandle(t, rh).Release(ctx); e != 0 {
		t.Fatal(e)
	}
	if f.modes[d.entry.ID] == nil {
		t.Fatal("shared mode discarded before last close")
	}
	if e := mustFileHandle(t, wh).Release(ctx); e != 0 {
		t.Fatal(e)
	}
	if f.modes[d.entry.ID] != nil {
		t.Fatal("shared mode retained after last close")
	}
}

func TestLinkedChmodFailedRPCLeavesEntryAndHandleMode(t *testing.T) {
	ctx := context.Background()
	d := newStub(t, []byte("data"))
	f := New(d)
	n := f.nodeFor(ctx, d.entry, d.path)
	h, _, e := n.Open(ctx, uint32(os.O_RDONLY))
	if e != 0 {
		t.Fatal(e)
	}
	defer func() {
		if errno := mustFileHandle(t, h).Release(ctx); errno != 0 {
			t.Errorf("release file handle: %v", errno)
		}
	}()
	d.chmodErr = errors.New("rpc unavailable")
	in := &fuse.SetAttrIn{SetAttrInCommon: fuse.SetAttrInCommon{Valid: fuse.FATTR_MODE, Mode: 0o600}}
	if e := n.Setattr(ctx, h, in, &fuse.AttrOut{}); e != syscall.EIO {
		t.Fatalf("chmod errno=%v, want EIO", e)
	}
	if d.entry.Mode&0o7777 != 0o644 {
		t.Fatalf("failed RPC changed mode: %o", d.entry.Mode&0o7777)
	}
}

func TestFailedModeRPCUnlocksStateAndRetryWorks(t *testing.T) {
	ctx := context.Background()
	d := newStub(t, []byte("data"))
	f := New(d)
	n := f.nodeFor(ctx, d.entry, d.path)
	h, _, e := n.Open(ctx, uint32(os.O_RDONLY))
	if e != 0 {
		t.Fatal(e)
	}
	defer func() {
		if errno := mustFileHandle(t, h).Release(ctx); errno != 0 {
			t.Errorf("release file handle: %v", errno)
		}
	}()
	d.chmodErr = errors.New("rpc unavailable")
	in := &fuse.SetAttrIn{SetAttrInCommon: fuse.SetAttrInCommon{Valid: fuse.FATTR_MODE, Mode: 0o600}}
	if e := n.Setattr(ctx, h, in, &fuse.AttrOut{}); e != syscall.EIO {
		t.Fatalf("failed chmod=%v", e)
	}
	d.chmodErr = nil
	d.resolveErr = nil
	if e := n.Setattr(ctx, h, in, &fuse.AttrOut{}); e != 0 {
		t.Fatalf("retry chmod=%v", e)
	}
	if e := mustFileHandle(t, h).Getattr(ctx, &fuse.AttrOut{}); e != 0 {
		t.Fatalf("independent fstat after retry=%v", e)
	}
}

func TestFailedResolveUnlocksStateAndRetryWorks(t *testing.T) {
	ctx := context.Background()
	d := newStub(t, []byte("data"))
	f := New(d)
	n := f.nodeFor(ctx, d.entry, d.path)
	h, _, e := n.Open(ctx, uint32(os.O_RDONLY))
	if e != 0 {
		t.Fatal(e)
	}
	defer func() {
		if errno := mustFileHandle(t, h).Release(ctx); errno != 0 {
			t.Errorf("release file handle: %v", errno)
		}
	}()
	d.resolveErr = errors.New("resolve unavailable")
	in := &fuse.SetAttrIn{SetAttrInCommon: fuse.SetAttrInCommon{Valid: fuse.FATTR_MODE, Mode: 0o600}}
	if e := n.Setattr(ctx, h, in, &fuse.AttrOut{}); e != syscall.EIO {
		t.Fatalf("failed resolve=%v", e)
	}
	d.resolveErr = nil
	if e := n.Setattr(ctx, h, in, &fuse.AttrOut{}); e != 0 {
		t.Fatalf("retry after resolve failure=%v", e)
	}
}

func TestUnlinkRemovalFailureKeepsLinkedModeAndRetries(t *testing.T) {
	ctx := context.Background()
	d := newStub(t, []byte("old"))
	f := New(d)
	root := &entryNode{owner: f, entry: controlrpc.Entry{ID: "root", Kind: "directory"}, path: "/"}
	_ = fs.NewNodeFS(root, &fs.Options{})
	var out fuse.EntryOut
	if _, e := root.Lookup(ctx, "file", &out); e != 0 {
		t.Fatal(e)
	}
	n := f.byID[d.entry.ID]
	state, e := f.startWrite(ctx, n, false)
	if e != 0 {
		t.Fatal(e)
	}
	state.handles = 1
	f.writes[n.entryID()] = state
	h := &fileHandle{node: n, write: state, writable: true}
	rh, _, e := n.Open(ctx, uint32(os.O_RDONLY))
	if e != 0 {
		t.Fatal(e)
	}
	d.removeErr = errors.New("metadata delete unavailable")
	if e := root.Unlink(ctx, "file"); e != syscall.EIO {
		t.Fatalf("failed unlink=%v", e)
	}
	if !d.exists || state.unlinked || !state.unlinkIntent || (f.modes[n.entryID()] != nil && f.modes[n.entryID()].unlinked) {
		t.Fatalf("failure state namespace=%v unlinked=%v intent=%v", d.exists, state.unlinked, state.unlinkIntent)
	}
	d.mu.Lock()
	d.entry.Mode = 0o601
	d.entry.Revision++
	d.mu.Unlock()
	var linkedAttr fuse.AttrOut
	if e := mustFileHandle(t, rh).Getattr(ctx, &linkedAttr); e != 0 || linkedAttr.Mode&0o7777 != 0o601 {
		t.Fatalf("pending-intent handle mode=%o errno=%v", linkedAttr.Mode&0o7777, e)
	}
	if e := h.Fsync(ctx, 0); e != 0 || d.commits != 0 {
		t.Fatalf("tombstoned fsync errno=%v commits=%d", e, d.commits)
	}
	d.removeErr = nil
	if e := root.Unlink(ctx, "file"); e != 0 {
		t.Fatalf("unlink retry=%v", e)
	}
	if d.exists || !state.unlinked || !f.modes[n.entryID()].unlinked {
		t.Fatalf("retry did not detach namespace/state: exists=%v", d.exists)
	}
	if e := h.Release(ctx); e != 0 {
		t.Fatalf("release after retry=%v", e)
	}
	if e := mustFileHandle(t, rh).Getattr(ctx, &linkedAttr); e != 0 {
		t.Fatalf("detached read getattr=%v", e)
	}
	if e := mustFileHandle(t, rh).Release(ctx); e != 0 {
		t.Fatalf("read release=%v", e)
	}
}

func TestConcurrentModeSetattrAndUnlinkCompletes(t *testing.T) {
	ctx := context.Background()
	d := newStub(t, []byte("data"))
	f := New(d)
	root := &entryNode{owner: f, entry: controlrpc.Entry{ID: "root", Kind: "directory"}, path: "/"}
	_ = fs.NewNodeFS(root, &fs.Options{})
	var out fuse.EntryOut
	if _, e := root.Lookup(ctx, "file", &out); e != 0 {
		t.Fatal(e)
	}
	n := f.byID[d.entry.ID]
	h, _, e := n.Open(ctx, uint32(os.O_RDONLY))
	if e != 0 {
		t.Fatal(e)
	}
	wh, _, e := n.Open(ctx, uint32(os.O_WRONLY))
	if e != 0 {
		t.Fatal(e)
	}
	start := make(chan struct{})
	done := make(chan syscall.Errno, 1)
	go func() {
		<-start
		for i := 0; i < 50; i++ {
			in := &fuse.SetAttrIn{SetAttrInCommon: fuse.SetAttrInCommon{Valid: fuse.FATTR_MODE, Mode: uint32(0o600 + i%8)}}
			if err := n.Setattr(ctx, wh, in, &fuse.AttrOut{}); err != 0 && err != syscall.ENOENT {
				done <- err
				return
			}
		}
		done <- 0
	}()
	close(start)
	unlink := root.Unlink(ctx, "file")
	var setattr syscall.Errno
	select {
	case setattr = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent Setattr/Unlink deadlocked")
	}
	if unlink != 0 {
		t.Fatalf("unlink=%v", unlink)
	}
	if setattr != 0 {
		t.Fatalf("concurrent setattr=%v", setattr)
	}
	if e := mustFileHandle(t, h).Release(ctx); e != 0 {
		t.Fatal(e)
	}
	if e := mustFileHandle(t, wh).Release(ctx); e != 0 {
		t.Fatal(e)
	}
}

func TestLinkedHandleGetattrSeesExternalModeMutation(t *testing.T) {
	ctx := context.Background()
	d := newStub(t, []byte("data"))
	f := New(d)
	n := f.nodeFor(ctx, d.entry, d.path)
	h, _, e := n.Open(ctx, uint32(os.O_RDONLY))
	if e != 0 {
		t.Fatal(e)
	}
	defer func() {
		if errno := mustFileHandle(t, h).Release(ctx); errno != 0 {
			t.Errorf("release file handle: %v", errno)
		}
	}()
	d.mu.Lock()
	d.entry.Mode = 0o601
	d.entry.Revision++
	d.mu.Unlock()
	var out fuse.AttrOut
	if e := mustFileHandle(t, h).Getattr(ctx, &out); e != 0 || out.Mode&0o7777 != 0o601 {
		t.Fatalf("external chmod fstat mode=%o errno=%v", out.Mode&0o7777, e)
	}
}

func assign(target, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func TestFuseCreateModeZeroKeepsStagePrivateAndCommitsLogicalMode(t *testing.T) {
	ctx := context.Background()
	daemon := newStub(t, nil)
	daemon.exists = false
	adapter := New(daemon)
	root := mustEntryNode(t, adapter.Root())
	// Initialize the inode bridge so Create can publish its child inode.
	fs.NewNodeFS(root, &fs.Options{})
	var out fuse.EntryOut
	_, handle, _, errno := root.Create(ctx, "file", uint32(os.O_RDWR), 0, &out)
	if errno != 0 {
		t.Fatalf("Create: %v", errno)
	}
	fh := mustFileHandle(t, handle)
	info, err := fh.write.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("staging permissions = %o, want 600", got)
	}
	if n, errno := fh.Write(ctx, []byte("payload"), 0); errno != 0 || n != 7 {
		t.Fatalf("Write = %d, %v", n, errno)
	}
	if errno := fh.Fsync(ctx, 0); errno != 0 {
		t.Fatalf("Fsync: %v", errno)
	}
	if got := daemon.entry.Mode & 0o7777; got != 0 {
		t.Fatalf("committed mode = %o, want 0", got)
	}
	if got := string(daemon.content); got != "payload" {
		t.Fatalf("committed bytes = %q", got)
	}
	if errno := fh.Release(ctx); errno != 0 {
		t.Fatalf("Release: %v", errno)
	}
}

func TestSetattrChangesLogicalModeWithoutChangingStageMode(t *testing.T) {
	ctx := context.Background()
	daemon := newStub(t, []byte("content"))
	adapter := New(daemon)
	node := adapter.nodeFor(ctx, daemon.entry, daemon.path)
	state, errno := adapter.startWrite(ctx, node, false)
	if errno != 0 {
		t.Fatalf("start write: %v", errno)
	}
	fh := &fileHandle{node: node, write: state, writable: true}
	in := &fuse.SetAttrIn{SetAttrInCommon: fuse.SetAttrInCommon{Valid: fuse.FATTR_MODE, Mode: 0o640}}
	if errno := node.Setattr(ctx, fh, in, &fuse.AttrOut{}); errno != 0 {
		t.Fatalf("Setattr chmod: %v", errno)
	}
	info, err := state.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("staging permissions = %o, want 600", got)
	}
	if errno := fh.Fsync(ctx, 0); errno != 0 {
		t.Fatalf("Fsync: %v", errno)
	}
	if got := daemon.entry.Mode & 0o7777; got != 0o640 {
		t.Fatalf("committed mode = %o, want 640", got)
	}
	if errno := fh.Release(ctx); errno != 0 {
		t.Fatalf("Release: %v", errno)
	}
}

func TestReadHandleGetattrRefreshesLinkedMetadata(t *testing.T) {
	ctx := context.Background()
	daemon := newStub(t, []byte("content"))
	adapter := New(daemon)
	node := adapter.nodeFor(ctx, daemon.entry, daemon.path)
	h, _, errno := node.Open(ctx, uint32(os.O_RDONLY))
	if errno != 0 {
		t.Fatal(errno)
	}
	defer func() {
		if errno := mustFileHandle(t, h).Release(ctx); errno != 0 {
			t.Errorf("release file handle: %v", errno)
		}
	}()
	daemon.mu.Lock()
	daemon.entry.Mode = 0o604
	daemon.entry.Revision++
	daemon.mu.Unlock()
	var out fuse.AttrOut
	if errno := mustFileHandle(t, h).Getattr(ctx, &out); errno != 0 {
		t.Fatalf("Getattr: %v", errno)
	}
	if got := out.Mode & 0o7777; got != 0o604 {
		t.Fatalf("linked read-handle mode=%o, want external mode 604", got)
	}
}

func TestSetattrResponseReflectsUncommittedSession(t *testing.T) {
	ctx := context.Background()
	daemon := newStub(t, []byte("content"))
	adapter := New(daemon)
	node := adapter.nodeFor(ctx, daemon.entry, daemon.path)
	state, errno := adapter.startWrite(ctx, node, false)
	if errno != 0 {
		t.Fatalf("start write: %v", errno)
	}
	h := &fileHandle{node: node, write: state, writable: true}
	in := &fuse.SetAttrIn{SetAttrInCommon: fuse.SetAttrInCommon{Valid: fuse.FATTR_SIZE | fuse.FATTR_MODE, Size: 3, Mode: 0o640}}
	var out fuse.AttrOut
	if errno := node.Setattr(ctx, h, in, &out); errno != 0 {
		t.Fatalf("Setattr: %v", errno)
	}
	if got := out.Mode & 0o7777; got != 0o640 {
		t.Fatalf("Setattr response mode=%o, want 640", got)
	}
	if out.Size != 3 {
		t.Fatalf("Setattr response size=%d, want 3", out.Size)
	}
	if daemon.commits != 0 || daemon.entry.Mode != 0o640 || daemon.entry.Size != int64(len("content")) {
		t.Fatalf("Setattr metadata/content result: commits=%d Entry=%+v", daemon.commits, daemon.entry)
	}
	if errno := h.Fsync(ctx, 0); errno != 0 {
		t.Fatalf("Fsync: %v", errno)
	}
	if daemon.entry.Mode != 0o640 || daemon.entry.Size != 3 {
		t.Fatalf("committed Entry=%+v, want mode 640 size 3", daemon.entry)
	}
	if errno := h.Release(ctx); errno != 0 {
		t.Fatalf("Release: %v", errno)
	}
}

func TestEphemeralSetattrResponseAndCommitUseLogicalMode(t *testing.T) {
	ctx := context.Background()
	daemon := newStub(t, []byte("content"))
	adapter := New(daemon)
	node := adapter.nodeFor(ctx, daemon.entry, daemon.path)
	in := &fuse.SetAttrIn{SetAttrInCommon: fuse.SetAttrInCommon{Valid: fuse.FATTR_MODE, Mode: 0o604}}
	var out fuse.AttrOut
	if errno := node.Setattr(ctx, nil, in, &out); errno != 0 {
		t.Fatalf("Setattr: %v", errno)
	}
	if got := out.Mode & 0o7777; got != 0o604 || daemon.entry.Mode != 0o604 {
		t.Fatalf("response mode=%o committed mode=%o, want both 604", got, daemon.entry.Mode)
	}
	if out.Size != uint64(len("content")) {
		t.Fatalf("mode-only Setattr changed size: %d", out.Size)
	}
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
	if root.GetChild("file") == nil {
		t.Fatal("Unlink callback changed inode tree before go-fuse rawBridge")
	}
	var attr fuse.AttrOut
	stageInfo, err := state.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if errno := handle.Getattr(ctx, &attr); errno != 0 {
		t.Fatalf("Getattr through unlinked write handle: %v", errno)
	}
	if got := attr.Mode & 0o7777; got != state.logicalMode {
		t.Fatalf("unlinked handle mode=%o, want logical mode %o", got, state.logicalMode)
	}
	if attr.Ino != inodeNumber(node.entryID()) || attr.Mode&syscall.S_IFMT != syscall.S_IFREG {
		t.Fatalf("unlinked handle identity/type: ino=%d mode=%o", attr.Ino, attr.Mode)
	}
	if attr.Size != uint64(stageInfo.Size()) || attr.Mtime != uint64(stageInfo.ModTime().Unix()) || attr.Mtimensec != uint32(stageInfo.ModTime().Nanosecond()) {
		t.Fatalf("unlinked handle staged attributes: size=%d mtime=%d.%09d, want size=%d mtime=%s", attr.Size, attr.Mtime, attr.Mtimensec, stageInfo.Size(), stageInfo.ModTime().Format(time.RFC3339Nano))
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

func TestUnlinkedOpenWriterOwnsAttributesAndResources(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		name := "active session"
		if recovery {
			name = "recovery with cleared session ID"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			daemon := newStub(t, []byte("original"))
			adapter := New(daemon)
			node := adapter.nodeFor(ctx, daemon.entry, daemon.path)
			state, errno := adapter.startWrite(ctx, node, false)
			if errno != 0 {
				t.Fatal(errno)
			}
			state.handles = 1
			adapter.writes[node.entryID()] = state
			handle := &fileHandle{node: node, write: state, writable: true}
			if recovery {
				if _, errno := handle.Write(ctx, []byte("old"), 0); errno != 0 {
					t.Fatal(errno)
				}
				daemon.commitErr = &controlrpc.RemoteError{Code: controlrpc.CodeEntryConflict, Message: "conflict"}
				if errno := handle.Fsync(ctx, 0); errno != syscall.EAGAIN {
					t.Fatalf("failed commit errno=%v, want EAGAIN", errno)
				}
				if state.sessionID != "" || state.recoveryID == "" {
					t.Fatalf("failed commit state session=%q recovery=%q", state.sessionID, state.recoveryID)
				}
				daemon.commitErr = nil
			}
			root := &entryNode{owner: adapter, entry: controlrpc.Entry{ID: "root", Kind: "directory"}, path: "/"}
			_ = fs.NewNodeFS(root, &fs.Options{})
			if errno := root.Unlink(ctx, "file"); errno != 0 {
				t.Fatalf("unlink: %v", errno)
			}
			callsAfterUnlink := len(daemon.calls)

			in := &fuse.SetAttrIn{SetAttrInCommon: fuse.SetAttrInCommon{Valid: fuse.FATTR_SIZE | fuse.FATTR_MODE, Size: 4, Mode: 0o640}}
			var attr fuse.AttrOut
			if errno := node.Setattr(ctx, handle, in, &attr); errno != 0 {
				t.Fatalf("handle Setattr after unlink: %v", errno)
			}
			if got := attr.Mode & 0o7777; got != 0o640 || attr.Size != 4 {
				t.Fatalf("Setattr snapshot mode=%o size=%d, want 640 and 4", got, attr.Size)
			}
			if _, errno := handle.Write(ctx, []byte("data"), 0); errno != 0 {
				t.Fatalf("Write after unlink: %v", errno)
			}
			result, errno := handle.Read(ctx, make([]byte, 4), 0)
			if errno != 0 {
				t.Fatalf("Read after unlink: %v", errno)
			}
			buf, status := result.Bytes(make([]byte, 4))
			if status != fuse.OK || string(buf) != "data" {
				t.Fatalf("Read result=%q status=%v", buf, status)
			}
			if errno := handle.Getattr(ctx, &attr); errno != 0 {
				t.Fatalf("Getattr after unlink: %v", errno)
			}
			if attr.Size != 4 || attr.Mode&0o7777 != 0o640 {
				t.Fatalf("handle Getattr mode=%o size=%d", attr.Mode&0o7777, attr.Size)
			}
			if errno := handle.Fsync(ctx, 0); errno != 0 {
				t.Fatalf("Fsync after unlink: %v", errno)
			}
			info, err := state.file.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o600 {
				t.Fatalf("staging permission mode=%o, want 600", got)
			}
			if len(daemon.calls) != callsAfterUnlink {
				t.Fatalf("handle operations made daemon calls after unlink: %v", daemon.calls[callsAfterUnlink:])
			}
			if daemon.exists || daemon.commits != 0 || daemon.begins != 1 {
				t.Fatalf("unlink handle operations changed repository/session state: exists=%v commits=%d begins=%d", daemon.exists, daemon.commits, daemon.begins)
			}
			if errno := handle.Release(ctx); errno != 0 {
				t.Fatalf("Release: %v", errno)
			}
			if len(daemon.sessions) != 0 {
				t.Fatalf("cleanup left session state: %+v", daemon.sessions)
			}
		})
	}
}

func TestReadHandleSetattrAfterUnlinkKeepsImmutableResource(t *testing.T) {
	ctx := context.Background()
	daemon := newStub(t, []byte("readable"))
	adapter := New(daemon)
	root := &entryNode{owner: adapter, entry: controlrpc.Entry{ID: "root", Kind: "directory"}, path: "/"}
	_ = fs.NewNodeFS(root, &fs.Options{})
	var out fuse.EntryOut
	if _, errno := root.Lookup(ctx, "file", &out); errno != 0 {
		t.Fatal(errno)
	}
	node := adapter.byID[daemon.entry.ID]
	read, file, lease, errno := adapter.beginRead(ctx, daemon.path, daemon.entry.ID)
	if errno != 0 {
		t.Fatal(errno)
	}
	handle := &fileHandle{node: node, readFile: file, readID: read.SessionID, lease: lease, logicalMode: daemon.entry.Mode & 0o7777, modeState: adapter.retainMode(node.entryID(), daemon.entry.Mode)}
	if errno := root.Unlink(ctx, "file"); errno != 0 {
		t.Fatalf("unlink: %v", errno)
	}
	callsAfterUnlink := len(daemon.calls)
	var attr fuse.AttrOut
	mode := &fuse.SetAttrIn{SetAttrInCommon: fuse.SetAttrInCommon{Valid: fuse.FATTR_MODE, Mode: 0o600}}
	if errno := node.Setattr(ctx, handle, mode, &attr); errno != 0 {
		t.Fatalf("read handle fchmod after unlink: %v", errno)
	}
	if attr.Mode&0o7777 != 0o600 {
		t.Fatalf("private read-handle mode=%o, want 600", attr.Mode&0o7777)
	}
	size := &fuse.SetAttrIn{SetAttrInCommon: fuse.SetAttrInCommon{Valid: fuse.FATTR_SIZE, Size: 1}}
	if errno := node.Setattr(ctx, handle, size, &attr); errno != syscall.EBADF {
		t.Fatalf("read handle ftruncate errno=%v, want EBADF", errno)
	}
	result, errno := handle.Read(ctx, make([]byte, 8), 0)
	if errno != 0 {
		t.Fatalf("Read after unlink: %v", errno)
	}
	data, status := result.Bytes(make([]byte, 8))
	if status != fuse.OK || string(data) != "readable" {
		t.Fatalf("read bytes=%q status=%v", data, status)
	}
	if errno := handle.Getattr(ctx, &attr); errno != 0 {
		t.Fatalf("Getattr after unlink: %v", errno)
	}
	if errno := handle.Fsync(ctx, 0); errno != 0 {
		t.Fatalf("Fsync after unlink: %v", errno)
	}
	if len(daemon.calls) != callsAfterUnlink {
		t.Fatalf("read handle operations made daemon calls after unlink: %v", daemon.calls[callsAfterUnlink:])
	}
	if daemon.exists || daemon.commits != 0 {
		t.Fatalf("read handle operations resurrected Entry: exists=%v commits=%d", daemon.exists, daemon.commits)
	}
	if errno := handle.Release(ctx); errno != 0 {
		t.Fatalf("Release: %v", errno)
	}
	if len(daemon.sessions) != 0 {
		t.Fatalf("read session cleanup left state: %+v", daemon.sessions)
	}
}

func TestRenameLeavesInodeTreeUpdateToGoFuseBridge(t *testing.T) {
	ctx := context.Background()
	daemon := newStub(t, []byte("content"))
	adapter := New(daemon)
	root, ok := adapter.Root().(*entryNode)
	if !ok {
		t.Fatal("Root returned an unexpected node type")
	}
	_ = fs.NewNodeFS(root, &fs.Options{})
	var out fuse.EntryOut
	childInode, errno := root.Lookup(ctx, "file", &out)
	if errno != 0 {
		t.Fatal(errno)
	}
	child, ok := childInode.Operations().(*entryNode)
	if !ok {
		t.Fatal("Lookup returned an unexpected node type")
	}
	if errno := root.Rename(ctx, "file", root, "renamed", 0); errno != 0 {
		t.Fatalf("rename callback: %v", errno)
	}
	if daemon.path != "/renamed" || child.currentPath() != "/renamed" {
		t.Fatalf("daemon path=%q cached Entry path=%q", daemon.path, child.currentPath())
	}
	if root.GetChild("file") != childInode || root.GetChild("renamed") != nil {
		t.Fatal("Rename callback changed inode tree before go-fuse rawBridge")
	}
}

func TestRmdirLeavesInodeTreeRemovalToGoFuseBridge(t *testing.T) {
	ctx := context.Background()
	daemon := newStub(t, nil)
	daemon.entry.Kind = "directory"
	adapter := New(daemon)
	root, ok := adapter.Root().(*entryNode)
	if !ok {
		t.Fatal("Root returned an unexpected node type")
	}
	_ = fs.NewNodeFS(root, &fs.Options{})
	var out fuse.EntryOut
	childInode, errno := root.Lookup(ctx, "file", &out)
	if errno != 0 {
		t.Fatal(errno)
	}
	if errno := root.Rmdir(ctx, "file"); errno != 0 {
		t.Fatalf("rmdir callback: %v", errno)
	}
	if root.GetChild("file") != childInode {
		t.Fatal("Rmdir callback changed inode tree before go-fuse rawBridge")
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

func TestFinalCloseCommitFailureRetainsRecoveryAndPrimaryError(t *testing.T) {
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
	daemon.commitErr = &controlrpc.RemoteError{Code: controlrpc.CodeEntryConflict, Message: "revision changed"}
	daemon.releaseErr = errors.New("cleanup transport failed")
	if errno := handle.Release(ctx); errno != syscall.EAGAIN {
		t.Fatalf("final close errno=%v, want Commit EAGAIN despite cleanup failure", errno)
	}
	if state.sessionID == "" || state.recoveryID != state.sessionID || state.lastErr != syscall.EAGAIN {
		t.Fatalf("final-close failure state session=%q recovery=%q lastErr=%v", state.sessionID, state.recoveryID, state.lastErr)
	}
	session := daemon.sessions[state.recoveryID]
	if session == nil {
		t.Fatal("failed final-close Commit session was not retained for recovery")
	}
	if data, err := os.ReadFile(session.file); err != nil || string(data) != "new" {
		t.Fatalf("recovery staging=%q err=%v, want exact failed bytes", data, err)
	}
	if daemon.commits != 0 || string(daemon.content) != "old" {
		t.Fatalf("failed Commit changed visible content: content=%q commits=%d", daemon.content, daemon.commits)
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
	file := mustFileHandle(t, handle)
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
