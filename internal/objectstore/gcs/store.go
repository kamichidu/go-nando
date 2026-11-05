package gcs

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/objectstore"
	"google.golang.org/api/iterator"
)

var (
	ErrRemoteObjectConflict = errors.New("GCS object does not match requested content ID")
	ErrObjectNotFound       = errors.New("GCS object not found")
)

// apiAttrs are the object attributes returned by the GCS JSON API.
type apiAttrs struct {
	Name       string
	ID         content.ID
	Size       int64
	Generation int64
	ContentID  string
	MD5        []byte
}

type gcsAPI interface {
	Upload(context.Context, string, content.ID, int64, []byte, io.Reader) (apiAttrs, error)
	Attrs(context.Context, string) (apiAttrs, error)
	Open(context.Context, string) (io.ReadCloser, error)
	List(context.Context, string) ([]apiAttrs, error)
}

// Store implements remote immutable object operations for one GCS bucket.
type Store struct {
	bucket string
	prefix string
	api    gcsAPI
}

var (
	_ objectstore.RemoteObjectStore = (*Store)(nil)
)

// NewStore constructs a GCS object store around an authenticated Cloud Storage client.
func NewStore(remoteURI string, client *storage.Client) (*Store, error) {
	bucket, prefix, err := ParseRemoteURI(remoteURI)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("GCS client is required")
	}
	return &Store{bucket: bucket, prefix: prefix, api: &storageAPI{bucket: client.Bucket(bucket), prefix: prefix}}, nil
}

// NewClientStore constructs a GCS store using Application Default Credentials.
func NewClientStore(ctx context.Context, remoteURI string) (*Store, *storage.Client, error) {
	if _, _, err := ParseRemoteURI(remoteURI); err != nil {
		return nil, nil, err
	}
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("create GCS client: %w", err)
	}
	store, err := NewStore(remoteURI, client)
	if err != nil {
		return nil, nil, errors.Join(err, client.Close())
	}
	return store, client, nil
}

// NewStoreWithAPI supports deterministic tests without contacting Google Cloud.
func NewStoreWithAPI(remoteURI string, api gcsAPI) (*Store, error) {
	bucket, prefix, err := ParseRemoteURI(remoteURI)
	if err != nil {
		return nil, err
	}
	return NewStoreWithAPIAndPrefix(bucket, prefix, api), nil
}

func NewStoreWithAPIAndPrefix(bucket, prefix string, api gcsAPI) *Store {
	return &Store{bucket: bucket, prefix: prefix, api: api}
}

// ParseRemoteURI validates gs://bucket[/prefix] and preserves every prefix byte.
// It deliberately avoids URL parsing so escaped bytes and trailing slashes are
// never decoded or normalized.
func ParseRemoteURI(remoteURI string) (bucket, prefix string, err error) {
	const scheme = "gs://"
	if !strings.HasPrefix(remoteURI, scheme) {
		return "", "", fmt.Errorf("remote URI must use gs://")
	}
	rest := remoteURI[len(scheme):]
	if rest == "" || strings.ContainsAny(rest, "?#\\") || strings.ContainsAny(remoteURI, "\x00\r\n") {
		return "", "", fmt.Errorf("malformed GCS remote URI")
	}
	bucket, prefix, hasPrefix := strings.Cut(rest, "/")
	if bucket == "" || bucket == "." || bucket == ".." || strings.Contains(bucket, "%") || strings.Contains(bucket, ":") || strings.Contains(bucket, "@") {
		return "", "", fmt.Errorf("malformed GCS bucket in remote URI")
	}
	for _, r := range bucket {
		valid := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_'
		if !valid {
			return "", "", fmt.Errorf("malformed GCS bucket in remote URI")
		}
	}
	if !hasPrefix {
		prefix = ""
	}
	for _, segment := range strings.Split(prefix, "/") {
		if segment == "." || segment == ".." {
			return "", "", fmt.Errorf("malformed GCS object prefix in remote URI")
		}
	}
	return bucket, prefix, nil
}

