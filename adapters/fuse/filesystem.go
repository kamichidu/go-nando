package fusefs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/kamichidu/go-nando/controlrpc"
)

type rpcClient interface {
	Call(context.Context, string, any, any) error
}

type FileSystem struct {
	client rpcClient
	mu     sync.Mutex
	byID   map[string]*entryNode
	writes map[string]*writeState
}

type entryNode struct {
	fs.Inode
	owner *FileSystem
	mu    sync.Mutex
	entry controlrpc.Entry
	path  string
}

type renewer struct {
	stop chan struct{}
	done chan struct{}
	mu   sync.Mutex
	err  error
	once sync.Once
}

type writeState struct {
	mu               sync.Mutex
	node             *entryNode
	sessionID        string
	pendingReleaseID string
	recoveryID       string
	resource         string
	file             *os.File
	lease            *renewer
	handles          int
	dirty            bool
	unlinked         bool
	closed           bool
	lastErr          error
	logicalMode      uint32
}

type fileHandle struct {
	node     *entryNode
	write    *writeState
	readFile *os.File
	readID   string
	lease    *renewer
	writable bool
	append   bool
	once     sync.Once
}

func New(client rpcClient) *FileSystem {
	return &FileSystem{client: client, byID: make(map[string]*entryNode), writes: make(map[string]*writeState)}
}

func (f *FileSystem) Root() fs.InodeEmbedder {
	return &entryNode{owner: f, entry: controlrpc.Entry{ID: "root", Kind: "directory", Mode: 0o755}, path: "/"}
}

func (f *FileSystem) Mount(mountpoint string, options *fs.Options) (*fuse.Server, error) {
	if options == nil {
		options = &fs.Options{}
	}
	copy := *options
	zero := time.Duration(0)
	copy.EntryTimeout, copy.AttrTimeout, copy.NegativeTimeout = &zero, &zero, &zero
	server, err := fs.Mount(mountpoint, f.Root(), &copy)
	return server, err
}

func (f *FileSystem) call(ctx context.Context, method string, params, result any) syscall.Errno {
	if err := f.client.Call(ctx, method, params, result); err != nil {
		return errnoFor(err)
	}
	return 0
}

func (f *FileSystem) cleanupSession(ctx context.Context, id string, primary syscall.Errno) syscall.Errno {
	if cleanup := f.call(ctx, controlrpc.MethodRelease, map[string]string{"session_id": id}, nil); cleanup != 0 {
		return cleanup
	}
	return primary
}

func (f *FileSystem) abortSession(ctx context.Context, id string, primary syscall.Errno) syscall.Errno {
	if cleanup := f.call(ctx, controlrpc.MethodAbort, map[string]string{"session_id": id}, nil); cleanup != 0 {
		return cleanup
	}
	return primary
}

func (f *FileSystem) cleanupSessionError(ctx context.Context, id string, cause error, file *os.File) syscall.Errno {
	return errnoFor(errors.Join(cause, file.Close(), f.client.Call(ctx, controlrpc.MethodAbort, map[string]string{"session_id": id}, nil)))
}

func errnoFor(err error) syscall.Errno {
	if errno, ok := err.(syscall.Errno); ok {
		return errno
	}
	var remote *controlrpc.RemoteError
	if errors.As(err, &remote) {
		switch remote.Code {
		case controlrpc.CodeEntryNotFound:
			return syscall.ENOENT
		case controlrpc.CodeEntryConflict:
			return syscall.EAGAIN
		case controlrpc.CodeEntryExists:
			return syscall.EEXIST
		case controlrpc.CodeDirectoryNotEmpty:
			return syscall.ENOTEMPTY
		case controlrpc.CodePermissionDenied:
			return syscall.EACCES
		case controlrpc.CodeInvalidParams:
			return syscall.EINVAL
		case controlrpc.CodeResourceSessionExpired:
			return syscall.ESTALE
		case -32010:
			return syscall.EOPNOTSUPP
		}
	}
	if errors.Is(err, context.Canceled) {
		return syscall.EINTR
	}
	if errors.Is(err, os.ErrPermission) {
		return syscall.EACCES
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		if e, ok := pathErr.Err.(syscall.Errno); ok {
			return e
		}
	}
	return syscall.EIO
}

