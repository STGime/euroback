// Package storage provides Scaleway S3-compatible object storage operations.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
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
// Object Lock is off by default — call CreateBucketWithObjectLock for
// Legal-Team projects that need WORM enforcement.
func (s *S3Client) CreateBucket(ctx context.Context, bucketName string) error {
	return s.CreateBucketWithObjectLock(ctx, bucketName, false)
}

// CreateBucketWithObjectLock creates a private S3 bucket, optionally
// enabling S3 Object Lock at bucket creation time (a one-time flag —
// Object Lock cannot be turned on after the fact). Returns nil if the
// bucket already exists (idempotent).
//
// For Legal-Team-tier projects we pass enableObjectLock=true so per-
// object retention headers on PUT are honoured by Scaleway. For every
// other tier we pass false — Object Lock adds no cost but exposing it
// only to tiers that need it keeps the storage tier gated and the
// bucket-config surface small.
//
// GoBD §146 Abs. 4 AO ("Unveränderbarkeit") requires WORM at rest for
// tax / accounting / mandant documents; Object Lock in COMPLIANCE mode
// is how we deliver that on S3-compatible storage.
func (s *S3Client) CreateBucketWithObjectLock(ctx context.Context, bucketName string, enableObjectLock bool) error {
	slog.Info("creating s3 bucket", "bucket", bucketName, "object_lock", enableObjectLock)

	input := &s3.CreateBucketInput{
		Bucket: aws.String(bucketName),
	}
	if enableObjectLock {
		input.ObjectLockEnabledForBucket = aws.Bool(true)
	}
	_, err := s.client.CreateBucket(ctx, input)
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

	slog.Info("s3 bucket created", "bucket", bucketName, "object_lock", enableObjectLock)
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

// ObjectLockEnabled reports whether S3 Object Lock is enabled on the
// given bucket. Used by the policy-upsert path to refuse creating a
// retention policy on a bucket that wasn't provisioned with lock
// support — otherwise every subsequent upload would attach lock
// headers and S3 would reject with InvalidRequest.
//
// Returns (false, nil) on the "not enabled" case (S3 returns
// ObjectLockConfigurationNotFoundError in that shape). A network
// failure returns the error verbatim so callers can distinguish
// "definitely off" from "couldn't tell."
func (s *S3Client) ObjectLockEnabled(ctx context.Context, bucketName string) (bool, error) {
	out, err := s.client.GetObjectLockConfiguration(ctx, &s3.GetObjectLockConfigurationInput{
		Bucket: aws.String(bucketName),
	})
	if err != nil {
		// The SDK's typed error for "not configured" isn't reliably
		// surfaced by S3-compatible backends (Scaleway returns a
		// generic 404). Fall back to a substring check on the
		// message — same rationale as isObjectLockedError.
		msg := err.Error()
		if containsFold(msg, "ObjectLockConfigurationNotFoundError") ||
			containsFold(msg, "object lock configuration") ||
			containsFold(msg, "not found") {
			return false, nil
		}
		return false, fmt.Errorf("get object lock configuration %s: %w", bucketName, err)
	}
	if out == nil || out.ObjectLockConfiguration == nil {
		return false, nil
	}
	return out.ObjectLockConfiguration.ObjectLockEnabled == types.ObjectLockEnabledEnabled, nil
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

// Retention describes an S3 Object Lock retention window applied at
// upload time. Zero value = no retention (equivalent to a nil pointer
// in the S3 API); use RetentionCompliance / RetentionGovernance to
// construct.
//
// Mode "compliance" cannot be shortened by any principal, even root
// credentials, until RetainUntil passes — this is what §146 Abs. 4 AO
// / GoBD Unveränderbarkeit requires. Mode "governance" is softer:
// principals with s3:BypassGovernanceRetention can override, which is
// useful for non-legal retention use-cases (e.g. an audit window a
// customer can waive after internal review).
type Retention struct {
	Mode        RetentionMode
	RetainUntil time.Time
}

// RetentionMode is the S3 Object Lock mode. The two-value set matches
// the S3 spec; we surface it as a typed string so callers don't have
// to import the AWS SDK enum.
type RetentionMode string

const (
	RetentionCompliance RetentionMode = "COMPLIANCE"
	RetentionGovernance RetentionMode = "GOVERNANCE"
)

// UploadObject streams an upload to the given bucket/key without buffering the
// entire file in memory. No retention is applied — call
// UploadObjectWithRetention for WORM enforcement.
func (s *S3Client) UploadObject(ctx context.Context, bucketName, key string, reader io.Reader, contentType string, size int64) error {
	return s.UploadObjectWithRetention(ctx, bucketName, key, reader, contentType, size, Retention{})
}

// UploadObjectWithRetention streams an upload and, if retention.Mode
// is set, attaches S3 Object Lock retention headers so the object
// cannot be deleted or overwritten before retention.RetainUntil.
//
// The bucket must have been created with Object Lock enabled
// (CreateBucketWithObjectLock(..., true)) — Object Lock is a one-time
// bucket-level flag, and PutObject with retention headers against a
// non-locked bucket returns InvalidRequest. The provisioning path
// (workers/provision.go) is responsible for setting that flag for
// Legal-Team-tier projects.
//
// A zero-value Retention (Mode=="") behaves exactly like UploadObject.
func (s *S3Client) UploadObjectWithRetention(ctx context.Context, bucketName, key string, reader io.Reader, contentType string, size int64, retention Retention) error {
	slog.Info("uploading object", "bucket", bucketName, "key", key, "content_type", contentType, "size", size, "retention_mode", retention.Mode)

	input := &s3.PutObjectInput{
		Bucket:        aws.String(bucketName),
		Key:           aws.String(key),
		Body:          reader,
		ContentType:   aws.String(contentType),
		ContentLength: aws.Int64(size),
	}
	if retention.Mode != "" {
		input.ObjectLockMode = types.ObjectLockMode(retention.Mode)
		input.ObjectLockRetainUntilDate = aws.Time(retention.RetainUntil)
	}

	_, err := s.client.PutObject(ctx, input)
	if err != nil {
		return fmt.Errorf("put object %s/%s: %w", bucketName, key, err)
	}

	slog.Info("object uploaded", "bucket", bucketName, "key", key, "retention_until", retention.RetainUntil)
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

// ErrObjectLocked is returned by DeleteObject when the underlying S3
// call is refused because the object is under an active Object Lock
// retention window (compliance-mode WORM). Callers surface this as
// HTTP 409 with code "object_locked" so the UI can explain why the
// delete failed and when it would succeed.
//
// We can't reliably fetch the exact retention_until from the delete
// error alone — HeadObject with ObjectLockRetainUntilDate is required
// — so the field is best-effort populated when the caller has already
// looked it up (e.g. the retention-hold check).
type ErrObjectLocked struct {
	Bucket      string
	Key         string
	RetainUntil time.Time // zero if unknown
	Cause       error
}

func (e *ErrObjectLocked) Error() string {
	if !e.RetainUntil.IsZero() {
		return fmt.Sprintf("object %s/%s is under retention until %s", e.Bucket, e.Key, e.RetainUntil.Format(time.RFC3339))
	}
	return fmt.Sprintf("object %s/%s is under retention", e.Bucket, e.Key)
}

func (e *ErrObjectLocked) Unwrap() error { return e.Cause }

// CopyObject creates a server-side copy of an object at a new key
// within the same bucket. Preserves ContentType and other object
// metadata (MetadataDirective defaults to COPY). Used by the
// offline backfill to rename non-NFC keys → NFC without shuffling
// bytes over the wire (S3 copy is server-internal). Callers are
// responsible for the subsequent DeleteObject on the old key.
//
// NOTE: this bypasses any per-prefix retention policy applied to
// the destination key by the retention resolver — the copy is a
// pure S3 op and callers should only use it for administrative
// reshuffling (backfill), not for tenant-facing operations.
func (s *S3Client) CopyObject(ctx context.Context, bucketName, srcKey, dstKey string) error {
	slog.Info("copying object", "bucket", bucketName, "src", srcKey, "dst", dstKey)
	// S3 CopySource requires "bucket/key" URL-escaped.
	copySource := url.PathEscape(bucketName) + "/" + url.PathEscape(srcKey)
	_, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(bucketName),
		Key:        aws.String(dstKey),
		CopySource: aws.String(copySource),
	})
	if err != nil {
		return fmt.Errorf("copy object %s/%s → %s: %w", bucketName, srcKey, dstKey, err)
	}
	return nil
}

// DeleteObject removes an object from the bucket. Returns an
// *ErrObjectLocked (which callers can detect via errors.As) if S3
// refuses because the object is under an active retention window.
func (s *S3Client) DeleteObject(ctx context.Context, bucketName, key string) error {
	slog.Info("deleting object", "bucket", bucketName, "key", key)

	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(key),
	})
	if err != nil {
		if isObjectLockedError(err) {
			return &ErrObjectLocked{Bucket: bucketName, Key: key, Cause: err}
		}
		return fmt.Errorf("delete object %s/%s: %w", bucketName, key, err)
	}

	slog.Info("object deleted", "bucket", bucketName, "key", key)
	return nil
}

