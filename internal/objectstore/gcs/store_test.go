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
	attrs      apiAttrs
	attrsErr   error
	uploadErr  error
	uploadSize int64
	bytes      string
	deletes    int
	consume    int64
}

func (a *fakeAPI) Upload(_ context.Context, name string, id content.ID, size int64, _ []byte, src io.Reader) (apiAttrs, error) {
	a.name = name
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
		a.attrs = apiAttrs{ID: id, ContentID: id.String(), Size: int64(len(data)), Generation: 17, MD5: sum[:]}
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
	api.attrs = apiAttrs{ID: id, ContentID: id.String(), Size: 3, Generation: 22, MD5: want[:]}
	store := NewStoreWithAPI("bucket", api)
	got, err := store.Upload(context.Background(), id, 3, want[:], strings.NewReader("abc"))
	if err != nil || got.Generation != 22 {
		t.Fatalf("partial upload reconciliation failed: replica=%+v err=%v", got, err)
	}
}
func (a *fakeAPI) Attrs(_ context.Context, name string) (apiAttrs, error) {
	a.name = name
	return a.attrs, a.attrsErr
}
func (a *fakeAPI) Open(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(a.bytes)), nil
}
func (a *fakeAPI) List(context.Context) ([]apiAttrs, error)    { return []apiAttrs{a.attrs}, nil }
func (a *fakeAPI) Delete(context.Context, string, int64) error { a.deletes++; return nil }

func TestUploadTargetsFinalNameAndUsesAPIAttributesForAmbiguousResponse(t *testing.T) {
	id, err := content.ParseID("sha256-ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{uploadErr: errors.New("response lost")}
	sum := md5.Sum([]byte("abc"))
	api.attrs = apiAttrs{ID: id, ContentID: id.String(), Size: 3, Generation: 91, MD5: sum[:]}
	store := NewStoreWithAPI("bucket", api)
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
	api.attrs = apiAttrs{ID: id, ContentID: id.String(), Size: 4, Generation: 4, MD5: sum[:]}
	store := NewStoreWithAPI("bucket", api)
	expected := md5.Sum([]byte("abc"))
	if _, err := store.Upload(context.Background(), id, 3, expected[:], strings.NewReader("abc")); !errors.Is(err, ErrReplicaConflict) {
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
	api.attrs = apiAttrs{ID: id, ContentID: id.String(), Size: 3, Generation: 1, MD5: wrong[:]}
	store := NewStoreWithAPI("bucket", api)
	sum := md5.Sum([]byte("abc"))
	if _, err := store.Upload(context.Background(), id, 3, sum[:], strings.NewReader("abc")); !errors.Is(err, ErrReplicaConflict) {
		t.Fatalf("MD5 mismatch accepted: %v", err)
	}
}
