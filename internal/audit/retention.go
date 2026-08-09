package audit

// Tier-1 GDPR #3 follow-up (#171): retention helpers for the two audit
// streams (`public.audit_log`, `public.data_access_log`). The actual SQL —
// pruning, partition drop, and rolling pre-create — lives in migrator-owned
// SECURITY DEFINER functions added in migration 000070. This file is the
// Go-side wrapper called by the periodic worker so the call sites read
// cleanly and the test surface is mockable.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RetentionConfig tunes the retention worker. Zero values mean "skip this
// task" — operators can disable individual streams without touching code.
type RetentionConfig struct {
	// AuditLogRetentionDays: fallback cutoff for the audit_log
	// pruner, used for projects whose plan_limits.audit_log_retention_days
	// is 0 (Free / Pro today) and for the global chain
	// (project_id IS NULL — platform-level events). Team / Legal-Team
	// projects use their plan's per-tier value (365 / 3650) regardless
	// of this fallback. 0 = never prune the fallback set. Pruned rows
	// land in public.audit_log_archive (000097) and are subsequently
	// dumped off-box to S3 Object Lock by the StartAuditArchiveExporter
	// worker (#170 object-dump), so this is a move, not a destruction.
	AuditLogRetentionDays int

	// DataAccessLogRetentionMonths: monthly partitions of
	// `public.data_access_log` whose covered month ends on or before
	// (today - this many months) are detached and dropped. 0 = never
	// drop partitions. Default 13 keeps ~1 year + 1 buffer month hot.
	DataAccessLogRetentionMonths int

	// FuturePartitionsMonthsAhead: how many future monthly partitions
	// to keep pre-created on every tick. 0 = don't pre-create (rows
	// would fall back to the DEFAULT partition). Default 12.
	FuturePartitionsMonthsAhead int
}

// DefaultRetentionConfig returns the shipped defaults. See the field
// comments above for the reasoning; the migration's header repeats the
// numbers so a future change shows up in both places.
func DefaultRetentionConfig() RetentionConfig {
	return RetentionConfig{
		AuditLogRetentionDays:        0,  // never prune audit_log in DB by default
		DataAccessLogRetentionMonths: 13, // 1 year + 1 buffer month
		FuturePartitionsMonthsAhead:  12, // keep a year of forward partitions
	}
}

// RetentionResult summarizes one pass of the retention worker. Useful for
// log lines and for the test suite to assert.
type RetentionResult struct {
	AuditLogRowsDeleted         int64
	AuditLogPerPlan             map[string]int64 // plan code → rows deleted this pass
	DataAccessPartitionsDropped []string
	DataAccessPartitionsEnsured int
}

// RetentionService is the Go wrapper around the SQL helpers added in
// migration 000070. Holds a pool so the worker can construct it once.
type RetentionService struct {
	pool *pgxpool.Pool
}

// NewRetentionService constructs the service.
func NewRetentionService(pool *pgxpool.Pool) *RetentionService {
	return &RetentionService{pool: pool}
}

// Run executes one full retention pass. The order matters:
//
//  1. Pre-create forward partitions BEFORE dropping old ones — if both
//     fail in some pathological way, we'd rather have extra partitions
//     than miss the next month's writes (they'd hit DEFAULT, recoverable).
//  2. Drop old partitions — pure delete; lock-light because each partition
//     is its own physical table.
//  3. Prune audit_log — heaviest, per-project; deferred to last so an
//     earlier failure doesn't block the cheaper housekeeping.
//
// Errors from any single step are returned wrapped; the worker logs and
// continues — retention is best-effort and idempotent, the next tick
// will catch up.
func (s *RetentionService) Run(ctx context.Context, cfg RetentionConfig) (*RetentionResult, error) {
	res := &RetentionResult{}

	if cfg.FuturePartitionsMonthsAhead > 0 {
		if err := s.pool.QueryRow(ctx,
			`SELECT public.ensure_future_data_access_log_partitions($1)`,
			cfg.FuturePartitionsMonthsAhead,
		).Scan(&res.DataAccessPartitionsEnsured); err != nil {
			return res, fmt.Errorf("ensure future partitions: %w", err)
		}
	}

	if cfg.DataAccessLogRetentionMonths > 0 {
		if err := s.pool.QueryRow(ctx,
			`SELECT public.drop_old_data_access_log_partitions($1)`,
			cfg.DataAccessLogRetentionMonths,
		).Scan(&res.DataAccessPartitionsDropped); err != nil {
			return res, fmt.Errorf("drop old partitions: %w", err)
		}
	}

	// Plan-aware pruning (#317). Runs unconditionally: Team gets
	// 365d and Legal-Team gets 3650d regardless of whether ops has
	// set the fallback env var. AuditLogRetentionDays only kicks in
	// for chains whose plan cap is 0 (Free / Pro / global). Setting
	// it to 0 is the operator's "leave the fallback set alone."
	rows, err := s.pool.Query(ctx,
		`SELECT o_plan, o_rows_deleted FROM public.prune_audit_log_by_plan($1)`,
		cfg.AuditLogRetentionDays,
	)
	if err != nil {
		return res, fmt.Errorf("prune audit_log by plan: %w", err)
	}
	res.AuditLogPerPlan = map[string]int64{}
	for rows.Next() {
		var plan string
		var n int64
		if err := rows.Scan(&plan, &n); err != nil {
			rows.Close()
			return res, fmt.Errorf("scan prune result: %w", err)
		}
		res.AuditLogPerPlan[plan] += n
		res.AuditLogRowsDeleted += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("iterate prune results: %w", err)
	}

	return res, nil
}