// isObjectLockedError inspects an S3 delete error and reports whether
// it looks like an Object-Lock refusal. Scaleway (and S3-in-general)
// returns 403 AccessDenied for locked objects; the SDK surfaces this
// as a *types.ObjectLockConfigurationNotFoundError only for
// GetObjectLockConfiguration, not for DeleteObject. The safest
// available check is a substring match on the message the S3 API
// puts in the error — "Access Denied because object protected by
// object lock" (Scaleway) or "AccessDenied" combined with a
// retention hint. False negatives here degrade to a generic 500 for
// the caller, which is safer than false positives that would mask a
// real permissions bug.
func isObjectLockedError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// Scaleway phrasing observed in fr-par tests.
	if containsFold(msg, "object lock") || containsFold(msg, "retention period") {
		return true
	}
	return false
}

func containsFold(s, substr string) bool {
	// Simple case-insensitive substring — small strings, no need for
	// a compiled regex. Kept as its own tiny helper to make the
	// intent obvious and testable.
	return len(s) >= len(substr) && indexFold(s, substr) >= 0
}

func indexFold(s, substr string) int {
	sn, tn := len(s), len(substr)
	if tn == 0 {
		return 0
	}
	for i := 0; i+tn <= sn; i++ {
		match := true
		for j := 0; j < tn; j++ {
			cs, ct := s[i+j], substr[j]
			if cs >= 'A' && cs <= 'Z' {
				cs += 'a' - 'A'
			}
			if ct >= 'A' && ct <= 'Z' {
				ct += 'a' - 'A'
			}
			if cs != ct {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
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

// PresignedUpload is the result of a presigned PUT-URL request. When
// Retention is set, SignedHeaders carries the x-amz-object-lock-*
// headers that got baked into the SigV4 signature — the client MUST
// echo them verbatim on the PUT or S3 refuses with
// SignatureDoesNotMatch. Passing them out explicitly is what lets a
// SDK/console client construct a valid upload without either
// dropping the lock (bad) or having to know which S3 headers exist
// (also bad).
type PresignedUpload struct {
	URL           string
	SignedHeaders map[string]string
}

// GeneratePresignedUploadURL creates a pre-signed PUT URL for direct client
// uploads. Default expiry is 15 minutes if expiry <= 0.
//
// Legacy signature preserved — returns just the URL. Non-retention
// uploads have no required headers beyond Content-Type, which the
// caller already knows about, so no signed-header map is needed.
func (s *S3Client) GeneratePresignedUploadURL(ctx context.Context, bucketName, key, contentType string, expiry time.Duration) (string, error) {
	p, err := s.GeneratePresignedUploadURLWithRetention(ctx, bucketName, key, contentType, expiry, Retention{})
	if err != nil {
		return "", err
	}
	return p.URL, nil
}

// GeneratePresignedUploadURLWithRetention is the retention-aware
// variant. If retention.Mode is set, the returned PresignedUpload
// carries the x-amz-object-lock-* headers that were baked into the
// SigV4 signature; the client MUST echo them on the PUT or S3
// refuses with SignatureDoesNotMatch. The "can't drop the lock"
// property still holds — a client omitting a header just gets a
// 403, they can't quietly succeed with no retention. A zero-value
// Retention returns SignedHeaders=nil.
//
// The echoed header VALUES come straight from presigned.SignedHeader
// — the SDK's own byte-exact record of what got signed — filtered
// to just the two X-Amz-Object-Lock-* entries. This is the fix for
// review-round-2 blocker: the SDK serialises retain-until with
// smithy's `2006-01-02T15:04:05.999Z` layout (millisecond precision),
// so a hand-rolled `time.RFC3339` format diverges whenever RetainUntil
// has sub-second precision → SignatureDoesNotMatch on the PUT. Reading
// from SignedHeader is immune to that (and to future SDK format
// changes). We also Truncate the incoming RetainUntil to second so
// upstream comparators (audit logs, policy resolution) see a stable
// value regardless of when the presigner ran.
func (s *S3Client) GeneratePresignedUploadURLWithRetention(ctx context.Context, bucketName, key, contentType string, expiry time.Duration, retention Retention) (*PresignedUpload, error) {
	if expiry <= 0 {
		expiry = 15 * time.Minute
	}
	if retention.Mode != "" {
		retention.RetainUntil = retention.RetainUntil.Truncate(time.Second)
	}

	slog.Info("generating presigned upload URL", "bucket", bucketName, "key", key, "expiry", expiry, "retention_mode", retention.Mode)

	input := &s3.PutObjectInput{
		Bucket:      aws.String(bucketName),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	}
	if retention.Mode != "" {
		input.ObjectLockMode = types.ObjectLockMode(retention.Mode)
		input.ObjectLockRetainUntilDate = aws.Time(retention.RetainUntil)
	}

	presigned, err := s.presignClient.PresignPutObject(ctx, input, s3.WithPresignExpires(expiry))
	if err != nil {
		return nil, fmt.Errorf("presign put object %s/%s: %w", bucketName, key, err)
	}

	out := &PresignedUpload{URL: presigned.URL}
	if retention.Mode != "" {
		// Pull only the X-Amz-Object-Lock-* headers out of the
		// signed set. presigned.SignedHeader also contains Host,
		// X-Amz-Content-SHA256, etc. — SigV4 machinery the client
		// gets right for free (Host from the URL, SHA256 the SDK
		// handles) and doesn't need to see. Only the lock headers
		// are opaque to the client without our help.
		out.SignedHeaders = extractObjectLockHeaders(presigned.SignedHeader)
	}
	return out, nil
}

// extractObjectLockHeaders picks out just the two x-amz-object-lock
// headers (case-insensitively) from a signed-header set. Kept as its
// own tiny function so it's cheap to test independently of an S3
// round-trip.
func extractObjectLockHeaders(in map[string][]string) map[string]string {
	out := map[string]string{}
	for k, vs := range in {
		if len(vs) == 0 {
			continue
		}
		lk := lowerFold(k)
		if lk == "x-amz-object-lock-mode" || lk == "x-amz-object-lock-retain-until-date" {
			out[lk] = vs[0]
		}
	}
	return out
}

func lowerFold(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

// GeneratePresignedDownloadURL creates a pre-signed GET URL for direct client
// downloads. Default expiry is 1 hour if expiry <= 0.
func (s *S3Client) GeneratePresignedDownloadURL(ctx context.Context, bucketName, key string, expiry time.Duration) (string, error) {
	return s.GeneratePresignedDownloadURLAs(ctx, bucketName, key, expiry, "")
}

// GeneratePresignedDownloadURLAs is like GeneratePresignedDownloadURL but
// embeds a `Content-Disposition: attachment; filename="…"` directive in
// the presigned URL via the `response-content-disposition` query
// parameter. When the browser follows the link, S3 echoes the header
// back and the user gets a download with the suggested name instead of
// the opaque S3 key (which is the export's UUID for DSAR zips).
//
// suggestedFilename is set verbatim; callers are responsible for
// sanitising any characters that would break a filename or HTTP header.
// Pass "" to fall back to the S3 key's basename (browser default).
func (s *S3Client) GeneratePresignedDownloadURLAs(ctx context.Context, bucketName, key string, expiry time.Duration, suggestedFilename string) (string, error) {
	if expiry <= 0 {
		expiry = 1 * time.Hour
	}

	slog.Info("generating presigned download URL", "bucket", bucketName, "key", key, "expiry", expiry, "filename", suggestedFilename)

	input := &s3.GetObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(key),
	}
	if suggestedFilename != "" {
		input.ResponseContentDisposition = aws.String(
			fmt.Sprintf(`attachment; filename="%s"`, suggestedFilename),
		)
	}

	presigned, err := s.presignClient.PresignGetObject(ctx, input, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("presign get object %s/%s: %w", bucketName, key, err)
	}

	return presigned.URL, nil
}
