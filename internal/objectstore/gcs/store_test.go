package gcs

import (
	"context"
	"crypto/md5"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/kamichidu/go-nando/internal/content"
)

type fakeAPI struct {
	name       string
	names      []string
	attrs      apiAttrs
	attrsErr   error
	uploadErr  error
	uploadSize int64
	bytes      string
	consume    int64
}

func (a *fakeAPI) Upload(_ context.Context, name string, id content.ID, size int64, _ []byte, src io.Reader) (apiAttrs, error) {
	a.name = name
	a.names = append(a.names, name)
	if a.uploadErr != nil && a.consume > 0 {
		_, err := io.CopyN(io.Discard, src, a.consume)
		if err != nil && !errors.Is(err, io.EOF) {
			return apiAttrs{}, err
		}
		return apiAttrs{}, a.uploadErr
	}
	data, err := io.ReadAll(src)
	if err != nil {
		return apiAttrs{}, err
	}
	a.bytes, a.uploadSize = string(data), size
	if a.uploadErr == nil {
		sum := md5.Sum(data)
		a.attrs = apiAttrs{Name: name, ID: id, ContentID: id.String(), Size: int64(len(data)), Generation: 17, MD5: sum[:]}
	}
	return a.attrs, a.uploadErr
}

func TestAmbiguousUploadReconciliationDoesNotDependOnConsumedBytes(t *testing.T) {
	id, err := content.ParseID("sha256-ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	if err != nil {
		t.Fatal(err)
	}
	want := md5.Sum([]byte("abc"))
	api := &fakeAPI{uploadErr: errors.New("response lost"), consume: 1}
	api.attrs = apiAttrs{Name: "objects/" + id.String(), ID: id, ContentID: id.String(), Size: 3, Generation: 22, MD5: want[:]}
	store, err := NewStoreWithAPI("gs://bucket", api)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Upload(context.Background(), id, 3, want[:], strings.NewReader("abc"))
	if err != nil || got.Generation != 22 {
		t.Fatalf("partial upload reconciliation failed: replica=%+v err=%v", got, err)
	}
}
func (a *fakeAPI) Attrs(_ context.Context, name string) (apiAttrs, error) {
	a.name = name
	a.names = append(a.names, name)
	return a.attrs, a.attrsErr
}
func (a *fakeAPI) Open(_ context.Context, name string) (io.ReadCloser, error) {
	a.name = name
	a.names = append(a.names, name)
	return io.NopCloser(strings.NewReader(a.bytes)), nil
}
func (a *fakeAPI) List(_ context.Context, prefix string) ([]apiAttrs, error) {
	if a.attrs.Name != "" && strings.HasPrefix(a.attrs.Name, prefix) {
		return []apiAttrs{a.attrs}, nil
	}
	return nil, nil
}

func (a *fakeAPI) lastName() string {
	if len(a.names) > 0 {
		return a.names[len(a.names)-1]
	}
	return a.name
}