func (s *Store) objectName(id content.ID) string { return s.prefix + "objects/" + id.String() }

// Upload writes directly to the final object name with a generation-match-zero
// precondition. If the response is ambiguous or the name already exists, API
// attributes are queried and accepted only when they identify this content ID
// and size. No object bytes are read back from GCS.
func (s *Store) Upload(ctx context.Context, id content.ID, size int64, expectedMD5 []byte, src io.Reader) (objectstore.RemoteObjectInfo, error) {
	if err := id.Validate(); err != nil {
		return objectstore.RemoteObjectInfo{}, err
	}
	if len(expectedMD5) != md5.Size {
		return objectstore.RemoteObjectInfo{}, fmt.Errorf("upload requires a precomputed MD5")
	}
	name := s.objectName(id)
	attrs, attrsErr := s.api.Attrs(ctx, name)
	if attrsErr == nil {
		if matches(attrs, id, size, expectedMD5) {
			return toRemoteObjectInfo(attrs), nil
		}
		return objectstore.RemoteObjectInfo{}, fmt.Errorf("%w: final name %q has incompatible API attributes", ErrRemoteObjectConflict, name)
	}
	if !errors.Is(attrsErr, ErrObjectNotFound) {
		return objectstore.RemoteObjectInfo{}, fmt.Errorf("inspect final GCS object %q: %w", name, attrsErr)
	}
	attrs, uploadErr := s.api.Upload(ctx, name, id, size, expectedMD5, src)
	if uploadErr == nil && matches(attrs, id, size, expectedMD5) {
		return toRemoteObjectInfo(attrs), nil
	}
	if uploadErr == nil {
		uploadErr = ErrRemoteObjectConflict
	}
	attrs, attrsErr = s.api.Attrs(ctx, name)
	if attrsErr == nil && matches(attrs, id, size, expectedMD5) {
		return toRemoteObjectInfo(attrs), nil
	}
	if attrsErr == nil {
		return objectstore.RemoteObjectInfo{}, fmt.Errorf("%w: final name %q has incompatible API attributes", ErrRemoteObjectConflict, name)
	}
	return objectstore.RemoteObjectInfo{}, errors.Join(fmt.Errorf("resumable GCS upload %q: %w", name, uploadErr), attrsErr)
}

func matches(attrs apiAttrs, id content.ID, size int64, localMD5 []byte) bool {
	return attrs.ID == id && attrs.ContentID == id.String() && attrs.Size == size && attrs.Generation > 0 && len(attrs.MD5) == md5.Size && bytes.Equal(attrs.MD5, localMD5)
}

func toRemoteObjectInfo(attrs apiAttrs) objectstore.RemoteObjectInfo {
	return objectstore.RemoteObjectInfo{ID: attrs.ID, ContentID: attrs.ContentID, Size: attrs.Size, Generation: attrs.Generation, MD5: append([]byte(nil), attrs.MD5...)}
}

func (s *Store) RemoteAttrs(ctx context.Context, id content.ID) (objectstore.RemoteObjectInfo, error) {
	if err := id.Validate(); err != nil {
		return objectstore.RemoteObjectInfo{}, err
	}
	attrs, err := s.api.Attrs(ctx, s.objectName(id))
	if err != nil {
		return objectstore.RemoteObjectInfo{}, err
	}
	if attrs.ID != id || attrs.ContentID != id.String() || attrs.Generation <= 0 {
		return objectstore.RemoteObjectInfo{}, ErrRemoteObjectConflict
	}
	return toRemoteObjectInfo(attrs), nil
}

func (s *Store) OpenRemote(ctx context.Context, id content.ID) (io.ReadCloser, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	r, err := s.api.Open(ctx, s.objectName(id))
	if errors.Is(err, ErrObjectNotFound) {
		return nil, objectstore.ErrObjectNotFound
	}
	return r, err
}

