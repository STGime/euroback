package workers

// River worker for #355 — delivers one batch of audit_log rows to a
// single destination row's syslog endpoint over TLS. Scheduled per-
// destination by the shared audit_delivery_scheduler.
//
// Same shape as deliver_audit_webhook.go: load destination fresh,
// resolve optional TLS client cert from vault, drain-until-empty
// loop capped at auditSyslogMaxBatchesPerJob, CAS-advance cursor
// per successful batch. Failure mid-batch advances the cursor to
// the last successfully-written seq (TLS + TCP guarantees ordered
// delivery), returns the error so River retries — the next attempt
// starts from the new cursor, no re-delivery of already-shipped
// events.

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"fmt"
	"log/slog"

	"github.com/eurobase/euroback/internal/audit/export"
	"github.com/eurobase/euroback/internal/compliance"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/eurobase/euroback/internal/vault"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

const (
	// auditSyslogBatchSize caps events per dial. Syslog handshake
	// tax is per-batch, so a larger batch amortises better than
	// webhook's 100; syslog sinks generally accept much larger
	// batches than HTTP webhooks.
	auditSyslogBatchSize = 500

	// auditSyslogMaxBatchesPerJob caps the drain loop the same
	// way #354 does — 20 * 500 = 10k events per job invocation,
	// bounded so a runaway insert rate can't turn one job into
	// an unbounded write storm.
	auditSyslogMaxBatchesPerJob = 20
)

// DeliverAuditSyslogWorker handles jobs.DeliverAuditSyslogArgs.
type DeliverAuditSyslogWorker struct {
	river.WorkerDefaults[jobs.DeliverAuditSyslogArgs]
	Pool      *pgxpool.Pool
	Deliverer *export.SyslogDeliverer
	Vault     *vault.VaultService
}

func (w *DeliverAuditSyslogWorker) Work(ctx context.Context, job *river.Job[jobs.DeliverAuditSyslogArgs]) error {
	destID := job.Args.DestinationID

	var (
		projectID  string
		schemaName string
		kind       string
		endpoint   string
		secretRef  *string
		enabled    bool
		lastCursor int64
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
		return fmt.Errorf("load destination %s: %w", destID, err)
	}
	if !enabled {
		return nil
	}
	if kind != string(compliance.DestinationSyslog) {
		// Webhook jobs go through DeliverAuditWebhookWorker. Drop.
		return nil
	}

	// Resolve optional TLS client cert once per job — the vault
	// value can't change within a job invocation. secret_ref for
	// syslog is a PEM bundle (cert + key) rather than an HMAC key.
	// Nil-safe: NULL secret_ref → server-cert-only TLS.
	var clientCert *tls.Certificate
	if secretRef != nil && *secretRef != "" && w.Vault != nil {
		sec, err := w.Vault.Get(ctx, schemaName, *secretRef)
		if err != nil {
			return fmt.Errorf("resolve syslog client cert %q: %w", *secretRef, err)
		}
		cert, err := loadTLSCertFromPEMBundle([]byte(sec.Value))
		if err != nil {
			return fmt.Errorf("parse syslog client cert %q: %w", *secretRef, err)
		}
		clientCert = &cert
	}

	var totalDelivered int
	for i := 0; i < auditSyslogMaxBatchesPerJob; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		events, err := w.readSyslogBatch(ctx, projectID, lastCursor)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			break // caught up
		}

		delivered, sendErr := w.Deliverer.DialAndSend(ctx, endpoint, clientCert, events)
		if sendErr != nil {
			// Whole batch failed — hold cursor. Syslog has no
			// per-event ACK, so any error in the batch means we
			// don't know which events reached the sink and must
			// re-ship the whole batch on the next attempt. Sinks
			// dedupe on the seq field in SD-DATA (documented in
			// audit-export.md) so re-delivery is lossless.
			slog.Warn("audit syslog delivery failed",
				"dest_id", destID, "error", sendErr,
				"batches_this_job", i, "delivered_this_job", totalDelivered)
			return sendErr
		}
		// Full-batch success — CAS-advance cursor to the batch max
		// (WHERE last_cursor < $2 keeps it monotonic under any
		// theoretical concurrent runner).
		if _, cerr := w.Pool.Exec(ctx,
			`UPDATE public.audit_export_destinations
			    SET last_cursor = $2, updated_at = now()
			  WHERE id = $1::uuid AND last_cursor < $2`,
			destID, delivered,
		); cerr != nil {
			return fmt.Errorf("advance cursor: %w", cerr)
		}
		slog.Info("audit syslog delivered batch",
			"dest_id", destID, "events", len(events), "cursor", delivered)
		lastCursor = delivered
		totalDelivered += len(events)
	}
	if totalDelivered == auditSyslogMaxBatchesPerJob*auditSyslogBatchSize {
		slog.Warn("audit syslog: batch cap reached, backlog not fully drained this job",
			"dest_id", destID, "delivered", totalDelivered,
			"next_tick_in", auditDeliverySchedulerInterval.String())
	}
	return nil
}

// readSyslogBatch pulls the next auditSyslogBatchSize rows above
// cursor, mapping directly to export.SyslogEvent. Ordering by seq
// ASC keeps cursor advancement monotonic. Filters out global-chain
// events (project_id IS NULL) — those are platform events and don't
// belong on a tenant SIEM.
func (w *DeliverAuditSyslogWorker) readSyslogBatch(ctx context.Context, projectID string, cursor int64) ([]export.SyslogEvent, error) {
	rows, err := w.Pool.Query(ctx,
		`SELECT id::text, project_id::text, COALESCE(actor_id::text, ''),
		        action, row_hash, seq, created_at
		   FROM public.audit_log
		  WHERE project_id = $1::uuid AND seq > $2
		  ORDER BY seq ASC
		  LIMIT $3`,
		projectID, cursor, auditSyslogBatchSize,
	)
	if err != nil {
		return nil, fmt.Errorf("read audit batch: %w", err)
	}
	defer rows.Close()

	var events []export.SyslogEvent
	for rows.Next() {
		var (
			ev      export.SyslogEvent
			rowHash []byte
		)
		if err := rows.Scan(&ev.ID, &ev.ProjectID, &ev.ActorID,
			&ev.Action, &rowHash, &ev.Seq, &ev.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan audit row: %w", err)
		}
		ev.RowHash = hexEncode(rowHash)
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit rows: %w", err)
	}
	return events, nil
}

// loadTLSCertFromPEMBundle parses a vault-stored PEM bundle
// (concatenated CERTIFICATE + PRIVATE KEY blocks in either order)
// into a tls.Certificate for mutual-TLS to the syslog sink.
//
// The vault stores the bundle as a single string value — one
// vault key per destination, not two — because the console UI has
// one "Vault secret name" field. Parsing accepts either order and
// tolerates additional CERTIFICATE blocks (intermediate chain).
func loadTLSCertFromPEMBundle(bundle []byte) (tls.Certificate, error) {
	var (
		certPEM []byte
		keyPEM  []byte
		rest    = bundle
	)
	for {
		block, remaining := pem.Decode(rest)
		if block == nil {
			break
		}
		switch block.Type {
		case "CERTIFICATE":
			// Append so intermediate chain travels along too.
			certPEM = append(certPEM, pem.EncodeToMemory(block)...)
		case "PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY":
			keyPEM = pem.EncodeToMemory(block)
		}
		rest = remaining
	}
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return tls.Certificate{}, fmt.Errorf("bundle must contain both CERTIFICATE and PRIVATE KEY blocks")
	}
	return tls.X509KeyPair(certPEM, keyPEM)
}

