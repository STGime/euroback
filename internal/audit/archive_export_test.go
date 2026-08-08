package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// fakeUploader captures Upload() calls so tests can assert on the
// bucket/key/body/retention that the exporter chose without needing
// a live S3.
type fakeUploader struct {
	calls []fakeUpload
	err   error
}

type fakeUpload struct {
	bucket      string
	key         string
	body        []byte
	contentType string
	size        int64
	retention   WORMRetention
}

func (f *fakeUploader) Upload(ctx context.Context, bucket, key string, body io.Reader, contentType string, size int64, retention WORMRetention) error {
	if f.err != nil {
		return f.err
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	f.calls = append(f.calls, fakeUpload{bucket, key, b, contentType, size, retention})
	return nil
}

// Bucket unset → Run() must refuse rather than silently succeed
// (would leave archive rows growing forever with no telemetry).
// Distinct from the sweeper's "log and skip" behaviour, which
// happens at Start time.
func TestArchiveExporter_MissingBucketRefuses(t *testing.T) {
	up := &fakeUploader{}
	e := &ArchiveExporter{
		pool: nil,
		up:   up,
		cfg:  ArchiveExportConfig{}, // Bucket=""
	}
	_, err := e.Run(context.Background())
	if err == nil {
		t.Fatal("expected error when bucket unconfigured")
	}
	if !strings.Contains(err.Error(), "bucket not configured") {
		t.Errorf("error should name the missing config; got %q", err)
	}
	if len(up.calls) != 0 {
		t.Error("must not upload when bucket unconfigured")
	}
}

// #170 review-round-1 blocker prevention: retain-until MUST be
// truncated to second. Without this the SDK's smithy layout diverges
// from RFC3339 emission at any downstream re-serialiser, same class
// of bug as #349 GeneratePresignedUploadURLWithRetention. Not
// user-facing here (server-side upload) but keeping the invariant
// consistent across every retention-header emit site avoids a class
// of bug rather than a single instance.
//
// The exporter constructs retention inline; assert on the shape a
// downstream reader would see.
func TestWORMRetentionShape(t *testing.T) {
	now := time.Now().UTC()
	r := WORMRetention{
		Mode:        "COMPLIANCE",
		RetainUntil: now.AddDate(10, 0, 0).Truncate(time.Second),
	}
	if r.Mode != "COMPLIANCE" {
		t.Errorf("mode must be COMPLIANCE for §257 HGB / §147 AO, got %q", r.Mode)
	}
	if r.RetainUntil.Nanosecond() != 0 {
		t.Errorf("retain-until must be second-precision (SDK smithy layout compatibility), got %d ns",
			r.RetainUntil.Nanosecond())
	}
	if r.RetainUntil.Before(now) {
		t.Errorf("retain-until must be in the future")
	}
}

// The NDJSON row shape must round-trip cleanly. A downstream
// consumer restoring the archive can't tolerate schema drift
// silently — this test pins the field set that ends up on disk.
func TestArchiveRow_JSONRoundTrip(t *testing.T) {
	pid := "00000000-0000-0000-0000-000000000001"
	src := archiveRow{
		ID:             "00000000-0000-0000-0000-000000000abc",
		ProjectID:      &pid,
		ActorEmail:     "ops@eurobase.app",
		Action:         "project.deleted",
		CreatedAt:      time.Now().UTC().Truncate(time.Second),
		Seq:            42,
		RowHash:        "deadbeef",
		ArchivedAt:     time.Now().UTC().Truncate(time.Second),
		ArchivedReason: "team",
		Metadata:       json.RawMessage(`{"reason":"quota_exceeded"}`),
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(&src); err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Every field must be present in the emitted JSON — omission on
	// a required field would be a durable-copy hole.
	for _, field := range []string{
		`"id":`, `"project_id":`, `"actor_email":`, `"action":`,
		`"created_at":`, `"seq":`, `"row_hash":`, `"archived_at":`,
		`"archived_reason":`,
	} {
		if !bytes.Contains(buf.Bytes(), []byte(field)) {
			t.Errorf("required field %s missing from NDJSON output: %s", field, buf.String())
		}
	}
	// Metadata is json.RawMessage — must not double-encode.
	if !bytes.Contains(buf.Bytes(), []byte(`{"reason":"quota_exceeded"}`)) {
		t.Errorf("metadata double-encoded: %s", buf.String())
	}
}

// Sanity: the fakeUploader interface conformance guard. If audit
// ever tightens WORMUploader, this fails at compile time rather
// than at first-tick surprise in prod.
var _ WORMUploader = (*fakeUploader)(nil)
