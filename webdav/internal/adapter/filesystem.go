package adapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/webdav"

	"github.com/kamichidu/go-nando/controlrpc"
)

type rpcClient interface {
	Call(context.Context, string, any, any) error
}

type Handler struct {
	client rpcClient
	dav    http.Handler
}

func New(client rpcClient) http.Handler {
	fs := &filesystem{client: client}
	return &Handler{client: client, dav: &webdav.Handler{FileSystem: fs, LockSystem: webdav.NewMemLS()}}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodOptions:
		w.Header().Set("DAV", "1")
		w.Header().Set("Allow", "GET, HEAD, OPTIONS, PROPFIND, PUT, DELETE, MKCOL, MOVE, COPY")
		w.WriteHeader(http.StatusOK)
	case http.MethodPut:
		h.put(w, r)
	case "MKCOL":
		h.mkcol(w, r)
	case http.MethodDelete:
		h.delete(w, r)
	case "MOVE":
		h.move(w, r)
	case "COPY":
		h.copyFile(w, r)
	case "LOCK", "UNLOCK", "PROPPATCH":
		w.Header().Set("Allow", "GET, HEAD, OPTIONS, PROPFIND, PUT, DELETE, MKCOL, MOVE, COPY")
		http.Error(w, "method not supported by Phase A repository contract", http.StatusNotImplemented)
	default:
		h.dav.ServeHTTP(w, r)
	}
}

type filesystem struct{ client rpcClient }

func (f *filesystem) call(ctx context.Context, method string, params, result any) error {
	if err := f.client.Call(ctx, method, params, result); err != nil {
		return translate(err)
	}
	return nil
}
func cleanName(n string) (string, error) {
	if strings.ContainsRune(n, '\x00') {
		return "", os.ErrInvalid
	}
	if n == "" {
		return "/", nil
	}
	if !strings.HasPrefix(n, "/") {
		n = "/" + n
	}
	return path.Clean(n), nil
}
func (f *filesystem) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	p, err := cleanName(name)
	if err != nil {
		return nil, err
	}
	var entry controlrpc.Entry
	if err = f.call(ctx, controlrpc.MethodStat, map[string]string{"path": p}, &entry); err != nil {
		return nil, err
	}
	return entryInfo{entry: entry}, nil
}
func (f *filesystem) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	if flag != os.O_RDONLY {
		return nil, syscall.EOPNOTSUPP
	}
	p, err := cleanName(name)
	if err != nil {
		return nil, err
	}
	var entry controlrpc.Entry
	if err := f.call(ctx, controlrpc.MethodStat, map[string]string{"path": p}, &entry); err != nil {
		return nil, err
	}
	if entry.Kind == "directory" {
		var entries []controlrpc.Entry
		if err := f.call(ctx, controlrpc.MethodList, map[string]string{"path": p}, &entries); err != nil {
			return nil, err
		}
		return &directoryFile{entryInfo: entryInfo{entry: entry}, entries: entries}, nil
	}
	var result controlrpc.BeginReadResult
	if err = f.call(ctx, controlrpc.MethodBeginRead, controlrpc.BeginReadParams{Path: p, ResourceSchemes: []string{"file"}}, &result); err != nil {
		return nil, err
	}
	entry = result.Entry
	resource, err := localPath(result.ResourceURI)
	if err != nil {
		return nil, errors.Join(err, f.client.Call(context.Background(), controlrpc.MethodRelease, map[string]string{"session_id": result.SessionID}, nil))
	}
	file, err := os.Open(resource)
	if err != nil {
		return nil, errors.Join(err, f.client.Call(context.Background(), controlrpc.MethodRelease, map[string]string{"session_id": result.SessionID}, nil))
	}
	read := &readFile{File: file, info: entryInfo{entry: result.Entry}, client: f.client, sessionID: result.SessionID, stop: make(chan struct{}), done: make(chan struct{})}
	go read.renew()
	return read, nil
}
func (f *filesystem) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	p, err := cleanName(name)
	if err != nil {
		return err
	}
	return f.call(ctx, controlrpc.MethodMkdir, map[string]any{"path": p, "mode": uint32(perm.Perm())}, nil)
}
func (f *filesystem) RemoveAll(ctx context.Context, name string) error {
	p, err := cleanName(name)
	if err != nil {
		return err
	}
	if p == "/" {
		return os.ErrPermission
	}
	return f.call(ctx, controlrpc.MethodRemove, map[string]string{"path": p}, nil)
}
func (f *filesystem) Rename(ctx context.Context, oldName, newName string) error {
	oldPath, err := cleanName(oldName)
	if err != nil {
		return err
	}
	newPath, err := cleanName(newName)
	if err != nil {
		return err
	}
	if oldPath == "/" || newPath == "/" {
		return os.ErrPermission
	}
	return f.call(ctx, controlrpc.MethodMove, map[string]string{"source": oldPath, "destination": newPath}, nil)
}