func (f *FileSystem) beginRead(ctx context.Context, p, entryID string) (controlrpc.BeginReadResult, *os.File, *renewer, syscall.Errno) {
	var result controlrpc.BeginReadResult
	params := controlrpc.BeginReadParams{Path: p, ResourceSchemes: []string{"file"}, EntryID: entryID}
	if errno := f.call(ctx, controlrpc.MethodBeginRead, params, &result); errno != 0 {
		return result, nil, nil, errno
	}
	resourcePath, errno := localResourcePath(result.ResourceURI)
	if errno != 0 {
		return result, nil, nil, f.cleanupSession(ctx, result.SessionID, errno)
	}
	file, err := os.Open(resourcePath)
	if err != nil {
		return result, nil, nil, f.cleanupSession(ctx, result.SessionID, errnoFor(err))
	}
	return result, file, f.startRenew(result.SessionID), 0
}

func (f *FileSystem) startWrite(ctx context.Context, node *entryNode, truncate bool) (*writeState, syscall.Errno) {
	if errno := f.resolveNode(ctx, node); errno != 0 {
		return nil, errno
	}
	p := node.currentPath()
	var begin controlrpc.BeginWriteResult
	entryID := node.entryID()
	logicalMode := node.entryMode() & 0o7777
	params := controlrpc.BeginWriteParams{Path: p, ResourceSchemes: []string{"file"}, FollowEntryID: entryID != "" && entryID != "root", LogicalMode: &logicalMode}
	if params.FollowEntryID {
		params.EntryID = entryID
	}
	if errno := f.call(ctx, controlrpc.MethodBeginWrite, params, &begin); errno != 0 {
		return nil, errno
	}
	stagePath, errno := localResourcePath(begin.ResourceURI)
	if errno != 0 {
		return nil, f.abortSession(ctx, begin.SessionID, errno)
	}
	stage, err := os.OpenFile(stagePath, os.O_RDWR, 0)
	if err != nil {
		return nil, f.abortSession(ctx, begin.SessionID, errnoFor(err))
	}
	if !truncate && node.entryID() != "" && node.entryID() != "root" {
		read, source, lease, readErrno := f.beginRead(ctx, p, entryID)
		if readErrno != 0 {
			return nil, f.cleanupSessionError(ctx, begin.SessionID, readErrno, stage)
		}
		_, copyErr := io.Copy(stage, source)
		closeErr := source.Close()
		releaseErr := f.client.Call(ctx, controlrpc.MethodRelease, map[string]string{"session_id": read.SessionID}, nil)
		lease.close()
		if err := errors.Join(copyErr, closeErr, releaseErr); err != nil {
			return nil, f.cleanupSessionError(ctx, begin.SessionID, err, stage)
		}
		if _, err := stage.Seek(0, io.SeekStart); err != nil {
			return nil, f.cleanupSessionError(ctx, begin.SessionID, err, stage)
		}
	}
	return &writeState{node: node, sessionID: begin.SessionID, resource: stagePath, file: stage, lease: f.startRenew(begin.SessionID), dirty: truncate, logicalMode: logicalMode}, 0
}

func (f *FileSystem) resolveNode(ctx context.Context, node *entryNode) syscall.Errno {
	id := node.entryID()
	if id == "" || id == "root" {
		return 0
	}
	var result controlrpc.ResolveEntryResult
	if errno := f.call(ctx, controlrpc.MethodResolveEntry, controlrpc.ResolveEntryParams{EntryID: id}, &result); errno != 0 {
		return errno
	}
	node.mu.Lock()
	if result.Entry.Revision >= node.entry.Revision {
		node.entry, node.path = result.Entry, result.Path
	}
	node.mu.Unlock()
	return 0
}

func (f *FileSystem) startRenew(id string) *renewer {
	r := &renewer{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(r.done)
		ticker := time.NewTicker(controlrpc.ResourceSessionRenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-r.stop:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				var result controlrpc.RenewResult
				err := f.client.Call(ctx, controlrpc.MethodRenew, map[string]string{"session_id": id}, &result)
				cancel()
				if err != nil {
					r.mu.Lock()
					r.err = err
					r.mu.Unlock()
					if errors.Is(err, context.Canceled) || errnoFor(err) == syscall.ESTALE || errnoFor(err) == syscall.ENOENT {
						return
					}
				} else {
					r.mu.Lock()
					r.err = nil
					r.mu.Unlock()
				}
			}
		}
	}()
	return r
}

func (r *renewer) failure() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *renewer) close() {
	if r == nil {
		return
	}
	r.once.Do(func() { close(r.stop) })
	<-r.done
}

func localResourcePath(resource string) (string, syscall.Errno) {
	u, err := url.Parse(resource)
	if err != nil || u.Scheme != "file" || u.Host != "" || u.Path == "" || !path.IsAbs(u.Path) {
		return "", syscall.EIO
	}
	return u.Path, 0
}

