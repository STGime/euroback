// Package storage provides Scaleway S3-compatible object storage operations.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// ObjectInfo describes a single object in a bucket listing.
type ObjectInfo struct {
	Key          string    `json:"key"`
	ContentType  string    `json:"content_type"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"last_modified"`
}

// ListResult is the response for a paginated object listing.
type ListResult struct {
	Objects     []ObjectInfo `json:"objects"`
	NextToken   string       `json:"next_token,omitempty"`
	IsTruncated bool         `json:"is_truncated"`
}

// S3Client wraps an S3 client configured for Scaleway Object Storage.
type S3Client struct {
	client        *s3.Client
	presignClient *s3.PresignClient
	endpoint      string
	region        string
}

// NewS3Client creates a new S3 client configured for Scaleway's S3-compatible endpoint.
func NewS3Client(endpoint, region, accessKey, secretKey string) (*S3Client, error) {
	if region == "" {
		region = "fr-par"
	}

	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})

	presignClient := s3.NewPresignClient(client)

	slog.Info("s3 client initialized", "endpoint", endpoint, "region", region)

	return &S3Client{
		client:        client,
		presignClient: presignClient,
		endpoint:      endpoint,
		region:        region,
	}, nil
}

// CreateBucket creates a private S3 bucket. Returns nil if the bucket already exists (idempotent).
func (s *S3Client) CreateBucket(ctx context.Context, bucketName string) error {
	slog.Info("creating s3 bucket", "bucket", bucketName)

	_, err := s.client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucketName),
	})
	if err != nil {
		// Check if bucket already exists (owned by us).
		var alreadyOwned *types.BucketAlreadyOwnedByYou
		var alreadyExists *types.BucketAlreadyExists
		if errors.As(err, &alreadyOwned) || errors.As(err, &alreadyExists) {
			slog.Info("bucket already exists, skipping creation", "bucket", bucketName)
			return nil
		}
		return fmt.Errorf("create bucket %s: %w", bucketName, err)
	}

	// Block all public access by setting the bucket ACL to private.
	_, err = s.client.PutBucketAcl(ctx, &s3.PutBucketAclInput{
		Bucket: aws.String(bucketName),
		ACL:    types.BucketCannedACLPrivate,
	})
	if err != nil {
		return fmt.Errorf("set bucket acl to private %s: %w", bucketName, err)
	}

	slog.Info("s3 bucket created", "bucket", bucketName)
	return nil
}

// DeleteBucket deletes all objects in the bucket, then deletes the bucket itself.
func (s *S3Client) DeleteBucket(ctx context.Context, bucketName string) error {
	slog.Info("deleting s3 bucket", "bucket", bucketName)

	// List and delete all objects first.
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucketName),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list objects in bucket %s: %w", bucketName, err)
		}

		if len(page.Contents) == 0 {
			continue
		}

		objects := make([]types.ObjectIdentifier, len(page.Contents))
		for i, obj := range page.Contents {
			objects[i] = types.ObjectIdentifier{
				Key: obj.Key,
			}
		}

		_, err = s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(bucketName),
			Delete: &types.Delete{
				Objects: objects,
				Quiet:   aws.Bool(true),
			},
		})
		if err != nil {
			return fmt.Errorf("delete objects in bucket %s: %w", bucketName, err)
		}
	}

	// Delete the bucket.
	_, err := s.client.DeleteBucket(ctx, &s3.DeleteBucketInput{
		Bucket: aws.String(bucketName),
	})
	if err != nil {
		return fmt.Errorf("delete bucket %s: %w", bucketName, err)
	}

	slog.Info("s3 bucket deleted", "bucket", bucketName)
	return nil
}

// BucketExists checks whether the given bucket exists and is accessible.
func (s *S3Client) BucketExists(ctx context.Context, bucketName string) (bool, error) {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bucketName),
	})
	if err != nil {
		var notFound *types.NotFound
		if errors.As(err, &notFound) {
			return false, nil
		}
		// Also handle the case where HeadBucket returns a generic error for non-existent buckets.
		return false, fmt.Errorf("head bucket %s: %w", bucketName, err)
	}
	return true, nil
}