type readFile struct {
	*os.File
	info      entryInfo
	client    rpcClient
	sessionID string
	stop      chan struct{}
	done      chan struct{}
	errMu     sync.Mutex
	renewErr  error
}

func (f *readFile) Stat() (os.FileInfo, error) { return f.info, nil }

func (f *readFile) Close() error {
	close(f.stop)
	<-f.done
	f.errMu.Lock()
	renewErr := f.renewErr
	f.errMu.Unlock()
	return errors.Join(f.File.Close(), renewErr, f.client.Call(context.Background(), controlrpc.MethodRelease, map[string]string{"session_id": f.sessionID}, nil))
}
func (f *readFile) Read(p []byte) (int, error) {
	f.errMu.Lock()
	err := f.renewErr
	f.errMu.Unlock()
	if err != nil {
		return 0, err
	}
	return f.File.Read(p)
}
func (f *readFile) renew() {
	defer close(f.done)
	ticker := time.NewTicker(controlrpc.ResourceSessionRenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-f.stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			var result controlrpc.RenewResult
			err := f.client.Call(ctx, controlrpc.MethodRenew, map[string]string{"session_id": f.sessionID}, &result)
			cancel()
			if err != nil {
				f.errMu.Lock()
				f.renewErr = err
				f.errMu.Unlock()
				return
			}
		}
	}
}

type entryInfo struct{ entry controlrpc.Entry }

func (i entryInfo) Name() string { return i.entry.Name }
func (i entryInfo) Size() int64  { return i.entry.Size }
func (i entryInfo) Mode() os.FileMode {
	mode := os.FileMode(i.entry.Mode).Perm()
	if i.entry.Kind == "directory" {
		mode |= os.ModeDir
	}
	return mode
}
func (i entryInfo) ModTime() time.Time { return time.Unix(0, i.entry.Mtime) }
func (i entryInfo) IsDir() bool        { return i.entry.Kind == "directory" }
func (i entryInfo) Sys() any           { return i.entry }
func (i entryInfo) ETag(context.Context) (string, error) {
	return fmt.Sprintf("\"%s-%d\"", i.entry.ID, i.entry.Revision), nil
}

type directoryFile struct {
	entryInfo
	entries []controlrpc.Entry
	read    int
}

func (f *directoryFile) Close() error                   { return nil }
func (f *directoryFile) Stat() (os.FileInfo, error)     { return f.entryInfo, nil }
func (f *directoryFile) Read([]byte) (int, error)       { return 0, syscall.EISDIR }
func (f *directoryFile) Write([]byte) (int, error)      { return 0, syscall.EISDIR }
func (f *directoryFile) Seek(int64, int) (int64, error) { return 0, syscall.EISDIR }
func (f *directoryFile) Readdir(count int) ([]os.FileInfo, error) {
	if f.read >= len(f.entries) {
		return nil, io.EOF
	}
	end := len(f.entries)
	if count > 0 && end-f.read > count {
		end = f.read + count
	}
	out := make([]os.FileInfo, 0, end-f.read)
	for _, e := range f.entries[f.read:end] {
		out = append(out, entryInfo{entry: e})
	}
	f.read = end
	return out, nil
}

func localPath(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" || u.Host != "" || u.Path == "" {
		return "", fmt.Errorf("invalid daemon file Resource URI %q", raw)
	}
	return u.Path, nil
}
func translate(err error) error {
	var remote *controlrpc.RemoteError
	if !errors.As(err, &remote) {
		return err
	}
	switch remote.Code {
	case controlrpc.CodeEntryNotFound:
		return os.ErrNotExist
	case controlrpc.CodeEntryExists:
		return os.ErrExist
	case controlrpc.CodeDirectoryNotEmpty:
		return syscall.ENOTEMPTY
	case controlrpc.CodeEntryConflict:
		return syscall.EAGAIN
	case controlrpc.CodePermissionDenied:
		return os.ErrPermission
	case controlrpc.CodeInvalidParams:
		return os.ErrInvalid
	case controlrpc.CodeResourceSessionExpired:
		return syscall.ESTALE
	default:
		return err
	}
}