func (n *entryNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if errno := n.owner.resolveNode(ctx, n); errno != 0 {
		return nil, errno
	}
	if n.currentEntry().Kind != "directory" {
		return nil, syscall.ENOTDIR
	}
	return n.owner.lookup(ctx, &n.Inode, n.currentPath(), name, out)
}

func (f *FileSystem) lookup(ctx context.Context, parent *fs.Inode, parentPath, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if name == "" || strings.Contains(name, "/") || name == "." || name == ".." {
		return nil, syscall.EINVAL
	}
	p := path.Join(parentPath, name)
	var entry controlrpc.Entry
	if errno := f.call(ctx, controlrpc.MethodStat, map[string]string{"path": p}, &entry); errno != 0 {
		return nil, errno
	}
	node := f.nodeFor(ctx, entry, p)
	inode := parent.NewPersistentInode(ctx, node, fs.StableAttr{Mode: inodeMode(entry), Ino: inodeNumber(entry.ID)})
	parent.AddChild(name, inode, true)
	setAttr(&out.Attr, entry, inode.StableAttr().Ino)
	return inode, 0
}

func (f *FileSystem) nodeFor(ctx context.Context, entry controlrpc.Entry, p string) *entryNode {
	f.mu.Lock()
	defer f.mu.Unlock()
	node := f.byID[entry.ID]
	if node == nil {
		node = &entryNode{owner: f}
		f.byID[entry.ID] = node
	}
	node.mu.Lock()
	if entry.Revision >= node.entry.Revision {
		node.entry, node.path = entry, p
	}
	node.mu.Unlock()
	return node
}

func (n *entryNode) currentPath() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.path
}

func (n *entryNode) entryID() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.entry.ID
}

func (n *entryNode) entryMode() uint32 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.entry.Mode
}

func (n *entryNode) currentEntry() controlrpc.Entry {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.entry
}

func (n *entryNode) Getattr(ctx context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if errno := n.owner.resolveNode(ctx, n); errno != 0 {
		return errno
	}
	entry := n.currentEntry()
	setAttr(&out.Attr, entry, n.StableAttr().Ino)
	return 0
}

func (n *entryNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	if errno := n.owner.resolveNode(ctx, n); errno != 0 {
		return nil, errno
	}
	if n.currentEntry().Kind != "directory" {
		return nil, syscall.ENOTDIR
	}
	var entries []controlrpc.Entry
	if errno := n.owner.call(ctx, controlrpc.MethodList, map[string]string{"path": n.currentPath()}, &entries); errno != 0 {
		return nil, errno
	}
	result := make([]fuse.DirEntry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, fuse.DirEntry{Name: entry.Name, Mode: inodeMode(entry), Ino: inodeNumber(entry.ID)})
	}
	return fs.NewListDirStream(result), 0
}

func (n *entryNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if errno := n.owner.resolveNode(ctx, n); errno != 0 {
		return nil, 0, errno
	}
	writable := flags&uint32(os.O_WRONLY|os.O_RDWR) != 0
	if !writable {
		if flags&uint32(os.O_TRUNC) != 0 {
			return nil, 0, syscall.EBADF
		}
		begin, file, lease, errno := n.owner.beginRead(ctx, n.currentPath(), n.entryID())
		if errno != 0 {
			return nil, 0, errno
		}
		h := &fileHandle{node: n, readFile: file, readID: begin.SessionID, lease: lease}
		return h, fuse.FOPEN_DIRECT_IO, 0
	}
	id := n.entryID()
	f := n.owner
	f.mu.Lock()
	state := f.writes[id]
	f.mu.Unlock()
	if state != nil {
		return openSharedWriter(ctx, n, state, flags)
	}
	fresh, errno := f.startWrite(ctx, n, flags&uint32(os.O_TRUNC) != 0)
	if errno != 0 {
		return nil, 0, errno
	}
	f.mu.Lock()
	state = f.writes[id]
	if state == nil {
		fresh.handles = 1
		f.writes[id] = fresh
		state = fresh
	}
	f.mu.Unlock()
	if state != fresh {
		fresh.lease.close()
		if err := errors.Join(fresh.file.Close(), f.client.Call(ctx, controlrpc.MethodAbort, map[string]string{"session_id": fresh.sessionID}, nil)); err != nil {
			return nil, 0, errnoFor(err)
		}
		return openSharedWriter(ctx, n, state, flags)
	}
	return &fileHandle{node: n, write: state, writable: true, append: flags&uint32(os.O_APPEND) != 0}, fuse.FOPEN_DIRECT_IO, 0
}