// UploadObject streams an upload to the given bucket/key without buffering the
// entire file in memory.
func (s *S3Client) UploadObject(ctx context.Context, bucketName, key string, reader io.Reader, contentType string, size int64) error {
	slog.Info("uploading object", "bucket", bucketName, "key", key, "content_type", contentType, "size", size)

	input := &s3.PutObjectInput{
		Bucket:        aws.String(bucketName),
		Key:           aws.String(key),
		Body:          reader,
		ContentType:   aws.String(contentType),
		ContentLength: aws.Int64(size),
	}

	_, err := s.client.PutObject(ctx, input)
	if err != nil {
		return fmt.Errorf("put object %s/%s: %w", bucketName, key, err)
	}

	slog.Info("object uploaded", "bucket", bucketName, "key", key)
	return nil
}

// DownloadObject retrieves an object and returns a reader, the content type,
// and the content length. The caller is responsible for closing the reader.
func (s *S3Client) DownloadObject(ctx context.Context, bucketName, key string) (io.ReadCloser, string, int64, error) {
	slog.Info("downloading object", "bucket", bucketName, "key", key)

	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(key),
	})
	if err != nil {
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			return nil, "", 0, fmt.Errorf("object not found: %s/%s", bucketName, key)
		}
		return nil, "", 0, fmt.Errorf("get object %s/%s: %w", bucketName, key, err)
	}

	contentType := ""
	if output.ContentType != nil {
		contentType = *output.ContentType
	}

	var size int64
	if output.ContentLength != nil {
		size = *output.ContentLength
	}

	return output.Body, contentType, size, nil
}

// DeleteObject removes an object from the bucket.
func (s *S3Client) DeleteObject(ctx context.Context, bucketName, key string) error {
	slog.Info("deleting object", "bucket", bucketName, "key", key)

	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("delete object %s/%s: %w", bucketName, key, err)
	}

	slog.Info("object deleted", "bucket", bucketName, "key", key)
	return nil
}

// ListObjects lists objects in a bucket with optional prefix filtering and
// pagination via continuation tokens.
func (s *S3Client) ListObjects(ctx context.Context, bucketName, prefix string, limit int, continuationToken string) (*ListResult, error) {
	slog.Info("listing objects", "bucket", bucketName, "prefix", prefix, "limit", limit)

	input := &s3.ListObjectsV2Input{
		Bucket:  aws.String(bucketName),
		MaxKeys: aws.Int32(int32(limit)),
	}
	if prefix != "" {
		input.Prefix = aws.String(prefix)
	}
	if continuationToken != "" {
		input.ContinuationToken = aws.String(continuationToken)
	}

	output, err := s.client.ListObjectsV2(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("list objects %s: %w", bucketName, err)
	}

	objects := make([]ObjectInfo, 0, len(output.Contents))
	for _, obj := range output.Contents {
		var info ObjectInfo
		if obj.Key != nil {
			info.Key = *obj.Key
		}
		if obj.Size != nil {
			info.Size = *obj.Size
		}
		if obj.LastModified != nil {
			info.LastModified = *obj.LastModified
		}
		objects = append(objects, info)
	}

	isTruncated := false
	if output.IsTruncated != nil {
		isTruncated = *output.IsTruncated
	}

	result := &ListResult{
		Objects:     objects,
		IsTruncated: isTruncated,
	}
	if output.NextContinuationToken != nil {
		result.NextToken = *output.NextContinuationToken
	}

	return result, nil
}

// GeneratePresignedUploadURL creates a pre-signed PUT URL for direct client
// uploads. Default expiry is 15 minutes if expiry <= 0.
func (s *S3Client) GeneratePresignedUploadURL(ctx context.Context, bucketName, key, contentType string, expiry time.Duration) (string, error) {
	if expiry <= 0 {
		expiry = 15 * time.Minute
	}

	slog.Info("generating presigned upload URL", "bucket", bucketName, "key", key, "expiry", expiry)

	input := &s3.PutObjectInput{
		Bucket:      aws.String(bucketName),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	}

	presigned, err := s.presignClient.PresignPutObject(ctx, input, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("presign put object %s/%s: %w", bucketName, key, err)
	}

	return presigned.URL, nil
}

// GeneratePresignedDownloadURL creates a pre-signed GET URL for direct client
// downloads. Default expiry is 1 hour if expiry <= 0.
func (s *S3Client) GeneratePresignedDownloadURL(ctx context.Context, bucketName, key string, expiry time.Duration) (string, error) {
	if expiry <= 0 {
		expiry = 1 * time.Hour
	}

	slog.Info("generating presigned download URL", "bucket", bucketName, "key", key, "expiry", expiry)

	input := &s3.GetObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(key),
	}

	presigned, err := s.presignClient.PresignGetObject(ctx, input, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("presign get object %s/%s: %w", bucketName, key, err)
	}

	return presigned.URL, nil
}
