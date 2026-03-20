package storage

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// setupTestS3 creates an S3Client pointing to the local MinIO instance.
func setupTestS3(t *testing.T) *S3Client {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}

	endpoint := os.Getenv("S3_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:9000"
	}
	accessKey := os.Getenv("S3_ACCESS_KEY")
	if accessKey == "" {
		accessKey = "minioadmin"
	}
	secretKey := os.Getenv("S3_SECRET_KEY")
	if secretKey == "" {
		secretKey = "minioadmin"
	}
	region := os.Getenv("S3_REGION")
	if region == "" {
		region = "us-east-1"
	}

	client, err := NewS3Client(endpoint, region, accessKey, secretKey)
	if err != nil {
		t.Skipf("cannot create S3 client (MinIO not running?): %v", err)
	}

	// Verify connectivity by listing buckets.
	ctx := context.Background()
	_, err = client.client.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		t.Skipf("cannot connect to MinIO: %v", err)
	}

	return client
}

func TestCreateBucket(t *testing.T) {
	client := setupTestS3(t)
	ctx := context.Background()
	bucketName := "test-create-bucket"

	t.Cleanup(func() {
		_ = client.DeleteBucket(ctx, bucketName)
	})

	err := client.CreateBucket(ctx, bucketName)
	if err != nil {
		t.Fatalf("CreateBucket() returned error: %v", err)
	}

	exists, err := client.BucketExists(ctx, bucketName)
	if err != nil {
		t.Fatalf("BucketExists() returned error: %v", err)
	}
	if !exists {
		t.Error("expected bucket to exist after creation")
	}
}

func TestCreateBucketIdempotent(t *testing.T) {
	client := setupTestS3(t)
	ctx := context.Background()
	bucketName := "test-idempotent-bucket"

	t.Cleanup(func() {
		_ = client.DeleteBucket(ctx, bucketName)
	})

	err := client.CreateBucket(ctx, bucketName)
	if err != nil {
		t.Fatalf("first CreateBucket() returned error: %v", err)
	}

	// Second call should not return an error.
	err = client.CreateBucket(ctx, bucketName)
	if err != nil {
		t.Fatalf("second CreateBucket() returned error (should be idempotent): %v", err)
	}
}

func TestDeleteBucket(t *testing.T) {
	client := setupTestS3(t)
	ctx := context.Background()
	bucketName := "test-delete-bucket"

	// Create the bucket.
	err := client.CreateBucket(ctx, bucketName)
	if err != nil {
		t.Fatalf("CreateBucket() returned error: %v", err)
	}

	// Upload a small test object.
	body := bytes.NewReader([]byte("test content"))
	_, err = client.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucketName),
		Key:         aws.String("test-object.txt"),
		Body:        body,
		ContentType: aws.String("text/plain"),
	})
	if err != nil {
		t.Fatalf("PutObject() returned error: %v", err)
	}

	// Delete the bucket (should remove objects first).
	err = client.DeleteBucket(ctx, bucketName)
	if err != nil {
		t.Fatalf("DeleteBucket() returned error: %v", err)
	}

	// Verify bucket no longer exists.
	exists, err := client.BucketExists(ctx, bucketName)
	if err != nil {
		// Some S3 implementations return an error for non-existent buckets.
		fmt.Printf("BucketExists after delete returned error (acceptable): %v\n", err)
		return
	}
	if exists {
		t.Error("expected bucket to not exist after deletion")
	}
}