func openSharedWriter(ctx context.Context, node *entryNode, state *writeState, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return nil, 0, syscall.ESTALE
	}
	if state.lastErr != nil {
		return nil, 0, errnoFor(state.lastErr)
	}
	state.handles++
	if flags&uint32(os.O_TRUNC) != 0 && !state.unlinked {
		if state.sessionID == "" {
			if errno := (&fileHandle{node: node}).restartWrite(ctx, state); errno != 0 {
				state.handles--
				return nil, 0, errno
			}
		}
		if state.file == nil {
			state.handles--
			return nil, 0, syscall.ESTALE
		}
		if err := state.file.Truncate(0); err != nil {
			state.handles--
			return nil, 0, errnoFor(err)
		}
		state.dirty = true
	}
	return &fileHandle{node: node, write: state, writable: true, append: flags&uint32(os.O_APPEND) != 0}, fuse.FOPEN_DIRECT_IO, 0
}

func (h *fileHandle) Read(_ context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	var file *os.File
	if h.write != nil {
		h.write.mu.Lock()
		defer h.write.mu.Unlock()
		if h.write.closed {
			return nil, syscall.ESTALE
		}
		file = h.write.file
	} else {
		file = h.readFile
	}
	if file == nil {
		return nil, syscall.ESTALE
	}
	n, err := file.ReadAt(dest, off)
	if err != nil && err != io.EOF {
		return nil, errnoFor(err)
	}
	return fuse.ReadResultData(dest[:n]), 0
}

func (h *fileHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	if !h.writable || h.write == nil {
		return 0, syscall.EBADF
	}
	state := h.write
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return 0, syscall.ESTALE
	}
	if state.unlinked {
		if state.file == nil {
			return 0, syscall.ESTALE
		}
	} else if state.lastErr != nil {
		return 0, errnoFor(state.lastErr)
	} else if err := state.lease.failure(); err != nil {
		return 0, errnoFor(err)
	} else if state.sessionID == "" {
		if errno := h.restartWrite(ctx, state); errno != 0 {
			return 0, errno
		}
	}
	if state.file == nil {
		return 0, syscall.ESTALE
	}
	if ctx.Err() != nil {
		return 0, syscall.EINTR
	}
	if h.append {
		info, statErr := state.file.Stat()
		if statErr != nil {
			return 0, errnoFor(statErr)
		}
		off = info.Size()
	}
	n, err := state.file.WriteAt(data, off)
	if err != nil {
		if n > 0 {
			state.dirty = true
		}
		return uint32(n), errnoFor(err)
	}
	state.dirty = true
	state.lastErr = nil
	return uint32(n), 0
}

func (h *fileHandle) restartWrite(ctx context.Context, state *writeState) syscall.Errno {
	if state.pendingReleaseID != "" {
		if errno := state.node.owner.call(ctx, controlrpc.MethodRelease, map[string]string{"session_id": state.pendingReleaseID}, nil); errno != 0 {
			return errno
		}
		state.pendingReleaseID = ""
	}
	fresh, errno := state.node.owner.startWrite(ctx, state.node, false)
	if errno != 0 {
		return errno
	}
	oldFile, oldLease := state.file, state.lease
	state.sessionID, state.resource, state.file, state.lease = fresh.sessionID, fresh.resource, fresh.file, fresh.lease
	state.logicalMode = fresh.logicalMode
	state.dirty, state.lastErr = false, nil
	state.recoveryID = ""
	if oldFile != nil {
		if err := oldFile.Close(); err != nil {
			log.Printf("close replaced staging descriptor for Entry %s: %v", state.node.entryID(), err)
		}
	}
	if oldLease != nil {
		oldLease.close()
	}
	return 0
}

func (h *fileHandle) Fsync(ctx context.Context, _ uint32) syscall.Errno {
	if h.write == nil {
		return 0
	}
	state := h.write
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return syscall.ESTALE
	}
	if state.unlinked {
		if state.file == nil {
			return syscall.ESTALE
		}
		if err := state.file.Sync(); err != nil {
			return errnoFor(err)
		}
		return 0
	}
	if state.lastErr != nil {
		return errnoFor(state.lastErr)
	}
	if state.file == nil {
		return syscall.ESTALE
	}
	if !state.dirty {
		return 0
	}
	if err := state.file.Sync(); err != nil {
		return errnoFor(err)
	}
	if err := state.lease.failure(); err != nil {
		return errnoFor(err)
	}
	if errno := state.node.owner.call(ctx, controlrpc.MethodCommit, map[string]string{"session_id": state.sessionID}, nil); errno != 0 {
		state.lastErr = errno
		state.recoveryID = state.sessionID
		state.dirty = false
		state.lease.close()
		state.lease = nil
		state.sessionID = ""
		return errno
	}
	committedSessionID := state.sessionID
	state.lease.close()
	state.lease, state.sessionID = nil, ""
	state.dirty, state.lastErr = false, nil
	state.pendingReleaseID = committedSessionID
	if errno := state.node.owner.resolveNode(ctx, state.node); errno != 0 {
		log.Printf("refresh committed Entry %s after Fsync: %v", state.node.entryID(), errno)
	}
	if errno := state.node.owner.call(ctx, controlrpc.MethodRelease, map[string]string{"session_id": committedSessionID}, nil); errno != 0 {
		return errno
	}
	state.pendingReleaseID = ""
	return 0
}

