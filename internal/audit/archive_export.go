package audit

// Tier-1 GDPR #170 (object-dump destination only): the archive-to-WORM
// dumper. Reads batches of public.audit_log_archive rows, writes each
// batch as an NDJSON object to a platform-managed S3 bucket with S3
// Object Lock retention, then DELETEs the archived rows.
//
// This closes the loop retention has been assuming existed:
//     append → chain → per-plan prune → archive table → WORM dump → purge
//
// Scope note: #170's full scope covers webhook + syslog + object-dump
// destination kinds. Only the object-dump path is implemented here —
// it's the piece that unblocks retention. Webhook (HMAC-signed POST)
// and syslog (RFC 5424 over TCP/TLS) are per-tenant customer SIEM
// features and belong in their own PR with the audit_export_destinations
// CRUD surface.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// WORMRetention is the retention window the exporter attaches to
// each dumped NDJSON blob. Values match storage.Retention verbatim
// but declared here (rather than imported) to avoid a
// storage→audit→storage cycle: storage's access logger imports
// audit, so audit can't import storage back.
//
// The worker wire-up (internal/workers/audit_archive_export.go)
// bridges to storage.Retention when constructing the WORMUploader.
type WORMRetention struct {
	Mode        string // "COMPLIANCE" | "GOVERNANCE"
	RetainUntil time.Time
}

// WORMUploader is the minimal S3-facing surface the exporter needs.
// Implemented by an adapter in internal/workers that forwards to
// storage.S3Client.UploadObjectWithRetention. Interface lives here so
// audit doesn't reach into storage.
type WORMUploader interface {
	Upload(ctx context.Context, bucket, key string, body io.Reader, contentType string, size int64, retention WORMRetention) error
}

// ArchiveExportConfig configures the WORM dump. Zero-value Bucket
// disables the exporter — the caller (StartAuditArchiveExporter)
// treats that as "not configured, skip" rather than an error.
type ArchiveExportConfig struct {
	// Bucket is the platform-managed S3 bucket. Must have S3 Object
	// Lock enabled at create time (S3 rejects lock headers on a
	// non-lock bucket). Provisioned once by ops via
	// scripts/provision-audit-archive-bucket.sh; see the runbook in
	// the PR description.
	Bucket string

	// KeyPrefix scopes archive objects inside the bucket. Defaults
	// to "audit-log-archive/" if empty. Final key is
	// <prefix>YYYY/MM/DD/<run-uuid>.ndjson so daily dumps sit under
	// browsable date folders and each run's blob is unique (WORM +
	// same-key-twice would be an error on the second write).
	KeyPrefix string

	// RetentionYears controls the S3 Object Lock retain-until.
	// Defaults to 10 (§257 HGB / §147 AO worst case). Compliance
	// mode — not even root credentials can shorten before expiry.
	RetentionYears int

	// BatchSize caps rows per NDJSON blob. Kept small enough to
	// bound memory (each row is a few KB), large enough to amortise
	// S3 round-trips. Defaults to 5000.
	BatchSize int
}

// ArchiveExportResult summarises a single Run() pass.
type ArchiveExportResult struct {
	Batches     int
	RowsDumped  int64
	ObjectKeys  []string
}

// ArchiveExporter is the Go wrapper. Constructed once; Run() is
// re-entrant across ticks (SELECT … LIMIT + DELETE where id IN,
// idempotent under crash-mid-run because unwritten rows stay in
// the archive for the next tick).
type ArchiveExporter struct {
	pool *pgxpool.Pool
	up   WORMUploader
	cfg  ArchiveExportConfig
}

func NewArchiveExporter(pool *pgxpool.Pool, up WORMUploader, cfg ArchiveExportConfig) *ArchiveExporter {
	if cfg.KeyPrefix == "" {
		cfg.KeyPrefix = "audit-log-archive/"
	}
	if cfg.RetentionYears <= 0 {
		cfg.RetentionYears = 10
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 5000
	}
	return &ArchiveExporter{pool: pool, up: up, cfg: cfg}
}

// archiveRow is the NDJSON line shape. Keys deliberately match
// audit_log column names so a downstream reader can round-trip into
// the same schema without translation. archived_at + archived_reason
// come along for provenance.
type archiveRow struct {
	ID             string          `json:"id"`
	ProjectID      *string         `json:"project_id,omitempty"`
	ActorID        *string         `json:"actor_id,omitempty"`
	ActorEmail     string          `json:"actor_email"`
	Action         string          `json:"action"`
	TargetType     *string         `json:"target_type,omitempty"`
	TargetID       *string         `json:"target_id,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	IPAddress      *string         `json:"ip_address,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	Seq            int64           `json:"seq"`
	PrevHash       *string         `json:"prev_hash,omitempty"`
	RowHash        string          `json:"row_hash"`
	ArchivedAt     time.Time       `json:"archived_at"`
	ArchivedReason string          `json:"archived_reason"`
}

