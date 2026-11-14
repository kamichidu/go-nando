// Package s3 implements the single S3-compatible remote object adapter used
// for AWS S3 and Wasabi.
package s3

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awsS3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/kamichidu/go-nando/internal/content"
	"github.com/kamichidu/go-nando/internal/objectstore"
)

var (
	ErrObjectConflict = errors.New("S3 object does not match requested content ID")
	ErrObjectNotFound = errors.New("S3 object not found")
)

type api interface {
	PutObject(context.Context, *awsS3.PutObjectInput, ...func(*awsS3.Options)) (*awsS3.PutObjectOutput, error)
	HeadObject(context.Context, *awsS3.HeadObjectInput, ...func(*awsS3.Options)) (*awsS3.HeadObjectOutput, error)
	GetObject(context.Context, *awsS3.GetObjectInput, ...func(*awsS3.Options)) (*awsS3.GetObjectOutput, error)
	ListObjectsV2(context.Context, *awsS3.ListObjectsV2Input, ...func(*awsS3.Options)) (*awsS3.ListObjectsV2Output, error)
}

// ClientConfig selects endpoint, region, and optional static credentials.
// Empty credentials use the AWS SDK credential chain (environment, profiles,
// web identity, or instance credentials).
type ClientConfig struct {
	Endpoint        string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

type Store struct {
	bucket string
	prefix string
	client api
}

var _ objectstore.RemoteObjectStore = (*Store)(nil)

func NewClientStore(ctx context.Context, remoteURI string, settings ClientConfig) (*Store, io.Closer, error) {
	bucket, prefix, err := ParseRemoteURI(remoteURI)
	if err != nil {
		return nil, nil, err
	}
	region := settings.Region
	if region == "" {
		region = "us-east-1"
	}
	options := []func(*config.LoadOptions) error{config.WithRegion(region)}
	if settings.AccessKeyID != "" || settings.SecretAccessKey != "" || settings.SessionToken != "" {
		if settings.AccessKeyID == "" || settings.SecretAccessKey == "" {
			return nil, nil, errors.New("S3 access key ID and secret access key must be configured together")
		}
		options = append(options, config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(settings.AccessKeyID, settings.SecretAccessKey, settings.SessionToken)))
	}
	awsConfig, err := config.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, nil, fmt.Errorf("load S3 credentials: %w", err)
	}
	client := awsS3.NewFromConfig(awsConfig, func(o *awsS3.Options) {
		if settings.Endpoint != "" {
			o.BaseEndpoint = aws.String(settings.Endpoint)
			o.UsePathStyle = true
		}
	})
	return &Store{bucket: bucket, prefix: prefix, client: client}, nil, nil
}

func NewStoreWithAPI(remoteURI string, client api) (*Store, error) {
	bucket, prefix, err := ParseRemoteURI(remoteURI)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("S3 client is required")
	}
	return &Store{bucket: bucket, prefix: prefix, client: client}, nil
}

// ParseRemoteURI accepts s3://bucket[/literal-prefix]. Prefix bytes are
// preserved and object names append "objects/" exactly as configured.
func ParseRemoteURI(uri string) (bucket, prefix string, err error) {
	const scheme = "s3://"
	if !strings.HasPrefix(uri, scheme) {
		return "", "", errors.New("remote URI must use s3://")
	}
	rest := uri[len(scheme):]
	if rest == "" || strings.ContainsAny(rest, "?#\\\x00\r\n") {
		return "", "", errors.New("malformed S3 remote URI")
	}
	bucket, prefix, hasPrefix := strings.Cut(rest, "/")
	if bucket == "" || bucket == "." || bucket == ".." || strings.Contains(bucket, "%") || strings.Contains(bucket, ":") || strings.Contains(bucket, "@") {
		return "", "", errors.New("malformed S3 bucket in remote URI")
	}
	for _, r := range bucket {
		valid := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.'
		if !valid {
			return "", "", errors.New("malformed S3 bucket in remote URI")
		}
	}
	if !hasPrefix {
		prefix = ""
	}
	for _, segment := range strings.Split(prefix, "/") {
		if segment == "." || segment == ".." {
			return "", "", errors.New("malformed S3 object prefix in remote URI")
		}
	}
	return bucket, prefix, nil
}

func (s *Store) key(id content.ID) string { return s.prefix + "objects/" + id.String() }