func (h *fileHandle) Flush(context.Context) syscall.Errno { return 0 }

func (h *fileHandle) Release(ctx context.Context) syscall.Errno {
	var result syscall.Errno
	h.once.Do(func() {
		if h.write != nil {
			state := h.write
			state.mu.Lock()
			state.handles--
			if state.handles == 0 {
				state.closed = true
				if state.dirty && !state.unlinked {
					if err := state.file.Sync(); err != nil {
						result = errnoFor(err)
					} else if err := state.lease.failure(); err != nil {
						result = errnoFor(err)
					} else {
						result = state.node.owner.call(ctx, controlrpc.MethodCommit, map[string]string{"session_id": state.sessionID}, nil)
					}
				}
				if state.lease != nil {
					state.lease.close()
				}
				if state.sessionID != "" {
					method := controlrpc.MethodRelease
					if !state.dirty && !state.unlinked {
						method = controlrpc.MethodAbort
					}
					if errno := state.node.owner.call(ctx, method, map[string]string{"session_id": state.sessionID}, nil); result == 0 {
						result = errno
					}
				}
				if state.pendingReleaseID != "" {
					if errno := state.node.owner.call(ctx, controlrpc.MethodRelease, map[string]string{"session_id": state.pendingReleaseID}, nil); result == 0 {
						result = errno
					}
				}
				if state.unlinked && state.recoveryID != "" {
					if errno := state.node.owner.call(ctx, controlrpc.MethodRelease, map[string]string{"session_id": state.recoveryID}, nil); result == 0 {
						result = errno
					}
				}
				if state.file != nil {
					if err := state.file.Close(); err != nil && result == 0 {
						result = errnoFor(err)
					}
				}
				f := state.node.owner
				state.mu.Unlock()
				f.mu.Lock()
				if f.writes[state.node.entryID()] == state {
					delete(f.writes, state.node.entryID())
				}
				f.mu.Unlock()
				state.mu.Lock()
			}
			state.mu.Unlock()
		} else {
			if h.readFile != nil {
				if err := h.readFile.Close(); err != nil {
					result = errnoFor(err)
				}
			}
			if h.lease != nil {
				h.lease.close()
			}
			if h.readID != "" {
				if errno := h.node.owner.call(ctx, controlrpc.MethodRelease, map[string]string{"session_id": h.readID}, nil); result == 0 {
					result = errno
				}
			}
		}
	})
	return result
}

func (n *entryNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	size, sizeSet := in.GetSize()
	mode, modeSet := in.GetMode()
	if !sizeSet && !modeSet {
		return n.Getattr(ctx, nil, out)
	}
	h, hasHandle := f.(*fileHandle)
	var state *writeState
	ephemeral := false
	if hasHandle && h.write != nil {
		state = h.write
	} else if state = n.owner.writeState(n.entryID()); state == nil {
		var errno syscall.Errno
		state, errno = n.owner.startWrite(ctx, n, false)
		if errno != 0 {
			return errno
		}
		ephemeral = true
	}
	state.mu.Lock()
	if state.closed {
		state.mu.Unlock()
		return syscall.ESTALE
	}
	if state.lastErr != nil && !state.unlinked {
		state.mu.Unlock()
		return errnoFor(state.lastErr)
	}
	if !state.unlinked && state.sessionID == "" {
		if errno := (&fileHandle{node: n}).restartWrite(ctx, state); errno != 0 {
			state.mu.Unlock()
			return errno
		}
	}
	if sizeSet {
		if err := state.file.Truncate(int64(size)); err != nil {
			state.mu.Unlock()
			return errnoFor(err)
		}
	}
	if modeSet {
		logicalMode := mode & 0o7777
		if errno := n.owner.call(ctx, controlrpc.MethodSetMode, controlrpc.SetModeParams{SessionID: state.sessionID, Mode: logicalMode}, nil); errno != 0 {
			state.mu.Unlock()
			return errno
		}
		state.logicalMode = logicalMode
	}
	state.dirty = true
	unlinked := state.unlinked
	logicalMode := state.logicalMode
	stagedInfo, err := state.file.Stat()
	if err != nil {
		state.mu.Unlock()
		return errnoFor(err)
	}
	state.mu.Unlock()
	if unlinked {
		return (&fileHandle{node: n, write: state}).Getattr(ctx, out)
	}
	if ephemeral && !state.unlinked {
		if errno := (&fileHandle{node: n, write: state, writable: true}).Fsync(ctx, 0); errno != 0 {
			if state.file != nil {
				if err := state.file.Close(); err != nil {
					log.Printf("close setattr staging descriptor for Entry %s: %v", n.entryID(), err)
				}
			}
			return errno
		}
		if state.file != nil {
			if err := state.file.Close(); err != nil {
				log.Printf("close completed setattr staging descriptor for Entry %s: %v", n.entryID(), err)
			}
		}
	}
	if errno := n.getattrWithMode(ctx, out, logicalMode); errno != 0 {
		return errno
	}
	out.Size = uint64(max(stagedInfo.Size(), 0))
	out.Mtime = uint64(stagedInfo.ModTime().Unix())
	out.Mtimensec = uint32(stagedInfo.ModTime().Nanosecond())
	return 0
}

