package repository_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awsS3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/kamichidu/go-nando/internal/content"
	coordinatorsqlite "github.com/kamichidu/go-nando/internal/coordinator/sqlite"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/objectstore"
	localstore "github.com/kamichidu/go-nando/internal/objectstore/local"
	s3store "github.com/kamichidu/go-nando/internal/objectstore/s3"
	"github.com/kamichidu/go-nando/internal/repository"
)

func TestS3RepositoryDeliveryAndVerifiedReadThrough(t *testing.T) {
	if os.Getenv("NANDO_S3_INTEGRATION") != "1" {
		t.Skip("set NANDO_S3_INTEGRATION=1 to run against the project-scoped RustFS service")
	}
	ctx := context.Background()
	bucket := "nando-repository-e2e"
	remoteURI := "s3://" + bucket + "/repository-e2e/"
	remote, _, err := s3store.NewClientStore(ctx, remoteURI, s3store.ClientConfig{
		Endpoint: os.Getenv("NANDO_S3_ENDPOINT"), Region: os.Getenv("NANDO_S3_REGION"),
		AccessKeyID: os.Getenv("NANDO_S3_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("NANDO_S3_SECRET_ACCESS_KEY"),
	})
	if err != nil {
		t.Fatal(err)
	}
	awsConfig, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(os.Getenv("NANDO_S3_REGION")),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(os.Getenv("NANDO_S3_ACCESS_KEY_ID"), os.Getenv("NANDO_S3_SECRET_ACCESS_KEY"), "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	client := awsS3.NewFromConfig(awsConfig, func(options *awsS3.Options) {
		options.BaseEndpoint = aws.String(os.Getenv("NANDO_S3_ENDPOINT"))
		options.UsePathStyle = true
	})
	if _, err := client.CreateBucket(ctx, &awsS3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("create isolated repository test bucket: %v", err)
	}

	root := t.TempDir()
	hasher := content.NewHasher()
	objects, err := localstore.NewStore(filepath.Join(root, "objects"), filepath.Join(root, "temp"), hasher)
	if err != nil {
		t.Fatal(err)
	}
	metaStore := openInitializedMetadataStore(t, filepath.Join(root, "metadata.sqlite"), ctx)
	coordinator, err := coordinatorsqlite.Open(filepath.Join(root, "coordination.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := coordinator.Close(); err != nil {
			t.Errorf("close coordinator: %v", err)
		}
	})
	repo := repository.NewRepositoryWithRemoteObjectStore(
		metadata.NewValidatingStore(metaStore), objects, remote, hasher, coordinator,
		repository.Settings{DeliveryMode: repository.DeliveryModeRemote, RemoteBackend: "s3", RemoteURI: remoteURI},
	)

	payload := []byte("repository-to-S3 delivery and SHA-256 read-through\x00\xff")
	source := filepath.Join(root, "source.bin")
	if err := os.WriteFile(source, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := repo.Put(ctx, source, "/payload.bin"); err != nil {
		t.Fatalf("commit local content and remote obligation: %v", err)
	}
	if err := repo.ProcessTransferQueue(ctx); err != nil {
		t.Fatalf("deliver queue item to S3: %v", err)
	}
	entry, err := repo.ResolvePath(ctx, "/payload.bin")
	if err != nil || entry.ObjectID == nil {
		t.Fatalf("resolve committed object: entry=%+v err=%v", entry, err)
	}
	if err := objects.Delete(ctx, *entry.ObjectID); err != nil {
		t.Fatalf("remove local copy to exercise read-through: %v", err)
	}
	destination := filepath.Join(root, "read-through.bin")
	if err := repo.Get(ctx, "/payload.bin", destination); err != nil {
		t.Fatalf("download, verify SHA-256, and publish Local object: %v", err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("read-through bytes=%x err=%v", got, err)
	}
	orphanPayload := []byte("metadata-only S3 orphan recovery")
	orphanID, err := hasher.Sum(ctx, bytes.NewReader(orphanPayload))
	if err != nil {
		t.Fatal(err)
	}
	orphanMD5 := md5.Sum(orphanPayload)
	if _, err := remote.Upload(ctx, orphanID, int64(len(orphanPayload)), orphanMD5[:], bytes.NewReader(orphanPayload)); err != nil {
		t.Fatalf("publish unreferenced S3 object for recovery: %v", err)
	}
	if recovered, err := repo.RestoreRemoteOrphansTo(ctx, "/recovered"); err != nil || recovered != 1 {
		t.Fatalf("metadata-only S3 orphan recovery count=%d err=%v", recovered, err)
	}
	recoveredEntry, err := repo.ResolvePath(ctx, "/recovered/"+orphanID.String())
	if err != nil || recoveredEntry.ObjectID == nil || *recoveredEntry.ObjectID != orphanID || recoveredEntry.Size != int64(len(orphanPayload)) || !bytes.Equal(recoveredEntry.MD5, orphanMD5[:]) {
		t.Fatalf("S3 orphan recovery entry=%+v err=%v", recoveredEntry, err)
	}

	// Simulate an external writer corrupting the immutable key. Read-through
	// must reject the payload by Content ID and keep it out of Local storage.
	corrupt := []byte("corrupt remote payload")
	corruptMD5 := md5.Sum(corrupt)
	corruptSHA := sha256.Sum256(corrupt)
	key := "repository-e2e/objects/" + entry.ObjectID.String()
	_, err = client.PutObject(ctx, &awsS3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(corrupt),
		ContentLength: aws.Int64(int64(len(corrupt))), ContentMD5: aws.String(base64.StdEncoding.EncodeToString(corruptMD5[:])),
		ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(corruptSHA[:])),
		Metadata:       map[string]string{"nando-content-id": entry.ObjectID.String(), "nando-md5": base64.StdEncoding.EncodeToString(corruptMD5[:])},
	})
	if err != nil {
		t.Fatalf("simulate external remote corruption: %v", err)
	}
	if err := objects.Delete(ctx, *entry.ObjectID); err != nil {
		t.Fatalf("remove local copy before corruption check: %v", err)
	}
	badDestination := filepath.Join(root, "corrupt-read-through.bin")
	if err := repo.Get(ctx, "/payload.bin", badDestination); !errors.Is(err, objectstore.ErrHashMismatch) {
		t.Fatalf("corrupt remote payload error=%v, want ErrHashMismatch", err)
	}
	if exists, err := objects.Exists(ctx, *entry.ObjectID); err != nil || exists {
		t.Fatalf("corrupt remote bytes became a Local object: exists=%v err=%v", exists, err)
	}
	if _, err := os.Stat(badDestination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt read-through published destination: stat err=%v", err)
	}
}
