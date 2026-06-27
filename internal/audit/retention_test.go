package audit

import (
	"context"
	"testing"
)

// TestRetention_PruneKeepsChainHeadAndVerifies appends a handful of rows,
// runs prune with a 0-day cutoff (everything-older-than-now), and asserts:
//   * The chain head (the newest row) is preserved.
//   * The pruned rows are gone.
//   * A checkpoint row was written for this project.
//   * Verify() walks cleanly using the checkpoint to seed prev_hash.
//
// 0-day cutoff is a useful test idiom: now() - 0 days = now(), so every
// row whose created_at is strictly before now() is prunable. Since INSERT
// happens "in the past" relative to the prune call, all but the head get
// pruned in a single pass.
func TestRetention_PruneKeepsChainHeadAndVerifies(t *testing.T) {
	svc, projectID, pool := setupAuditTest(t)
	ctx := context.Background()

	svc.Log(ctx, projectID, "", "admin@eurobase.app", "ret.one")
	svc.Log(ctx, projectID, "", "admin@eurobase.app", "ret.two")
	svc.Log(ctx, projectID, "", "admin@eurobase.app", "ret.three")

	// Clean baseline.
	if res, err := svc.Verify(ctx, projectID); err != nil {
		t.Fatalf("Verify (pre-prune): %v", err)
	} else if !res.OK || res.Checked != 3 {
		t.Fatalf("baseline verify: %+v", res)
	}

	var deleted int64
	if err := pool.QueryRow(ctx, `SELECT public.prune_audit_log(0)`).Scan(&deleted); err != nil {
		// 0 short-circuits to "do nothing" by contract — sanity check the
		// guard branch.
		t.Fatalf("prune_audit_log(0): %v", err)
	}
	if deleted != 0 {
		t.Fatalf("prune(0) should be a no-op, deleted %d", deleted)
	}

	// Now prune with cutoff_days=0 logically blocked by the guard. Use a
	// SQL helper that mirrors the worker's "everything < now()" intent by
	// passing cutoff_days=1 and back-dating two of three rows by a day.
	if _, err := pool.Exec(ctx,
		`UPDATE public.audit_log SET created_at = created_at - interval '2 days'
		 WHERE project_id = $1 AND action IN ('ret.one','ret.two')`,
		projectID); err != nil {
		t.Skipf("cannot back-date rows (UPDATE denied): %v", err)
	}

	if err := pool.QueryRow(ctx, `SELECT public.prune_audit_log(1)`).Scan(&deleted); err != nil {
		t.Fatalf("prune_audit_log(1): %v", err)
	}
	if deleted != 2 {
		t.Fatalf("expected to prune 2 rows, got %d", deleted)
	}

	// Verify the head survived.
	var remaining int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM public.audit_log WHERE project_id = $1`,
		projectID).Scan(&remaining); err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("expected 1 head row remaining, got %d", remaining)
	}

	// Checkpoint exists for this project.
	var ckExists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(
		   SELECT 1 FROM public.audit_log_chain_checkpoints WHERE project_id = $1::uuid)`,
		projectID).Scan(&ckExists); err != nil {
		t.Fatalf("check checkpoint: %v", err)
	}
	if !ckExists {
		t.Fatal("checkpoint row was not written after prune")
	}

	// Verify still passes — seeded from checkpoint.
	res, err := svc.Verify(ctx, projectID)
	if err != nil {
		t.Fatalf("Verify (post-prune): %v", err)
	}
	if !res.OK {
		t.Fatalf("post-prune verify broken at %s: %s", res.BrokenAtID, res.Reason)
	}
	if res.Checked != 1 {
		t.Errorf("Checked = %d, want 1 (only head remains)", res.Checked)
	}
}

// TestRetention_PruneNeverDeletesHead — even with an absurdly large
// retention window (everything older than 1 day), if the head row itself
// is older than cutoff, we MUST NOT prune it. Tests the "keep at least
// the head" invariant the SQL function guarantees with `seq < head_seq`.
func TestRetention_PruneNeverDeletesHead(t *testing.T) {
	svc, projectID, pool := setupAuditTest(t)
	ctx := context.Background()

	svc.Log(ctx, projectID, "", "admin@eurobase.app", "head.only")

	// Back-date the single row so cutoff catches it.
	if _, err := pool.Exec(ctx,
		`UPDATE public.audit_log SET created_at = created_at - interval '10 days'
		 WHERE project_id = $1`,
		projectID); err != nil {
		t.Skipf("cannot back-date head row: %v", err)
	}

	var deleted int64
	if err := pool.QueryRow(ctx, `SELECT public.prune_audit_log(1)`).Scan(&deleted); err != nil {
		t.Fatalf("prune_audit_log: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("prune must preserve the head row, but deleted %d", deleted)
	}

	var remaining int
	_ = pool.QueryRow(ctx,
		`SELECT count(*) FROM public.audit_log WHERE project_id = $1`,
		projectID).Scan(&remaining)
	if remaining != 1 {
		t.Errorf("head row missing after prune; remaining = %d", remaining)
	}
}

// TestRetention_EnsureFuturePartitionsIdempotent confirms the rolling
// pre-create is safe to call repeatedly. Migration 000066 already pre-
// creates 12 months, so a fresh DB has at least that many — calling the
// helper again should not throw and should not produce duplicates.
func TestRetention_EnsureFuturePartitionsIdempotent(t *testing.T) {
	_, _, pool := setupAuditTest(t)
	ctx := context.Background()

	countPartitions := func() int {
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM pg_inherits i
			   JOIN pg_class c ON c.oid = i.inhrelid
			   JOIN pg_class p ON p.oid = i.inhparent
			  WHERE p.relname = 'data_access_log'
			    AND c.relname ~ '^data_access_log_[0-9]{6}$'`).Scan(&n); err != nil {
			t.Fatalf("count partitions: %v", err)
		}
		return n
	}

	before := countPartitions()

	// First call — adds up to 12 months ahead (most may already exist).
	var ensured int
	if err := pool.QueryRow(ctx,
		`SELECT public.ensure_future_data_access_log_partitions(12)`).Scan(&ensured); err != nil {
		t.Fatalf("ensure (first): %v", err)
	}
	mid := countPartitions()

	// Second call — must be a no-op.
	if err := pool.QueryRow(ctx,
		`SELECT public.ensure_future_data_access_log_partitions(12)`).Scan(&ensured); err != nil {
		t.Fatalf("ensure (second): %v", err)
	}
	after := countPartitions()

	if after != mid {
		t.Errorf("ensure not idempotent: %d → %d → %d", before, mid, after)
	}
}

// TestRetention_RunDefaults runs the Go wrapper with the shipped
// defaults and asserts the call shape: ensure runs (positive), drop is
// safe (no partitions are old enough to drop on a fresh DB), and audit
// prune is skipped (defaults disable it).
func TestRetention_RunDefaults(t *testing.T) {
	_, _, pool := setupAuditTest(t)
	ctx := context.Background()

	svc := NewRetentionService(pool)
	res, err := svc.Run(ctx, DefaultRetentionConfig())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.DataAccessPartitionsEnsured == 0 {
		t.Error("ensure step did not run with defaults")
	}
	if res.AuditLogRowsDeleted != 0 {
		t.Errorf("default config should not prune audit_log, got %d deleted", res.AuditLogRowsDeleted)
	}
}
