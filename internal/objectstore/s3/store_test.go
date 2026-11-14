package s3

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsS3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/kamichidu/go-nando/internal/content"
)

func TestUploadConvergesAfterPersistedObjectLosesAcknowledgement(t *testing.T) {
	ctx := context.Background()
	payload := []byte("persisted before the response was lost")
	id, err := content.NewHasher().Sum(ctx, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	md5sum := md5.Sum(payload)
	api := &testAPI{putErr: errors.New("connection reset after server persisted object")}
	store, err := NewStoreWithAPI("s3://bucket/literal/", api)
	if err != nil {
		t.Fatal(err)
	}
	info, err := store.Upload(ctx, id, int64(len(payload)), md5sum[:], bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("ambiguous upload did not converge through HEAD: %v", err)
	}
	if info.ID != id || info.ContentID != id.String() || info.Size != int64(len(payload)) || !bytes.Equal(info.MD5, md5sum[:]) {
		t.Fatalf("reconciled info=%+v", info)
	}
	if api.putCalls != 1 || api.ifNoneMatch != "*" || api.putContentMD5 != base64.StdEncoding.EncodeToString(md5sum[:]) || api.putChecksumSHA256 != base64.StdEncoding.EncodeToString(id.Digest()) {
		t.Fatalf("PutObject request not conditional/checksummed: %+v", api)
	}
}

func TestUploadRejectsMismatchedExistingKeyWithoutPut(t *testing.T) {
	ctx := context.Background()
	payload := []byte("wanted")
	id, err := content.NewHasher().Sum(ctx, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	md5sum := md5.Sum(payload)
	api := &testAPI{present: true, data: []byte("other"), metadata: map[string]string{
		"nando-content-id": "sha256-incorrect", "nando-md5": base64.StdEncoding.EncodeToString(md5sum[:]),
	}}
	store, err := NewStoreWithAPI("s3://bucket/", api)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Upload(ctx, id, int64(len(payload)), md5sum[:], bytes.NewReader(payload)); !errors.Is(err, ErrObjectConflict) {
		t.Fatalf("mismatched existing object error=%v, want ErrObjectConflict", err)
	}
	if api.putCalls != 0 {
		t.Fatalf("mismatched existing key was overwritten with %d PUT calls", api.putCalls)
	}
}

type testAPI struct {
	putErr            error
	putCalls          int
	present           bool
	data              []byte
	metadata          map[string]string
	ifNoneMatch       string
	putContentMD5     string
	putChecksumSHA256 string
}

func (a *testAPI) PutObject(_ context.Context, input *awsS3.PutObjectInput, _ ...func(*awsS3.Options)) (*awsS3.PutObjectOutput, error) {
	a.putCalls++
	a.ifNoneMatch = aws.ToString(input.IfNoneMatch)
	a.putContentMD5 = aws.ToString(input.ContentMD5)
	a.putChecksumSHA256 = aws.ToString(input.ChecksumSHA256)
	a.metadata = input.Metadata
	data, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	a.data = data
	a.present = true
	return &awsS3.PutObjectOutput{}, a.putErr
}

func (a *testAPI) HeadObject(_ context.Context, _ *awsS3.HeadObjectInput, _ ...func(*awsS3.Options)) (*awsS3.HeadObjectOutput, error) {
	if !a.present {
		return nil, apiError("NoSuchKey")
	}
	length := int64(len(a.data))
	checksum := sha256.Sum256(a.data)
	return &awsS3.HeadObjectOutput{
		ContentLength: &length, ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(checksum[:])),
		Metadata: a.metadata,
	}, nil
}

func (*testAPI) GetObject(context.Context, *awsS3.GetObjectInput, ...func(*awsS3.Options)) (*awsS3.GetObjectOutput, error) {
	return nil, errors.New("unexpected GetObject")
}

func (*testAPI) ListObjectsV2(context.Context, *awsS3.ListObjectsV2Input, ...func(*awsS3.Options)) (*awsS3.ListObjectsV2Output, error) {
	return &awsS3.ListObjectsV2Output{}, nil
}

type apiError string

func (e apiError) Error() string               { return string(e) }
func (e apiError) ErrorCode() string           { return string(e) }
func (apiError) ErrorMessage() string          { return "test API error" }
func (apiError) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }
