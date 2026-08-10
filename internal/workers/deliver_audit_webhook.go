package workers

// River worker for #354 — delivers one batch of audit_log rows to a
// single destination row's webhook endpoint. Scheduled per-destination
// by the audit_webhook_scheduler sweeper below.
//
// Reads endpoint/secret_ref/format/enabled/last_cursor from the DB at
// run time (args carry only the destination_id) so a mutated
// destination row is picked up on the next tick without job-payload
// staleness.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/eurobase/euroback/internal/audit/export"
	"github.com/eurobase/euroback/internal/compliance"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/eurobase/euroback/internal/vault"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

const (
	// auditWebhookBatchSize caps rows per POST. Kept small enough
	// that most SIEM sinks don't reject the payload; env-tunable
	// via AUDIT_WEBHOOK_BATCH_SIZE.
	auditWebhookBatchSize = 100

	// auditWebhookMaxBatchesPerJob caps the drain loop inside a
	// single River job invocation, mirroring the #352 archive
	// exporter's shape. Without the loop, a recovered sink after a
	// long outage catches up at one batch per 30s scheduler tick;
	// with it, one job invocation drains up to 20 * 100 = 2000
	// rows before yielding to the scheduler. Cap keeps a
	// pathological insert rate from turning one job into an
	// unbounded upload storm; the next tick picks up whatever's
	// left.
	auditWebhookMaxBatchesPerJob = 20
)

// DeliverAuditWebhookWorker handles jobs.DeliverAuditWebhookArgs.
type DeliverAuditWebhookWorker struct {
	river.WorkerDefaults[jobs.DeliverAuditWebhookArgs]
	Pool      *pgxpool.Pool
	Deliverer *export.Deliverer
	Vault     *vault.VaultService
}

func (w *DeliverAuditWebhookWorker) Work(ctx context.Context, job *river.Job[jobs.DeliverAuditWebhookArgs]) error {
	destID := job.Args.DestinationID

	// Load the destination fresh — a Disabled toggle between enqueue
	// and execute must be honoured immediately.
	var (
		projectID   string
		schemaName  string
		kind        string
		endpoint    string
		secretRef   *string
		enabled     bool
		lastCursor  int64
	)
	err := w.Pool.QueryRow(ctx,
		`SELECT d.project_id::text, p.schema_name, d.kind, d.endpoint,
		        d.secret_ref, d.enabled, d.last_cursor
		   FROM public.audit_export_destinations d
		   JOIN public.projects p ON p.id = d.project_id
		  WHERE d.id = $1::uuid`,
		destID,
	).Scan(&projectID, &schemaName, &kind, &endpoint, &secretRef, &enabled, &lastCursor)
	if err != nil {
		// If the destination was deleted between enqueue and execute,
		// the job silently drops — that's the right thing.
		return fmt.Errorf("load destination %s: %w", destID, err)
	}
	if !enabled {
		return nil
	}
	if kind != string(compliance.DestinationWebhook) {
		// Syslog jobs go through a different worker (#355). Drop.
		return nil
	}

	// Resolve secret once at the top of the drain loop — the vault
	// value doesn't change across batches inside a single job.
	// nil-safe: NULL secret_ref → deliverer omits the sig (#356
	// "unauthenticated" semantics).
	var secret []byte
	if secretRef != nil && *secretRef != "" && w.Vault != nil {
		sec, err := w.Vault.Get(ctx, schemaName, *secretRef)
		if err != nil {
			return fmt.Errorf("resolve webhook secret %q: %w", *secretRef, err)
		}
		secret = []byte(sec.Value)
	}

	// Drain-until-empty loop with a bounded cap, mirroring the
	// #352 archive-exporter shape. Without this, a recovered sink
	// after a long outage catches up at one batch per 30s tick;
	// with it, one job invocation drains up to
	// auditWebhookMaxBatchesPerJob * auditWebhookBatchSize rows.
	var totalDelivered int
	for i := 0; i < auditWebhookMaxBatchesPerJob; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		env, err := w.readBatch(ctx, projectID, lastCursor)
		if err != nil {
			return err
		}
		if env == nil {
			break // caught up
		}

		res := w.Deliverer.PostEnvelope(ctx, endpoint, secret, env)
		if res.Err != nil {
			slog.Warn("audit webhook delivery failed",
				"dest_id", destID, "status", res.StatusCode, "error", res.Err,
				"batches_this_job", i, "delivered_this_job", totalDelivered)
			return res.Err
		}

		if _, err := w.Pool.Exec(ctx,
			`UPDATE public.audit_export_destinations
			    SET last_cursor = $2, updated_at = now()
			  WHERE id = $1::uuid AND last_cursor < $2`,
			destID, env.Cursor,
		); err != nil {
			return fmt.Errorf("advance cursor: %w", err)
		}
		slog.Info("audit webhook delivered",
			"dest_id", destID, "events", len(env.Events), "cursor", env.Cursor)

		lastCursor = env.Cursor
		totalDelivered += len(env.Events)
	}
	if totalDelivered == auditWebhookMaxBatchesPerJob*auditWebhookBatchSize {
		slog.Warn("audit webhook: batch cap reached, backlog not fully drained this job",
			"dest_id", destID, "delivered", totalDelivered,
			"next_tick_in", auditDeliverySchedulerInterval.String())
	}
	return nil
}