func (s *Store) RemoteExists(ctx context.Context, id content.ID) (bool, error) {
	if err := id.Validate(); err != nil {
		return false, err
	}
	attrs, err := s.api.Attrs(ctx, s.objectName(id))
	if errors.Is(err, ErrObjectNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return attrs.ID == id && attrs.ContentID == id.String(), nil
}

func (s *Store) ListRemoteObjects(ctx context.Context) ([]objectstore.RemoteObjectInfo, error) {
	attrs, err := s.api.List(ctx, s.prefix+"objects/")
	if err != nil {
		return nil, err
	}
	result := make([]objectstore.RemoteObjectInfo, 0, len(attrs))
	var failures []error
	for _, item := range attrs {
		keyPrefix := s.prefix + "objects/"
		if !strings.HasPrefix(item.Name, keyPrefix+"sha256-") {
			continue
		}
		nameID, nameErr := content.ParseID(strings.TrimPrefix(item.Name, keyPrefix))
		if err := item.ID.Validate(); err != nil || nameErr != nil || nameID != item.ID || item.ContentID == "" || item.ContentID != item.ID.String() || item.Name != keyPrefix+item.ContentID || item.Generation <= 0 {
			failures = append(failures, fmt.Errorf("GCS object %q has missing or invalid Content ID metadata or generation", item.Name))
			continue
		}
		result = append(result, toRemoteObjectInfo(item))
	}
	return result, errors.Join(failures...)
}

type storageAPI struct {
	bucket *storage.BucketHandle
	prefix string
}

func (a *storageAPI) Upload(ctx context.Context, name string, id content.ID, size int64, expectedMD5 []byte, src io.Reader) (apiAttrs, error) {
	localMD5 := md5.New()
	tee := io.TeeReader(src, localMD5)
	obj := a.bucket.Object(name).If(storage.Conditions{DoesNotExist: true})
	uploadCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := obj.NewWriter(uploadCtx)
	w.ChunkSize = 256 << 10
	w.Metadata = map[string]string{"nando-content-id": id.String()}
	if _, err := io.Copy(w, tee); err != nil {
		cancel()
		return apiAttrs{}, err
	}
	if err := w.Close(); err != nil {
		return apiAttrs{}, err
	}
	attrs := w.Attrs()
	if attrs == nil {
		return a.Attrs(ctx, name)
	}
	result := convertAttrs(name, attrs)
	if len(result.MD5) != md5.Size || !bytes.Equal(result.MD5, expectedMD5) || !bytes.Equal(localMD5.Sum(nil), expectedMD5) {
		return apiAttrs{}, ErrRemoteObjectConflict
	}
	return result, nil
}

func (a *storageAPI) Attrs(ctx context.Context, name string) (apiAttrs, error) {
	attrs, err := a.bucket.Object(name).Attrs(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return apiAttrs{}, ErrObjectNotFound
	}
	if err != nil {
		return apiAttrs{}, err
	}
	return convertAttrs(name, attrs), nil
}

func convertAttrs(name string, attrs *storage.ObjectAttrs) apiAttrs {
	var id content.ID
	contentID := ""
	if attrs.Metadata != nil {
		contentID = attrs.Metadata["nando-content-id"]
		parsed, err := content.ParseID(contentID)
		if err == nil {
			id = parsed
		}
	}
	return apiAttrs{Name: attrs.Name, ID: id, Size: attrs.Size, Generation: attrs.Generation, ContentID: contentID, MD5: append([]byte(nil), attrs.MD5...)}
}

func (a *storageAPI) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	r, err := a.bucket.Object(name).NewReader(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil, ErrObjectNotFound
	}
	return r, err
}

func (a *storageAPI) List(ctx context.Context, wantPrefix string) ([]apiAttrs, error) {
	iter := a.bucket.Objects(ctx, &storage.Query{Prefix: wantPrefix})
	var result []apiAttrs
	for {
		attrs, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return result, nil
		}
		if err != nil {
			return nil, err
		}
		name := attrs.Name
		if !strings.HasPrefix(name, wantPrefix) || len(name) <= len(wantPrefix) {
			continue
		}
		result = append(result, convertAttrs(name, attrs))
	}
}
