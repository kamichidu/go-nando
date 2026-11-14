package s3

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsS3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/kamichidu/go-nando/internal/content"
)

func TestParseRemoteURIPreservesLiteralPrefix(t *testing.T) {
	tests := []struct {
		uri, bucket, prefix string
	}{
		{"s3://archive", "archive", ""},
		{"s3://archive/hoge", "archive", "hoge"},
		{"s3://archive/hoge/", "archive", "hoge/"},
	}
	for _, test := range tests {
		bucket, prefix, err := ParseRemoteURI(test.uri)
		if err != nil || bucket != test.bucket || prefix != test.prefix {
			t.Errorf("ParseRemoteURI(%q)=(%q,%q,%v), want (%q,%q)", test.uri, bucket, prefix, err, test.bucket, test.prefix)
		}
	}
	for _, uri := range []string{"gs://archive", "s3://", "s3://archive/../escape", "s3://archive?query"} {
		if _, _, err := ParseRemoteURI(uri); err == nil {
			t.Errorf("ParseRemoteURI(%q) unexpectedly succeeded", uri)
		}
	}
}

func TestS3CompatibleSinglePutAndReconciliation(t *testing.T) {
	if os.Getenv("NANDO_S3_INTEGRATION") != "1" {
		t.Skip("set NANDO_S3_INTEGRATION=1 to run against the project-scoped RustFS service")
	}
	ctx := context.Background()
	store, _, err := NewClientStore(ctx, "s3://"+os.Getenv("NANDO_S3_BUCKET")+"/integration/", ClientConfig{
		Endpoint: os.Getenv("NANDO_S3_ENDPOINT"), Region: os.Getenv("NANDO_S3_REGION"),
		AccessKeyID: os.Getenv("NANDO_S3_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("NANDO_S3_SECRET_ACCESS_KEY"),
	})
	if err != nil {
		t.Fatal(err)
	}
	creator, ok := store.client.(interface {
		CreateBucket(context.Context, *awsS3.CreateBucketInput, ...func(*awsS3.Options)) (*awsS3.CreateBucketOutput, error)
	})
	if !ok {
		t.Fatal("S3 client does not support bucket creation in integration setup")
	}
	if _, err := creator.CreateBucket(ctx, &awsS3.CreateBucketInput{Bucket: aws.String(store.bucket)}); err != nil {
		t.Fatalf("create isolated test bucket: %v", err)
	}
	data := []byte("RustFS validates single-PUT checksum and immutable publication\x00\xff")
	id, err := content.NewHasher().Sum(ctx, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	md5sum := md5.Sum(data)
	info, err := store.Upload(ctx, id, int64(len(data)), md5sum[:], bytes.NewReader(data))
	if err != nil {
		t.Fatalf("single conditional PutObject: %v", err)
	}
	if info.ID != id || info.ContentID != id.String() || info.Size != int64(len(data)) || !bytes.Equal(info.MD5, md5sum[:]) {
		t.Fatalf("upload metadata mismatch: %+v", info)
	}
	wrongBody := []byte("overwrite attempt")
	wrongMD5 := md5.Sum(wrongBody)
	wrongSHA256 := sha256.Sum256(wrongBody)
	_, err = store.client.PutObject(ctx, &awsS3.PutObjectInput{
		Bucket: aws.String(store.bucket), Key: aws.String(store.key(id)), Body: bytes.NewReader(wrongBody),
		ContentLength: aws.Int64(int64(len(wrongBody))), ContentMD5: aws.String(base64.StdEncoding.EncodeToString(wrongMD5[:])),
		ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(wrongSHA256[:])), IfNoneMatch: aws.String("*"),
		Metadata: map[string]string{"nando-content-id": id.String(), "nando-md5": base64.StdEncoding.EncodeToString(wrongMD5[:])},
	})
	if err == nil {
		t.Fatal("S3-compatible endpoint accepted a conditional overwrite")
	}
	attrsAfterConditionalWrite, err := store.RemoteAttrs(ctx, id)
	if err != nil || attrsAfterConditionalWrite.Size != int64(len(data)) || !bytes.Equal(attrsAfterConditionalWrite.MD5, md5sum[:]) {
		t.Fatalf("conditional write changed existing object: attrs=%+v err=%v", attrsAfterConditionalWrite, err)
	}
	_, err = store.Upload(ctx, id, int64(len(data)), md5sum[:], errorReader{})
	if err != nil {
		t.Fatalf("existing identical object was not reconciled by HEAD: %v", err)
	}
	attrs, err := store.RemoteAttrs(ctx, id)
	if err != nil || attrs.Size != int64(len(data)) || !bytes.Equal(attrs.MD5, md5sum[:]) {
		t.Fatalf("HEAD attributes=%+v err=%v", attrs, err)
	}
	opened, err := store.OpenRemote(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(opened)
	closeErr := opened.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(got, data) {
		t.Fatalf("remote read got=%x readErr=%v closeErr=%v", got, readErr, closeErr)
	}
	listed, err := store.ListRemoteObjects(ctx)
	if err != nil {
		t.Fatalf("list objects through LIST plus metadata HEAD: %v", err)
	}
	found := false
	for _, item := range listed {
		if item.ID == id && item.ContentID == id.String() && item.Size == int64(len(data)) && bytes.Equal(item.MD5, md5sum[:]) {
			found = true
		}
	}
	if !found {
		t.Fatalf("uploaded content missing from metadata listing: %+v", listed)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("payload must not be read during HEAD reconciliation")
}