// readBatch pulls the next auditWebhookBatchSize rows above cursor
// into an Envelope. Returns (nil, nil) when no rows remain — the
// caller uses that as the loop-exit signal. Ordering by seq ASC
// keeps cursor advancement monotonic.
func (w *DeliverAuditWebhookWorker) readBatch(ctx context.Context, projectID string, cursor int64) (*export.Envelope, error) {
	rows, err := w.Pool.Query(ctx,
		`SELECT id::text, project_id::text, actor_id::text, actor_email, action,
		        target_type, target_id, metadata, ip_address,
		        created_at, seq, row_hash
		   FROM public.audit_log
		  WHERE project_id = $1::uuid AND seq > $2
		  ORDER BY seq ASC
		  LIMIT $3`,
		projectID, cursor, auditWebhookBatchSize,
	)
	if err != nil {
		return nil, fmt.Errorf("read audit batch: %w", err)
	}
	defer rows.Close()

	env := &export.Envelope{DeliveredAt: time.Now().UTC().Truncate(time.Second)}
	for rows.Next() {
		var (
			ev         export.EventRow
			projectID  *string
			actorID    *string
			targetType *string
			targetID   *string
			ip         *string
			metadata   []byte
			rowHash    []byte
		)
		if err := rows.Scan(&ev.ID, &projectID, &actorID, &ev.ActorEmail, &ev.Action,
			&targetType, &targetID, &metadata, &ip,
			&ev.CreatedAt, &ev.Seq, &rowHash,
		); err != nil {
			return nil, fmt.Errorf("scan audit row: %w", err)
		}
		ev.ProjectID = projectID
		ev.ActorID = actorID
		ev.TargetType = targetType
		ev.TargetID = targetID
		ev.IPAddress = ip
		if len(metadata) > 0 {
			ev.Metadata = metadata
		}
		if len(rowHash) > 0 {
			ev.RowHash = hexEncode(rowHash)
		}
		env.Events = append(env.Events, ev)
		if ev.Seq > env.Cursor {
			env.Cursor = ev.Seq
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit rows: %w", err)
	}
	if len(env.Events) == 0 {
		return nil, nil
	}
	return env, nil
}

// hexEncode is a tiny helper local to this worker so it doesn't
// have to import encoding/hex just for one call. Kept out of the
// export package because that package's EventRow already emits
// RowHash as a hex string; this is only used at DB-scan time.
func hexEncode(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hex[v>>4]
		out[i*2+1] = hex[v&0x0f]
	}
	return string(out)
}