func (s *Store) Upload(ctx context.Context, id content.ID, size int64, expectedMD5 []byte, src io.Reader) (objectstore.RemoteObjectInfo, error) {
	if err := id.Validate(); err != nil {
		return objectstore.RemoteObjectInfo{}, err
	}
	if size < 0 || len(expectedMD5) != md5.Size {
		return objectstore.RemoteObjectInfo{}, errors.New("S3 upload requires nonnegative size and a precomputed 16-byte MD5")
	}
	key := s.key(id)
	attrs, err := s.head(ctx, key)
	if err == nil {
		if matches(attrs, id, size, expectedMD5) {
			return attrs.info, nil
		}
		return objectstore.RemoteObjectInfo{}, fmt.Errorf("%w: existing key %q has incompatible HEAD attributes", ErrObjectConflict, key)
	}
	if !errors.Is(err, ErrObjectNotFound) {
		return objectstore.RemoteObjectInfo{}, fmt.Errorf("inspect S3 key %q before upload: %w", key, err)
	}

	seekable, ok := src.(io.ReadSeeker)
	if !ok {
		return objectstore.RemoteObjectInfo{}, errors.New("S3 single-PUT requires a seekable staged source for SDK checksum validation")
	}
	md5Value := base64.StdEncoding.EncodeToString(expectedMD5)
	sha256Value := base64.StdEncoding.EncodeToString(id.Digest())
	_, putErr := s.client.PutObject(ctx, &awsS3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), Body: seekable,
		ContentLength: aws.Int64(size), ContentMD5: aws.String(md5Value),
		ChecksumSHA256: aws.String(sha256Value), IfNoneMatch: aws.String("*"),
		Metadata: map[string]string{"nando-content-id": id.String(), "nando-md5": md5Value},
	})
	attrs, headErr := s.head(ctx, key)
	if headErr == nil {
		if matches(attrs, id, size, expectedMD5) {
			return attrs.info, nil
		}
		return objectstore.RemoteObjectInfo{}, fmt.Errorf("%w: final key %q has incompatible HEAD attributes", ErrObjectConflict, key)
	}
	if putErr != nil {
		return objectstore.RemoteObjectInfo{}, errors.Join(fmt.Errorf("conditional S3 PutObject %q: %w", key, putErr), headErr)
	}
	return objectstore.RemoteObjectInfo{}, fmt.Errorf("S3 PutObject %q was acknowledged but HEAD reconciliation failed: %w", key, headErr)
}

type headInfo struct {
	info objectstore.RemoteObjectInfo
	sha  string
}

func matches(attrs headInfo, id content.ID, size int64, expectedMD5 []byte) bool {
	return attrs.info.ID == id && attrs.info.ContentID == id.String() && attrs.info.Size == size && bytes.Equal(attrs.info.MD5, expectedMD5) && attrs.sha == base64.StdEncoding.EncodeToString(id.Digest())
}

func (s *Store) head(ctx context.Context, key string) (headInfo, error) {
	output, err := s.client.HeadObject(ctx, &awsS3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), ChecksumMode: types.ChecksumModeEnabled})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "404") {
			return headInfo{}, ErrObjectNotFound
		}
		return headInfo{}, err
	}
	metadata := output.Metadata
	contentID := metadata["nando-content-id"]
	md5Value := metadata["nando-md5"]
	decodedMD5, decodeErr := base64.StdEncoding.DecodeString(md5Value)
	if decodeErr != nil || len(decodedMD5) != md5.Size {
		decodedMD5 = nil
	}
	id, idErr := content.ParseID(contentID)
	if idErr != nil {
		id = content.ID{}
	}
	var size int64
	if output.ContentLength != nil {
		size = *output.ContentLength
	} else {
		return headInfo{}, errors.New("S3 HEAD omitted Content-Length")
	}
	sha := ""
	if output.ChecksumSHA256 != nil {
		sha = *output.ChecksumSHA256
	}
	return headInfo{info: objectstore.RemoteObjectInfo{ID: id, ContentID: contentID, Size: size, MD5: decodedMD5}, sha: sha}, nil
}

func (s *Store) RemoteAttrs(ctx context.Context, id content.ID) (objectstore.RemoteObjectInfo, error) {
	if err := id.Validate(); err != nil {
		return objectstore.RemoteObjectInfo{}, err
	}
	attrs, err := s.head(ctx, s.key(id))
	if err != nil {
		return objectstore.RemoteObjectInfo{}, err
	}
	if attrs.info.ID != id || attrs.info.ContentID != id.String() {
		return objectstore.RemoteObjectInfo{}, ErrObjectConflict
	}
	return attrs.info, nil
}

func (s *Store) OpenRemote(ctx context.Context, id content.ID) (io.ReadCloser, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	out, err := s.client.GetObject(ctx, &awsS3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.key(id))})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "404") {
			return nil, objectstore.ErrObjectNotFound
		}
		return nil, err
	}
	return out.Body, nil
}

func (s *Store) RemoteExists(ctx context.Context, id content.ID) (bool, error) {
	attrs, err := s.RemoteAttrs(ctx, id)
	if errors.Is(err, ErrObjectNotFound) {
		return false, nil
	}
	return err == nil && attrs.ID == id, err
}

func (s *Store) ListRemoteObjects(ctx context.Context) ([]objectstore.RemoteObjectInfo, error) {
	var result []objectstore.RemoteObjectInfo
	var failures []error
	var token *string
	for {
		page, err := s.client.ListObjectsV2(ctx, &awsS3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(s.prefix + "objects/"), ContinuationToken: token})
		if err != nil {
			return result, errors.Join(append(failures, fmt.Errorf("list S3 objects: %w", err))...)
		}
		for _, item := range page.Contents {
			if item.Key == nil {
				continue
			}
			attrs, headErr := s.head(ctx, *item.Key)
			if headErr != nil {
				failures = append(failures, fmt.Errorf("HEAD S3 object %q: %w", *item.Key, headErr))
				continue
			}
			if attrs.info.ID.Validate() != nil || attrs.info.ContentID != attrs.info.ID.String() || *item.Key != s.key(attrs.info.ID) {
				failures = append(failures, fmt.Errorf("S3 object %q has invalid Content ID metadata or key", *item.Key))
				continue
			}
			result = append(result, attrs.info)
		}
		if page.IsTruncated == nil || !*page.IsTruncated || page.NextContinuationToken == nil {
			break
		}
		token = page.NextContinuationToken
	}
	return result, errors.Join(failures...)
}