func (h *fileHandle) Getattr(ctx context.Context, out *fuse.AttrOut) syscall.Errno {
	var file *os.File
	if h.write != nil {
		h.write.mu.Lock()
		defer h.write.mu.Unlock()
		file = h.write.file
	} else {
		file = h.readFile
	}
	if file == nil {
		return syscall.ESTALE
	}
	info, err := file.Stat()
	if err != nil {
		return errnoFor(err)
	}
	if errno := h.node.owner.resolveNode(ctx, h.node); errno != 0 {
		return errno
	}
	entry := h.node.currentEntry()
	mode := entry.Mode & 0o7777
	if h.write != nil {
		mode = h.write.logicalMode
	}
	setAttr(&out.Attr, entry, h.node.StableAttr().Ino)
	out.Mode = inodeMode(entry)&^0o7777 | mode
	out.Size = uint64(max(info.Size(), 0))
	out.Mtime = uint64(info.ModTime().Unix())
	out.Mtimensec = uint32(info.ModTime().Nanosecond())
	return 0
}

func (n *entryNode) getattrWithMode(ctx context.Context, out *fuse.AttrOut, mode uint32) syscall.Errno {
	if errno := n.owner.resolveNode(ctx, n); errno != 0 {
		return errno
	}
	entry := n.currentEntry()
	entry.Mode = entry.Mode&^0o7777 | mode&0o7777
	setAttr(&out.Attr, entry, n.StableAttr().Ino)
	return 0
}

func (n *entryNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if n.currentEntry().Kind != "directory" || name == "" || strings.Contains(name, "/") {
		return nil, nil, 0, syscall.EINVAL
	}
	p := path.Join(n.currentPath(), name)
	var existing controlrpc.Entry
	if errno := n.owner.call(ctx, controlrpc.MethodStat, map[string]string{"path": p}, &existing); errno == 0 {
		if flags&uint32(os.O_EXCL) != 0 {
			return nil, nil, 0, syscall.EEXIST
		}
		node := n.owner.nodeFor(ctx, existing, p)
		child := n.NewPersistentInode(ctx, node, fs.StableAttr{Mode: inodeMode(existing), Ino: inodeNumber(existing.ID)})
		n.AddChild(name, child, true)
		setAttr(&out.Attr, existing, child.StableAttr().Ino)
		handle, fuseFlags, errno := node.Open(ctx, flags)
		return child, handle, fuseFlags, errno
	} else if errno != syscall.ENOENT {
		return nil, nil, 0, errno
	}
	// Publish an empty Entry at Create so the name is immediately visible and
	// subsequent rename/unlink operations have a stable Entry identity.
	var begin controlrpc.BeginWriteResult
	logicalMode := mode & 0o7777
	if errno := n.owner.call(ctx, controlrpc.MethodBeginWrite, controlrpc.BeginWriteParams{Path: p, ResourceSchemes: []string{"file"}, LogicalMode: &logicalMode}, &begin); errno != 0 {
		return nil, nil, 0, errno
	}
	if errno := n.owner.call(ctx, controlrpc.MethodCommit, map[string]string{"session_id": begin.SessionID}, nil); errno != 0 {
		if errno == syscall.EAGAIN {
			var raced controlrpc.Entry
			if statErrno := n.owner.call(ctx, controlrpc.MethodStat, map[string]string{"path": p}, &raced); statErrno == 0 {
				if abortErrno := n.owner.call(ctx, controlrpc.MethodAbort, map[string]string{"session_id": begin.SessionID}, nil); abortErrno != 0 {
					return nil, nil, 0, abortErrno
				}
				if flags&uint32(os.O_EXCL) != 0 {
					return nil, nil, 0, syscall.EEXIST
				}
				node := n.owner.nodeFor(ctx, raced, p)
				child := n.NewPersistentInode(ctx, node, fs.StableAttr{Mode: inodeMode(raced), Ino: inodeNumber(raced.ID)})
				n.AddChild(name, child, true)
				setAttr(&out.Attr, raced, child.StableAttr().Ino)
				handle, fuseFlags, openErrno := node.Open(ctx, flags)
				return child, handle, fuseFlags, openErrno
			}
		}
		return nil, nil, 0, errno
	}
	if errno := n.owner.call(ctx, controlrpc.MethodRelease, map[string]string{"session_id": begin.SessionID}, nil); errno != 0 {
		return nil, nil, 0, errno
	}
	var entry controlrpc.Entry
	if errno := n.owner.call(ctx, controlrpc.MethodStat, map[string]string{"path": p}, &entry); errno != 0 {
		return nil, nil, 0, errno
	}
	node := n.owner.nodeFor(ctx, entry, p)
	child := n.NewPersistentInode(ctx, node, fs.StableAttr{Mode: inodeMode(entry), Ino: inodeNumber(entry.ID)})
	n.AddChild(name, child, true)
	setAttr(&out.Attr, entry, child.StableAttr().Ino)
	handle, _, errno := node.Open(ctx, flags&^uint32(os.O_TRUNC))
	if errno != 0 {
		return nil, nil, 0, errno
	}
	return child, handle, 0, 0
}