// Run pulls one batch of archived rows and dumps them to a single
// NDJSON blob under Object Lock, then deletes those rows from the
// archive. Returns after ONE batch — the caller (ticker) invokes it
// again on the next tick, or a follow-up loop can call Run() until
// RowsDumped == 0 for catch-up runs.
//
// Ordering: read → write → delete. If the process crashes between
// write and delete, the next tick re-reads the same rows and writes
// them to a NEW blob (fresh run UUID in the key). That's a bounded-
// duplication trade — better than dropping rows to appease
// idempotency. The downstream reader dedupes on (id, archived_at) if
// it cares.
func (e *ArchiveExporter) Run(ctx context.Context) (*ArchiveExportResult, error) {
	if e.cfg.Bucket == "" {
		return nil, errors.New("archive exporter: bucket not configured")
	}
	res := &ArchiveExportResult{}

	rows, err := e.pool.Query(ctx,
		`SELECT id, project_id, actor_id, actor_email, action,
		        target_type, target_id, metadata, ip_address,
		        created_at, seq, prev_hash, row_hash,
		        archived_at, archived_reason
		   FROM public.audit_log_archive
		  ORDER BY archived_at ASC
		  LIMIT $1`,
		e.cfg.BatchSize,
	)
	if err != nil {
		return res, fmt.Errorf("select archive batch: %w", err)
	}

	var (
		buf     bytes.Buffer
		ids     []string
		earliest time.Time
		rowCount int64
	)
	enc := json.NewEncoder(&buf)
	for rows.Next() {
		var r archiveRow
		var (
			projectID, actorID, targetType, targetID, ipAddress *string
			prevHash                                            []byte
			rowHash                                             []byte
			metadata                                            []byte
		)
		if err := rows.Scan(
			&r.ID, &projectID, &actorID, &r.ActorEmail, &r.Action,
			&targetType, &targetID, &metadata, &ipAddress,
			&r.CreatedAt, &r.Seq, &prevHash, &rowHash,
			&r.ArchivedAt, &r.ArchivedReason,
		); err != nil {
			rows.Close()
			return res, fmt.Errorf("scan archive row: %w", err)
		}
		r.ProjectID = projectID
		r.ActorID = actorID
		r.TargetType = targetType
		r.TargetID = targetID
		r.IPAddress = ipAddress
		if len(metadata) > 0 {
			r.Metadata = json.RawMessage(metadata)
		}
		if len(prevHash) > 0 {
			h := hex.EncodeToString(prevHash)
			r.PrevHash = &h
		}
		r.RowHash = hex.EncodeToString(rowHash)
		if err := enc.Encode(&r); err != nil {
			rows.Close()
			return res, fmt.Errorf("encode archive row: %w", err)
		}
		ids = append(ids, r.ID)
		if earliest.IsZero() || r.ArchivedAt.Before(earliest) {
			earliest = r.ArchivedAt
		}
		rowCount++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("iterate archive rows: %w", err)
	}
	if rowCount == 0 {
		return res, nil // nothing to do this tick — normal steady state
	}

	// Key: <prefix>YYYY/MM/DD/<runUUID>.ndjson. Date bucket by the
	// EARLIEST archived_at in the batch — puts continuous days
	// together instead of splitting a day across two folders if a
	// batch straddles midnight. runUUID guarantees uniqueness so
	// Object Lock can never bounce a same-key write.
	runID := randomHex(16) // 32-char hex — collision-free per bucket-lifetime
	key := fmt.Sprintf("%s%04d/%02d/%02d/%s.ndjson",
		e.cfg.KeyPrefix,
		earliest.UTC().Year(), earliest.UTC().Month(), earliest.UTC().Day(),
		runID,
	)

	// Object Lock: compliance mode, retain-until = now + N years.
	// Truncated to second so the SDK's smithy layout matches (same
	// fix as #349 GeneratePresignedUploadURLWithRetention).
	retention := WORMRetention{
		Mode:        "COMPLIANCE",
		RetainUntil: time.Now().UTC().AddDate(e.cfg.RetentionYears, 0, 0).Truncate(time.Second),
	}

	body := buf.Bytes()
	if err := e.up.Upload(ctx, e.cfg.Bucket, key,
		bytes.NewReader(body), "application/x-ndjson", int64(len(body)),
		retention,
	); err != nil {
		return res, fmt.Errorf("upload archive blob %s: %w", key, err)
	}

	// Delete-after-upload. If we crash between upload and delete, the
	// next tick re-uploads the same rows to a new key — bounded
	// duplication rather than dropped rows. Consumers dedupe on
	// (id, archived_at).
	tag, err := e.pool.Exec(ctx,
		`DELETE FROM public.audit_log_archive WHERE id = ANY($1::uuid[])`,
		ids,
	)
	if err != nil {
		return res, fmt.Errorf("purge dumped rows: %w", err)
	}
	if int64(len(ids)) != tag.RowsAffected() {
		slog.Warn("archive export: delete count mismatch",
			"expected", len(ids), "deleted", tag.RowsAffected(),
			"key", key,
		)
	}

	res.Batches = 1
	res.RowsDumped = rowCount
	res.ObjectKeys = []string{key}
	return res, nil
}

// randomHex returns n random bytes hex-encoded. crypto/rand only —
// used for the run UUID in the archive object key, which becomes part
// of a WORM-locked path. Predictable IDs would let a mis-behaved
// second run land on a key another writer expects to own.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand.Read returning error is a should-never-happen
		// on any supported OS. Fall back to a timestamp-based tag
		// rather than panicking a housekeeping worker — the caller
		// will see a duplicate-key error on the very unlikely
		// collision, which surfaces cleanly.
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