func (h *Handler) put(w http.ResponseWriter, r *http.Request) {
	p, err := cleanName(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var begin controlrpc.BeginWriteResult
	if err = h.client.Call(r.Context(), controlrpc.MethodBeginWrite, controlrpc.BeginWriteParams{Path: p, ResourceSchemes: []string{"file"}}, &begin); err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	if !preconditionsMatch(r, begin) {
		abortErr := h.client.Call(context.Background(), controlrpc.MethodAbort, map[string]string{"session_id": begin.SessionID}, nil)
		if abortErr != nil {
			http.Error(w, "precondition failed; abort failed: "+abortErr.Error(), http.StatusInternalServerError)
			return
		}
		http.Error(w, "HTTP conditional request did not match the Begin-time Entry snapshot", http.StatusPreconditionFailed)
		return
	}
	resource, err := localPath(begin.ResourceURI)
	if err != nil {
		http.Error(w, errors.Join(err, h.client.Call(context.Background(), controlrpc.MethodAbort, map[string]string{"session_id": begin.SessionID}, nil)).Error(), http.StatusInternalServerError)
		return
	}
	file, err := os.OpenFile(resource, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		http.Error(w, errors.Join(err, h.client.Call(context.Background(), controlrpc.MethodRelease, map[string]string{"session_id": begin.SessionID}, nil)).Error(), http.StatusInternalServerError)
		return
	}
	_, copyErr := copyWithRenew(r.Context(), h.client, begin.SessionID, file, r.Body)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		http.Error(w, errors.Join(err, h.client.Call(context.Background(), controlrpc.MethodRelease, map[string]string{"session_id": begin.SessionID}, nil)).Error(), http.StatusInternalServerError)
		return
	}
	if err := h.client.Call(r.Context(), controlrpc.MethodCommit, map[string]string{"session_id": begin.SessionID}, nil); err != nil {
		http.Error(w, err.Error(), preconditionStatus(err))
		return
	}
	var entry controlrpc.Entry
	if err := h.client.Call(r.Context(), controlrpc.MethodStat, map[string]string{"path": p}, &entry); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("ETag", etag(entry))
	if begin.MustBeAbsent {
		w.WriteHeader(http.StatusCreated)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *Handler) mkcol(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > 0 {
		http.Error(w, "MKCOL request body is unsupported", http.StatusUnsupportedMediaType)
		return
	}
	p, err := cleanName(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err = h.client.Call(r.Context(), controlrpc.MethodMkdir, map[string]any{"path": p, "mode": uint32(0755)}, nil); err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	p, err := cleanName(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if p == "/" {
		http.Error(w, "cannot delete repository root", http.StatusForbidden)
		return
	}
	if err = h.client.Call(r.Context(), controlrpc.MethodRemove, map[string]string{"path": p}, nil); err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) move(w http.ResponseWriter, r *http.Request) {
	src, err := cleanName(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	u, err := url.Parse(r.Header.Get("Destination"))
	if err != nil || r.Header.Get("Destination") == "" {
		http.Error(w, "Destination header is required", 400)
		return
	}
	if u.IsAbs() && u.Host != r.Host {
		http.Error(w, "cross-origin MOVE is unsupported", 400)
		return
	}
	dst, err := cleanName(u.Path)
	if err != nil || src == "/" || dst == "/" {
		http.Error(w, "invalid MOVE path", 400)
		return
	}
	var exists controlrpc.Entry
	if err := h.client.Call(r.Context(), controlrpc.MethodStat, map[string]string{"path": dst}, &exists); err == nil {
		if r.Header.Get("Overwrite") == "F" {
			http.Error(w, "destination exists", http.StatusPreconditionFailed)
		} else {
			http.Error(w, "MOVE replacement is outside the Phase A repository contract", http.StatusNotImplemented)
		}
		return
	} else if statusFor(err) != http.StatusNotFound {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	if err := h.client.Call(r.Context(), controlrpc.MethodMove, map[string]string{"source": src, "destination": dst}, nil); err != nil {
		http.Error(w, err.Error(), preconditionStatus(err))
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) copyFile(w http.ResponseWriter, r *http.Request) {
	src, err := cleanName(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	dstURL, err := url.Parse(r.Header.Get("Destination"))
	if err != nil || r.Header.Get("Destination") == "" {
		http.Error(w, "Destination header is required", http.StatusBadRequest)
		return
	}
	if dstURL.IsAbs() && dstURL.Host != r.Host {
		http.Error(w, "cross-origin COPY is unsupported", http.StatusBadRequest)
		return
	}
	dst, err := cleanName(dstURL.Path)
	if err != nil || dst == "/" {
		http.Error(w, "invalid destination", http.StatusBadRequest)
		return
	}
	var info controlrpc.Entry
	if err := h.client.Call(r.Context(), controlrpc.MethodStat, map[string]string{"path": src}, &info); err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	if info.Kind != "file" {
		http.Error(w, "directory COPY is outside Phase A", http.StatusNotImplemented)
		return
	}
	var target controlrpc.Entry
	statErr := h.client.Call(r.Context(), controlrpc.MethodStat, map[string]string{"path": dst}, &target)
	if statErr == nil {
		http.Error(w, "COPY destination overwrite is unsupported", http.StatusPreconditionFailed)
		return
	}
	if statusFor(statErr) != http.StatusNotFound {
		http.Error(w, statErr.Error(), statusFor(statErr))
		return
	}
	var read controlrpc.BeginReadResult
	if err := h.client.Call(r.Context(), controlrpc.MethodBeginRead, controlrpc.BeginReadParams{Path: src, ResourceSchemes: []string{"file"}}, &read); err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	sourcePath, err := localPath(read.ResourceURI)
	if err != nil {
		http.Error(w, errors.Join(err, h.client.Call(context.Background(), controlrpc.MethodRelease, map[string]string{"session_id": read.SessionID}, nil)).Error(), http.StatusInternalServerError)
		return
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		http.Error(w, errors.Join(err, h.client.Call(context.Background(), controlrpc.MethodRelease, map[string]string{"session_id": read.SessionID}, nil)).Error(), http.StatusInternalServerError)
		return
	}
	var write controlrpc.BeginWriteResult
	if err := h.client.Call(r.Context(), controlrpc.MethodBeginWrite, controlrpc.BeginWriteParams{Path: dst, ResourceSchemes: []string{"file"}}, &write); err != nil {
		http.Error(w, errors.Join(err, source.Close(), h.client.Call(context.Background(), controlrpc.MethodRelease, map[string]string{"session_id": read.SessionID}, nil)).Error(), preconditionStatus(err))
		return
	}
	stage, err := localPath(write.ResourceURI)
	if err != nil {
		http.Error(w, errors.Join(err, h.client.Call(context.Background(), controlrpc.MethodAbort, map[string]string{"session_id": write.SessionID}, nil), source.Close(), h.client.Call(context.Background(), controlrpc.MethodRelease, map[string]string{"session_id": read.SessionID}, nil)).Error(), http.StatusInternalServerError)
		return
	}
	dest, err := os.OpenFile(stage, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		http.Error(w, errors.Join(err, h.client.Call(context.Background(), controlrpc.MethodRelease, map[string]string{"session_id": write.SessionID}, nil), source.Close(), h.client.Call(context.Background(), controlrpc.MethodRelease, map[string]string{"session_id": read.SessionID}, nil)).Error(), http.StatusInternalServerError)
		return
	}
	_, copyErr := copyWithRenew(r.Context(), h.client, write.SessionID, dest, source)
	syncErr := dest.Sync()
	closeErr := dest.Close()
	sourceCloseErr := source.Close()
	readReleaseErr := h.client.Call(context.Background(), controlrpc.MethodRelease, map[string]string{"session_id": read.SessionID}, nil)
	if err := errors.Join(copyErr, syncErr, closeErr, sourceCloseErr, readReleaseErr); err != nil {
		http.Error(w, errors.Join(err, h.client.Call(context.Background(), controlrpc.MethodRelease, map[string]string{"session_id": write.SessionID}, nil)).Error(), http.StatusInternalServerError)
		return
	}
	if err := h.client.Call(r.Context(), controlrpc.MethodCommit, map[string]string{"session_id": write.SessionID}, nil); err != nil {
		http.Error(w, err.Error(), preconditionStatus(err))
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func preconditionsMatch(r *http.Request, begin controlrpc.BeginWriteResult) bool {
	current := ""
	if begin.ExpectedEntryID != "" {
		current = fmt.Sprintf("\"%s-%d\"", begin.ExpectedEntryID, begin.ExpectedRevision)
	}
	if values := r.Header.Values("If-Match"); len(values) > 0 {
		matched := false
		for _, value := range values {
			for _, tag := range strings.Split(value, ",") {
				tag = strings.TrimSpace(tag)
				if tag == "*" && current != "" {
					matched = true
				}
				if tag == current && current != "" && !strings.HasPrefix(tag, "W/") {
					matched = true
				}
			}
		}
		if !matched {
			return false
		}
	}
	if values := r.Header.Values("If-None-Match"); len(values) > 0 {
		for _, value := range values {
			for _, tag := range strings.Split(value, ",") {
				tag = strings.TrimSpace(tag)
				if tag == "*" && current != "" {
					return false
				}
				if weakTag(tag) == current && current != "" {
					return false
				}
			}
		}
	}
	if date := r.Header.Get("If-Unmodified-Since"); date != "" && len(r.Header.Values("If-Match")) == 0 && current != "" && begin.ExpectedMtime > 0 {
		if parsed, err := http.ParseTime(date); err == nil && time.Unix(0, begin.ExpectedMtime).Truncate(time.Second).After(parsed) {
			return false
		}
	}
	return true
}
func weakTag(tag string) string      { return strings.TrimPrefix(tag, "W/") }
func etag(e controlrpc.Entry) string { return fmt.Sprintf("\"%s-%d\"", e.ID, e.Revision) }
func statusFor(err error) int {
	var remote *controlrpc.RemoteError
	if errors.As(err, &remote) {
		switch remote.Code {
		case controlrpc.CodeEntryNotFound:
			return http.StatusNotFound
		case controlrpc.CodeEntryExists:
			return http.StatusMethodNotAllowed
		case controlrpc.CodeEntryConflict:
			return http.StatusPreconditionFailed
		case controlrpc.CodePermissionDenied:
			return http.StatusForbidden
		case controlrpc.CodeInvalidParams:
			return http.StatusBadRequest
		case controlrpc.CodeDirectoryNotEmpty:
			return http.StatusConflict
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		return http.StatusNotFound
	}
	if errors.Is(err, os.ErrExist) {
		return http.StatusMethodNotAllowed
	}
	if errors.Is(err, syscall.EAGAIN) {
		return http.StatusPreconditionFailed
	}
	return http.StatusInternalServerError
}

func preconditionStatus(err error) int {
	var remote *controlrpc.RemoteError
	if errors.As(err, &remote) && (remote.Code == controlrpc.CodeEntryExists || remote.Code == controlrpc.CodeEntryConflict) {
		return http.StatusPreconditionFailed
	}
	return statusFor(err)
}
func copyWithRenew(ctx context.Context, client rpcClient, id string, dst io.Writer, src io.Reader) (int64, error) {
	ticker := time.NewTicker(controlrpc.ResourceSessionRenewInterval)
	defer ticker.Stop()
	type result struct {
		n   int64
		err error
	}
	done := make(chan result, 1)
	go func() { n, err := io.Copy(dst, src); done <- result{n, err} }()
	var renewErr error
	for {
		select {
		case res := <-done:
			return res.n, errors.Join(res.err, renewErr)
		case <-ctx.Done():
			if closer, ok := src.(io.Closer); ok {
				closeErr := closer.Close()
				res := <-done
				return res.n, errors.Join(ctx.Err(), res.err, closeErr)
			}
			res := <-done
			return res.n, errors.Join(ctx.Err(), res.err)
		case <-ticker.C:
			var renew controlrpc.RenewResult
			if err := client.Call(ctx, controlrpc.MethodRenew, map[string]string{"session_id": id}, &renew); err != nil {
				renewErr = err
				if closer, ok := src.(io.Closer); ok {
					closeErr := closer.Close()
					res := <-done
					return res.n, errors.Join(renewErr, res.err, closeErr)
				}
				res := <-done
				return res.n, errors.Join(renewErr, res.err)
			}
		}
	}
}