func TestUploadTargetsFinalNameAndUsesAPIAttributesForAmbiguousResponse(t *testing.T) {
	id, err := content.ParseID("sha256-ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{uploadErr: errors.New("response lost")}
	sum := md5.Sum([]byte("abc"))
	api.attrs = apiAttrs{Name: "objects/" + id.String(), ID: id, ContentID: id.String(), Size: 3, Generation: 91, MD5: sum[:]}
	store, err := NewStoreWithAPI("gs://bucket", api)
	if err != nil {
		t.Fatal(err)
	}
	info, err := store.Upload(context.Background(), id, 3, sum[:], strings.NewReader("abc"))
	if err != nil || info.ID != id || info.Size != 3 || info.Generation != 91 {
		t.Fatalf("ambiguous upload was not reconciled: info=%+v err=%v", info, err)
	}
	if api.name != "objects/"+id.String() {
		t.Fatalf("upload did not use final object name: %q", api.name)
	}
}

func TestUploadRejectsExistingObjectWithIncompatibleAttributes(t *testing.T) {
	id, err := content.ParseID("sha256-ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{uploadErr: errors.New("precondition failed")}
	sum := md5.Sum([]byte("abcd"))
	api.attrs = apiAttrs{Name: "objects/" + id.String(), ID: id, ContentID: id.String(), Size: 4, Generation: 4, MD5: sum[:]}
	store, err := NewStoreWithAPI("gs://bucket", api)
	if err != nil {
		t.Fatal(err)
	}
	expected := md5.Sum([]byte("abc"))
	if _, err := store.Upload(context.Background(), id, 3, expected[:], strings.NewReader("abc")); !errors.Is(err, ErrRemoteObjectConflict) {
		t.Fatalf("incompatible existing object was accepted: %v", err)
	}
}

func TestUploadRejectsMismatchedMD5(t *testing.T) {
	id, err := content.ParseID("sha256-ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{uploadErr: errors.New("ambiguous response")}
	wrong := md5.Sum([]byte("different"))
	api.attrs = apiAttrs{Name: "objects/" + id.String(), ID: id, ContentID: id.String(), Size: 3, Generation: 1, MD5: wrong[:]}
	store, err := NewStoreWithAPI("gs://bucket", api)
	if err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum([]byte("abc"))
	if _, err := store.Upload(context.Background(), id, 3, sum[:], strings.NewReader("abc")); !errors.Is(err, ErrRemoteObjectConflict) {
		t.Fatalf("MD5 mismatch accepted: %v", err)
	}
}

func TestParseRemoteURIPreservesRawPrefix(t *testing.T) {
	tests := []struct{ uri, bucket, prefix string }{
		{"gs://bucket", "bucket", ""},
		{"gs://bucket/hoge", "bucket", "hoge"},
		{"gs://bucket/hoge/", "bucket", "hoge/"},
		{"gs://bucket/%2Fraw", "bucket", "%2Fraw"},
	}
	for _, test := range tests {
		bucket, prefix, err := ParseRemoteURI(test.uri)
		if err != nil || bucket != test.bucket || prefix != test.prefix {
			t.Errorf("ParseRemoteURI(%q)=(%q,%q,%v), want (%q,%q)", test.uri, bucket, prefix, err, test.bucket, test.prefix)
		}
	}
}

func TestParseRemoteURIRejectsMalformedInputs(t *testing.T) {
	for _, uri := range []string{"", "bucket", "GS://bucket", "gs:///prefix", "gs://bucket/../x", "gs://bucket?query", "gs://bucket#fragment", "gs://bad bucket", "gs://bucket\\other"} {
		if _, _, err := ParseRemoteURI(uri); err == nil {
			t.Errorf("ParseRemoteURI(%q) unexpectedly succeeded", uri)
		}
	}
}

func TestPrefixKeySemanticsApplyToUploadAndReadOperations(t *testing.T) {
	id, err := content.ParseID("sha256-ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ uri, want string }{
		{"gs://bucket", "objects/" + id.String()},
		{"gs://bucket/hoge", "hogeobjects/" + id.String()},
		{"gs://bucket/hoge/", "hoge/objects/" + id.String()},
	} {
		sum := md5.Sum([]byte("abc"))
		api := &fakeAPI{attrs: apiAttrs{Name: test.want, ID: id, ContentID: id.String(), Size: 3, Generation: 17, MD5: sum[:]}}
		store, err := NewStoreWithAPI(test.uri, api)
		if err != nil {
			t.Fatal(err)
		}
		md5sum := md5.Sum([]byte("abc"))
		if _, err := store.Upload(context.Background(), id, 3, md5sum[:], strings.NewReader("abc")); err != nil {
			t.Fatal(err)
		}
		if api.lastName() != test.want {
			t.Fatalf("Upload key=%q want %q", api.lastName(), test.want)
		}
		api.names = nil
		if _, err := store.RemoteAttrs(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		if api.lastName() != test.want {
			t.Fatalf("Attrs key=%q want %q", api.lastName(), test.want)
		}
		api.names = nil
		if _, err := store.OpenRemote(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		if api.lastName() != test.want {
			t.Fatalf("Open key=%q want %q", api.lastName(), test.want)
		}
		api.names = nil
		api.name = ""
		if _, err := store.RemoteExists(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		if api.lastName() != test.want {
			t.Fatalf("Exists key=%q want %q", api.lastName(), test.want)
		}
	}
}

func TestListRemoteObjectsEnforcesExactConfiguredNamespace(t *testing.T) {
	id, err := content.ParseID("sha256-ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeListAPI{fakeAPI: fakeAPI{attrs: apiAttrs{ID: id, ContentID: id.String(), Generation: 1}}, listed: []apiAttrs{
		{Name: "hoge/objects/" + id.String(), ID: id, ContentID: id.String(), Generation: 1},
		{Name: "hoge/objects/" + id.String() + "suffix", ID: id, ContentID: id.String(), Generation: 1},
	}}
	store, err := NewStoreWithAPI("gs://bucket/hoge/", api)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.ListRemoteObjects(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("ListRemoteObjects=%+v err=%v", got, err)
	}
}

type fakeListAPI struct {
	fakeAPI
	listed []apiAttrs
}

func (a *fakeListAPI) List(context.Context, string) ([]apiAttrs, error) { return a.listed, nil }