func (n *entryNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if n.currentEntry().Kind != "directory" || name == "" || strings.Contains(name, "/") {
		return nil, syscall.EINVAL
	}
	p := path.Join(n.currentPath(), name)
	if errno := n.owner.call(ctx, controlrpc.MethodMkdir, map[string]any{"path": p, "mode": mode & 0o7777}, nil); errno != 0 {
		return nil, errno
	}
	var entry controlrpc.Entry
	if errno := n.owner.call(ctx, controlrpc.MethodStat, map[string]string{"path": p}, &entry); errno != 0 {
		return nil, errno
	}
	node := n.owner.nodeFor(ctx, entry, p)
	child := n.NewPersistentInode(ctx, node, fs.StableAttr{Mode: inodeMode(entry), Ino: inodeNumber(entry.ID)})
	n.AddChild(name, child, true)
	setAttr(&out.Attr, entry, child.StableAttr().Ino)
	return child, 0
}

func (n *entryNode) Unlink(ctx context.Context, name string) syscall.Errno {
	if errno := n.owner.resolveNode(ctx, n); errno != 0 {
		return errno
	}
	p := path.Join(n.currentPath(), name)
	var entry controlrpc.Entry
	if errno := n.owner.call(ctx, controlrpc.MethodStat, map[string]string{"path": p}, &entry); errno != 0 {
		return errno
	}
	if entry.Kind == "directory" {
		return syscall.EISDIR
	}
	if state := n.owner.writeState(entry.ID); state != nil {
		state.mu.Lock()
		if state.closed {
			state.mu.Unlock()
			return syscall.EAGAIN
		}
		if state.sessionID == "" && state.recoveryID == "" {
			if errno := (&fileHandle{node: state.node}).restartWrite(ctx, state); errno != 0 {
				state.mu.Unlock()
				return errno
			}
		}
		if state.sessionID != "" {
			if errno := n.owner.call(ctx, controlrpc.MethodMarkUnlinked, map[string]string{"session_id": state.sessionID}, nil); errno != 0 {
				state.mu.Unlock()
				return errno
			}
		}
		if state.recoveryID != "" {
			if errno := n.owner.call(ctx, controlrpc.MethodMarkUnlinked, map[string]string{"session_id": state.recoveryID}, nil); errno != 0 {
				state.mu.Unlock()
				return errno
			}
		}
		state.unlinked = true
		state.mu.Unlock()
	}
	if errno := n.owner.call(ctx, controlrpc.MethodRemoveEntry, controlrpc.RemoveEntryParams{EntryID: entry.ID}, nil); errno != 0 {
		return errno
	}
	// go-fuse's rawBridge removes the cached child after this callback returns.
	// Sending a synchronous FUSE notification here can block the request whose
	// reply the kernel is waiting for; duplicate RmChild also races the bridge.
	return 0
}

func (n *entryNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	if errno := n.owner.resolveNode(ctx, n); errno != 0 {
		return errno
	}
	p := path.Join(n.currentPath(), name)
	var entry controlrpc.Entry
	if errno := n.owner.call(ctx, controlrpc.MethodStat, map[string]string{"path": p}, &entry); errno != 0 {
		return errno
	}
	if entry.Kind != "directory" {
		return syscall.ENOTDIR
	}
	if errno := n.owner.call(ctx, controlrpc.MethodRemove, map[string]string{"path": p}, nil); errno != 0 {
		return errno
	}
	// go-fuse's rawBridge removes the cached child after this callback returns.
	return 0
}

func (n *entryNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if flags != 0 {
		return syscall.EINVAL
	}
	if name == "" || newName == "" || strings.Contains(name, "/") || strings.Contains(newName, "/") || newName == "." || newName == ".." {
		return syscall.EINVAL
	}
	dstParent, ok := newParent.(*entryNode)
	if !ok || dstParent.owner != n.owner {
		return syscall.EXDEV
	}
	if errno := n.owner.resolveNode(ctx, n); errno != 0 {
		return errno
	}
	if errno := n.owner.resolveNode(ctx, dstParent); errno != 0 {
		return errno
	}
	src := path.Join(n.currentPath(), name)
	dst := path.Join(dstParent.currentPath(), newName)
	var existing controlrpc.Entry
	if errno := n.owner.call(ctx, controlrpc.MethodStat, map[string]string{"path": dst}, &existing); errno == 0 {
		var source controlrpc.Entry
		if sourceErrno := n.owner.call(ctx, controlrpc.MethodStat, map[string]string{"path": src}, &source); sourceErrno != 0 {
			return sourceErrno
		}
		if source.ID == existing.ID {
			return 0
		}
		return syscall.EEXIST
	} else if errno != syscall.ENOENT {
		return errno
	}
	var source controlrpc.Entry
	if errno := n.owner.call(ctx, controlrpc.MethodStat, map[string]string{"path": src}, &source); errno != 0 {
		return errno
	}
	state := n.owner.writeState(source.ID)
	if state != nil {
		state.mu.Lock()
		defer state.mu.Unlock()
	}
	if errno := n.owner.call(ctx, controlrpc.MethodMoveEntry, controlrpc.MoveEntryParams{EntryID: source.ID, Destination: dst}, nil); errno != 0 {
		return errno
	}
	n.owner.renameCached(src, dst)
	// rawBridge performs MvChild after this callback succeeds. Entry and
	// attribute TTLs are zero, so external daemon mutations are looked up again;
	// do not synchronously notify the kernel while it awaits this Rename reply.
	return 0
}

func (n *entryNode) Link(context.Context, fs.InodeEmbedder, string, *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	return nil, syscall.EOPNOTSUPP
}

func (f *FileSystem) writeState(id string) *writeState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes[id]
}

func (f *FileSystem) renameCached(oldPath, newPath string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, node := range f.byID {
		node.mu.Lock()
		if node.path == oldPath || strings.HasPrefix(node.path, strings.TrimSuffix(oldPath, "/")+"/") {
			node.path = newPath + strings.TrimPrefix(node.path, oldPath)
		}
		node.mu.Unlock()
	}
}

func inodeMode(entry controlrpc.Entry) uint32 {
	mode := entry.Mode & 0o7777
	if mode == 0 {
		mode = 0o644
	}
	if entry.Kind == "directory" {
		return syscall.S_IFDIR | mode
	}
	return syscall.S_IFREG | mode
}

func inodeNumber(id string) uint64 {
	var result uint64 = 1469598103934665603
	for _, b := range []byte(id) {
		result ^= uint64(b)
		result *= 1099511628211
	}
	if result < 2 {
		return result + 2
	}
	return result & ((uint64(1) << 63) - 1)
}

func setAttr(attr *fuse.Attr, entry controlrpc.Entry, ino uint64) {
	attr.Ino = ino
	attr.Mode = inodeMode(entry)
	attr.Nlink = 1
	if entry.Kind == "directory" {
		attr.Nlink = 2
	}
	attr.Size = uint64(max(entry.Size, 0))
	attr.Uid, attr.Gid = uint32(os.Getuid()), uint32(os.Getgid())
	attr.Mtime = uint64(entry.Mtime / int64(time.Second))
	attr.Mtimensec = uint32(entry.Mtime % int64(time.Second))
	attr.Ctime = uint64(entry.UpdatedAt / int64(time.Second))
	attr.Ctimensec = uint32(entry.UpdatedAt % int64(time.Second))
	attr.Atime = attr.Mtime
	attr.Atimensec = attr.Mtimensec
	attr.Blksize = 4096
	attr.Blocks = (attr.Size + 511) / 512
}

func (h *fileHandle) String() string { return fmt.Sprintf("fuse handle %s", h.node.entryID()) }
