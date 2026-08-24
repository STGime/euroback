package tenant

// Backup + restore HTTP handlers (Team-tier M3).
//
// All routes are Legal-Team-aware only by inheritance — the actual
// gate is plans.CheckDedicatedDB, which allows any tier with
// `dedicated_db = true` in plan_limits (Team + Legal-Team today).
// M2b's Legal-Team-specific surface lives elsewhere (retention_holds,
// gobd_export); backups are baseline dedicated-DB functionality.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/dbprovider"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/eurobase/euroback/internal/plans"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// BackupSnapshot mirrors a row in public.backup_snapshots — the
// cached view of a provider-side backup.
type BackupSnapshot struct {
	ID                 string    `json:"id"`
	ProjectID          string    `json:"project_id"`
	ProjectDatabaseID  string    `json:"project_database_id"`
	ProviderSnapshotID string    `json:"provider_snapshot_id"`
	Name               string    `json:"name"`
	SizeMB             int64     `json:"size_mb"`
	Kind               string    `json:"kind"`     // 'scheduled' | 'ondemand'
	CreatedAt          time.Time `json:"created_at"`
	ExpiresAt          time.Time `json:"expires_at"`
}

// RestoreOperation mirrors a row in public.restore_operations.
type RestoreOperation struct {
	ID              string     `json:"id"`
	ProjectID       string     `json:"project_id"`
	Kind            string     `json:"kind"`
	SourceRef       string     `json:"source_ref"`
	TargetTime      *time.Time `json:"target_time,omitempty"`
	State           string     `json:"state"`
	NewInstanceID   *string    `json:"new_instance_id,omitempty"`
	OldInstanceID   string     `json:"old_instance_id"`
	Error           *string    `json:"error,omitempty"`
	RequestedBy     *string    `json:"requested_by,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
}

// BackupService owns the state around backup/restore requests.
// Combines the cache-facing DB, the dbprovider registry (for
// on-demand snapshots + restore worker input), and the River client
// (for enqueuing restore jobs).
type BackupService struct {
	pool     *pgxpool.Pool
	registry *dbprovider.Registry
	repo     *dbprovider.Repo
	river    *river.Client[pgx.Tx]
	limits   *plans.LimitsService
}

func NewBackupService(pool *pgxpool.Pool, registry *dbprovider.Registry, limits *plans.LimitsService) *BackupService {
	// Insert-only River client (workers are in the worker pod).
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		slog.Error("backup service: could not create river client", "error", err)
	}
	return &BackupService{
		pool:     pool,
		registry: registry,
		repo:     dbprovider.NewRepo(pool),
		river:    client,
		limits:   limits,
	}
}

// ── HTTP handlers ────────────────────────────────────────────────

// HandleListBackups — GET /platform/projects/{id}/backups.
// Reads the cache; refresh-from-provider happens via the cron.
// Gated on CheckDedicatedDB → 402 for Free/Pro.
func (s *BackupService) HandleListBackups() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if projectID == "" {
			http.Error(w, `{"error":"project id required"}`, http.StatusBadRequest)
			return
		}
		if err := s.limits.CheckDedicatedDB(r.Context(), projectID); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q,"code":"dedicated_db_required"}`, err.Error()), http.StatusPaymentRequired)
			return
		}

		rows, err := s.pool.Query(r.Context(),
			`SELECT id, project_id, project_database_id, provider_snapshot_id,
			        name, size_mb, kind, created_at, expires_at
			   FROM public.backup_snapshots
			  WHERE project_id = $1::uuid
			    AND expires_at > now()
			  ORDER BY created_at DESC
			  LIMIT 500`,
			projectID)
		if err != nil {
			slog.Error("list backups query failed", "error", err, "project_id", projectID)
			http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		out := make([]BackupSnapshot, 0)
		for rows.Next() {
			var b BackupSnapshot
			if err := rows.Scan(&b.ID, &b.ProjectID, &b.ProjectDatabaseID, &b.ProviderSnapshotID,
				&b.Name, &b.SizeMB, &b.Kind, &b.CreatedAt, &b.ExpiresAt); err != nil {
				http.Error(w, `{"error":"scan failed"}`, http.StatusInternalServerError)
				return
			}
			out = append(out, b)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"backups": out, "total": len(out)})
	}
}

// onDemandBackupRateLimit — 5 on-demand backups per project per day.
// Matches the plan doc. The count query filters on
// (kind='ondemand', created_at > now() - '24h').
const onDemandBackupRateLimit = 5

// HandleCreateBackup — POST /platform/projects/{id}/backups.
// Triggers an on-demand snapshot. Rate-limited to 5/day/project.
// Runs synchronously (provider returns fast; the snapshot itself
// takes minutes but the row is created immediately).
func (s *BackupService) HandleCreateBackup() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if projectID == "" {
			http.Error(w, `{"error":"project id required"}`, http.StatusBadRequest)
			return
		}
		if err := s.limits.CheckDedicatedDB(r.Context(), projectID); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q,"code":"dedicated_db_required"}`, err.Error()), http.StatusPaymentRequired)
			return
		}

		// Rate limit — on-demand snapshots count towards the daily
		// budget (scheduled ones don't).
		var dailyCount int
		if err := s.pool.QueryRow(r.Context(),
			`SELECT count(*) FROM public.backup_snapshots
			  WHERE project_id = $1::uuid
			    AND kind = 'ondemand'
			    AND created_at > now() - interval '24 hours'`,
			projectID,
		).Scan(&dailyCount); err != nil {
			http.Error(w, `{"error":"rate check failed"}`, http.StatusInternalServerError)
			return
		}
		if dailyCount >= onDemandBackupRateLimit {
			http.Error(w, fmt.Sprintf(`{"error":"on-demand backup limit of %d per 24h reached","code":"rate_limited","retry_after_seconds":86400}`, onDemandBackupRateLimit),
				http.StatusTooManyRequests)
			return
		}

		// Look up the live project_databases row.
		rec, err := s.repo.GetLiveByProject(r.Context(), projectID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				http.Error(w, `{"error":"project has no active dedicated database"}`, http.StatusConflict)
				return
			}
			slog.Error("get live db for backup failed", "error", err, "project_id", projectID)
			http.Error(w, `{"error":"lookup failed"}`, http.StatusInternalServerError)
			return
		}

		provider, err := s.registry.Get(rec.Provider)
		if err != nil {
			slog.Error("backup: provider not registered", "provider", rec.Provider)
			http.Error(w, `{"error":"provider not available"}`, http.StatusInternalServerError)
			return
		}

		// Retention comes from plan_limits.backup_retention_days —
		// 30 days for Team today. WITHOUT this the Scaleway backup
		// has no expires_at and accumulates indefinitely: 5/day on-
		// demand cap × 365 days = up to 1,825 permanent backups per
		// project × DB size × storage cost. See issue background in
		// the deprovision-sweeper PR (#455).
		limits, err := s.limits.GetProjectLimits(r.Context(), projectID)
		if err != nil {
			slog.Error("backup: limits lookup failed", "error", err, "project_id", projectID)
			http.Error(w, `{"error":"limits lookup failed"}`, http.StatusInternalServerError)
			return
		}
		// Defence-in-depth against the exact regression this PR is
		// closing: plan_limits.backup_retention_days DEFAULTs to 0
		// (see migration 000085) and Team/Legal-Team explicitly set
		// 30. If a future dedicated-DB plan (or an edited row) ever
		// leaves the default 0, the Scaleway request omits expires_at
		// and on-demand backups pile up forever. That's the specific
		// hazard this whole PR exists to eliminate, so refuse the
		// backup here rather than let a config regression silently
		// re-open the leak. Loud slog so ops sees it in seconds.
		if limits.BackupRetentionDays <= 0 {
			slog.Error("backup: refusing to snapshot with zero retention — plan_limits.backup_retention_days must be > 0 for any dedicated-DB plan",
				"project_id", projectID,
				"plan_backup_retention_days", limits.BackupRetentionDays)
			http.Error(w, `{"error":"backup retention not configured for this plan — contact support","code":"backup_retention_not_configured"}`,
				http.StatusInternalServerError)
			return
		}
		retention := time.Duration(limits.BackupRetentionDays) * 24 * time.Hour

		snap, err := provider.Snapshot(r.Context(), rec.ProviderInstanceID, dbprovider.SnapshotOpts{
			Retention: retention,
		})
		if err != nil {
			slog.Error("backup: provider snapshot failed", "error", err, "project_id", projectID)
			http.Error(w, fmt.Sprintf(`{"error":"provider snapshot failed: %v"}`, err), http.StatusBadGateway)
			return
		}

		// Cache the row so it appears in list-endpoint output immediately.
		var out BackupSnapshot
		err = s.pool.QueryRow(r.Context(),
			`INSERT INTO public.backup_snapshots
			    (project_id, project_database_id, provider_snapshot_id, name, size_mb, kind, created_at, expires_at)
			 VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8)
			 RETURNING id, project_id, project_database_id, provider_snapshot_id, name, size_mb, kind, created_at, expires_at`,
			projectID, rec.ID, snap.ProviderID, snap.Name, snap.SizeMB, string(snap.Kind), snap.CreatedAt, snap.ExpiresAt,
		).Scan(&out.ID, &out.ProjectID, &out.ProjectDatabaseID, &out.ProviderSnapshotID,
			&out.Name, &out.SizeMB, &out.Kind, &out.CreatedAt, &out.ExpiresAt)
		if err != nil {
			slog.Error("cache new backup row failed", "error", err, "project_id", projectID)
			http.Error(w, `{"error":"snapshot created at provider but cache write failed — refresh to see it"}`, http.StatusInternalServerError)
			return
		}

		writeBackupAudit(r, projectID, audit.ActionExportRequested, map[string]any{
			"kind":                 "on_demand_backup",
			"provider_snapshot_id": snap.ProviderID,
		})

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(out)
	}
}

// RestoreRequest is the JSON body for POST /restore. Exactly one of
// `snapshot_id` or `target_time` must be set (validated at handler
// time; Provider.Restore also validates via RestoreSource.Valid()).
type RestoreRequest struct {
	Source     string     `json:"source"` // "snapshot" | "pitr"
	SnapshotID string     `json:"snapshot_id,omitempty"`
	TargetTime *time.Time `json:"target_time,omitempty"`
}

// HandleCreateRestore — POST /platform/projects/{id}/restore.
// Validates the request, inserts a restore_operations row in
// state='pending', and enqueues a River job. Returns 202 with the
// restore-op ID so the client can start polling immediately.
//
// Gated on CheckDedicatedDB. The unique partial index on
// restore_operations enforces one live restore per project — a
// concurrent restore attempt gets 409.
func (s *BackupService) HandleCreateRestore() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if projectID == "" {
			http.Error(w, `{"error":"project id required"}`, http.StatusBadRequest)
			return
		}
		if err := s.limits.CheckDedicatedDB(r.Context(), projectID); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q,"code":"dedicated_db_required"}`, err.Error()), http.StatusPaymentRequired)
			return
		}

		// Monthly restore quota (migration 000108). Enforced BEFORE
		// PITR-window validation so a quota-exhausted user gets a
		// clean 402 with the reset time, rather than a 400 about
		// their target_time that's actually rejected by quota anyway.
		//
		// The count query mirrors the same shape used by the
		// restore-quota endpoint below — kept inline (not extracted
		// yet) because it's the only two callers and a helper would
		// obscure the WHERE clauses that matter.
		if err := s.enforceRestoreQuota(r.Context(), w, projectID); err != nil {
			// enforceRestoreQuota has already written the response.
			return
		}

		var req RestoreRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
			return
		}
		// Validate source.
		hasSnap := req.SnapshotID != ""
		hasPITR := req.TargetTime != nil && !req.TargetTime.IsZero()
		if hasSnap == hasPITR {
			http.Error(w, `{"error":"exactly one of snapshot_id or target_time must be set"}`, http.StatusBadRequest)
			return
		}
		if hasPITR && req.Source != "pitr" {
			req.Source = "pitr"
		}
		if hasSnap && req.Source != "snapshot" {
			req.Source = "snapshot"
		}

		// Look up the live project_databases row — this is the
		// "old" instance the restore replaces.
		oldRec, err := s.repo.GetLiveByProject(r.Context(), projectID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				http.Error(w, `{"error":"project has no active dedicated database"}`, http.StatusConflict)
				return
			}
			slog.Error("get live db for restore failed", "error", err, "project_id", projectID)
			http.Error(w, `{"error":"lookup failed"}`, http.StatusInternalServerError)
			return
		}

		// PITR window check — must fall within the plan's pitr_days.
		if hasPITR {
			limits, err := s.limits.GetProjectLimits(r.Context(), projectID)
			if err != nil {
				http.Error(w, `{"error":"limits lookup failed"}`, http.StatusInternalServerError)
				return
			}
			window := time.Duration(limits.PITRDays) * 24 * time.Hour
			if window <= 0 {
				http.Error(w, `{"error":"pitr not available on this plan","code":"pitr_disabled"}`, http.StatusPaymentRequired)
				return
			}
			earliest := time.Now().Add(-window)
			if req.TargetTime.Before(earliest) || req.TargetTime.After(time.Now()) {
				http.Error(w, fmt.Sprintf(`{"error":"target_time must be within the last %d days","code":"pitr_out_of_window"}`, limits.PITRDays), http.StatusBadRequest)
				return
			}
		}

		// Resolve source_ref.
		//
		// Snapshot restores: SECURITY — the client-supplied
		// SnapshotID is untrusted. Scaleway addresses backups by
		// GLOBAL ID (not per-instance), so passing the raw client
		// value to Provider.Restore would let any Team admin
		// restore ANOTHER tenant's snapshot into their own project
		// (M3 review blocker #1). Look up through the project-
		// scoped backup_snapshots cache and use only the verified
		// provider_snapshot_id. 404 for anything not owned by this
		// project (or expired). Accepts either our internal cache
		// row ID OR the provider_snapshot_id — both are per-project
		// unique.
		//
		// PITR restores: source_ref is the target timestamp; the
		// PITR API path in the Scaleway provider hits
		// /instances/{oldInstanceID}/renew-pitr, so the source
		// instance is enforced by the URL — no cross-tenant vector.
		var sourceRef string
		if hasSnap {
			var verifiedProviderID string
			err := s.pool.QueryRow(r.Context(),
				`SELECT provider_snapshot_id
				   FROM public.backup_snapshots
				  WHERE (id::text = $1 OR provider_snapshot_id = $1)
				    AND project_id = $2::uuid
				    AND expires_at > now()`,
				req.SnapshotID, projectID,
			).Scan(&verifiedProviderID)
			if errors.Is(err, pgx.ErrNoRows) {
				http.Error(w, `{"error":"snapshot not found for this project","code":"snapshot_not_found"}`, http.StatusNotFound)
				return
			}
			if err != nil {
				slog.Error("resolve snapshot for restore failed", "error", err, "project_id", projectID)
				http.Error(w, `{"error":"snapshot lookup failed"}`, http.StatusInternalServerError)
				return
			}
			sourceRef = verifiedProviderID
		} else {
			sourceRef = req.TargetTime.Format(time.RFC3339Nano)
		}

		var actorID string
		if claims, ok := auth.ClaimsFromContext(r.Context()); ok && claims != nil {
			actorID = claims.Subject
		}

		// Insert the pending restore row. The unique partial index
		// on (project_id) WHERE state IN (pending/provisioning/
		// verifying/cutover) catches races.
		var restoreID string
		err = s.pool.QueryRow(r.Context(),
			`INSERT INTO public.restore_operations
			    (project_id, kind, source_ref, target_time,
			     old_instance_id, requested_by)
			 VALUES ($1::uuid, $2, $3, $4, $5::uuid, NULLIF($6, '')::uuid)
			 RETURNING id`,
			projectID, req.Source, sourceRef, req.TargetTime,
			oldRec.ID, actorID,
		).Scan(&restoreID)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				http.Error(w, `{"error":"a restore is already in progress for this project","code":"restore_in_flight"}`, http.StatusConflict)
				return
			}
			slog.Error("insert restore_operations failed", "error", err, "project_id", projectID)
			http.Error(w, `{"error":"insert failed"}`, http.StatusInternalServerError)
			return
		}

		// Enqueue the restore worker.
		if s.river != nil {
			if _, err := s.river.Insert(r.Context(), jobs.RestoreTeamDatabaseArgs{
				RestoreOperationID: restoreID,
			}, nil); err != nil {
				// Don't fail the request — the restore row is
				// there; ops can re-enqueue if needed.
				slog.Error("enqueue restore worker failed", "error", err, "restore_id", restoreID)
			}
		}

		writeBackupAudit(r, projectID, audit.ActionExportRequested, map[string]any{
			"kind":              "restore",
			"restore_operation": restoreID,
			"source":            req.Source,
			"source_ref":        sourceRef,
		})

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"restore_id": restoreID,
			"state":      "pending",
		})
	}
}

// HandleGetRestore — GET /platform/projects/{id}/restore/{restore_id}.
// Poll endpoint the console hits every ~5s while a restore runs.
func (s *BackupService) HandleGetRestore() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		restoreID := chi.URLParam(r, "restoreId")
		if projectID == "" || restoreID == "" {
			http.Error(w, `{"error":"project id and restore id required"}`, http.StatusBadRequest)
			return
		}
		// CheckDedicatedDB not strictly needed here (read-only status
		// endpoint) but consistent with the other routes.
		if err := s.limits.CheckDedicatedDB(r.Context(), projectID); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q,"code":"dedicated_db_required"}`, err.Error()), http.StatusPaymentRequired)
			return
		}

		var op RestoreOperation
		err := s.pool.QueryRow(r.Context(),
			`SELECT id, project_id, kind, source_ref, target_time, state,
			        new_instance_id, old_instance_id, error, requested_by,
			        created_at, completed_at
			   FROM public.restore_operations
			  WHERE id = $1::uuid AND project_id = $2::uuid`,
			restoreID, projectID,
		).Scan(&op.ID, &op.ProjectID, &op.Kind, &op.SourceRef, &op.TargetTime, &op.State,
			&op.NewInstanceID, &op.OldInstanceID, &op.Error, &op.RequestedBy,
			&op.CreatedAt, &op.CompletedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, `{"error":"restore operation not found"}`, http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(op)
	}
}

// writeBackupAudit is a tiny helper — audit action + metadata for
// each backup/restore-lifecycle write.
func writeBackupAudit(r *http.Request, projectID, action string, metadata map[string]any) {
	svc := audit.FromContext(r.Context())
	if svc == nil {
		return
	}
	actorID, actorEmail := audit.ActorFromContext(r.Context())
	svc.Log(r.Context(), projectID, actorID, actorEmail, action,
		audit.WithMetadata(metadata),
		audit.WithIP(r.RemoteAddr))
}

// RestoreQuota is the shape returned by GET /platform/projects/{id}/
// restore-quota AND used internally by enforceRestoreQuota to build
// the 402 response body. Kept in one type so console + handler can't
// drift.
type RestoreQuota struct {
	Included  int       `json:"included"`
	Used      int       `json:"used"`
	ResetsAt  time.Time `json:"resets_at"`
	Exhausted bool      `json:"exhausted"`
}

// countMonthlyRestores returns the number of restore_operations
// created for `projectID` since the start of the current calendar
// month, excluding terminal-failure states so a broken restore
// attempt doesn't count against the user's quota. See migration
// 000108 for the design rationale — this is the single source of
// truth for restore-quota counting.
func (s *BackupService) countMonthlyRestores(ctx context.Context, projectID string) (int, error) {
	var used int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM public.restore_operations
		  WHERE project_id = $1::uuid
		    AND created_at >= date_trunc('month', now())
		    AND state <> 'failed'`,
		projectID,
	).Scan(&used)
	return used, err
}

// enforceRestoreQuota resolves the project's plan limit, counts
// this month's non-failed restores, and 402s if the quota is
// exhausted. Returns a non-nil error ONLY when the caller must
// stop processing (which it should because a response was already
// written). Returns nil when the quota check passed OR failed
// open (see comment below on error posture).
func (s *BackupService) enforceRestoreQuota(ctx context.Context, w http.ResponseWriter, projectID string) error {
	limits, err := s.limits.GetProjectLimits(ctx, projectID)
	if err != nil {
		// Fail open: if we can't resolve limits, let the restore
		// proceed rather than blocking a user for a transient DB
		// blip. The subsequent handler steps will fail cleanly if
		// the project is really broken. Matches the shape used by
		// other CheckX helpers that swallow transient errors.
		slog.Warn("restore quota: plan limits lookup failed — allowing restore",
			"project_id", projectID, "error", err)
		return nil
	}
	if limits.IncludedRestoresPerMonth <= 0 {
		// Feature disabled for this plan (Free/Pro have no restore
		// surface at all — CheckDedicatedDB should have already
		// blocked). Defensive.
		http.Error(w, `{"error":"restore not available on this plan","code":"dedicated_db_required"}`, http.StatusPaymentRequired)
		return fmt.Errorf("restore quota check: plan has no restore surface")
	}
	used, err := s.countMonthlyRestores(ctx, projectID)
	if err != nil {
		slog.Warn("restore quota: count failed — allowing restore",
			"project_id", projectID, "error", err)
		return nil
	}
	if used < limits.IncludedRestoresPerMonth {
		return nil
	}
	// Exhausted. Compute reset time = first day of next month, UTC.
	// Keeping it UTC (not the tenant's local) so the response is
	// unambiguous and matches how the count query filters
	// (date_trunc respects the session TZ, which for platform pool
	// is UTC).
	now := time.Now().UTC()
	resetsAt := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	body, _ := json.Marshal(map[string]any{
		"error":     fmt.Sprintf("restore quota exceeded (%d/%d this month) — contact support for additional restores", used, limits.IncludedRestoresPerMonth),
		"code":      "restore_quota_exceeded",
		"included":  limits.IncludedRestoresPerMonth,
		"used":      used,
		"resets_at": resetsAt.Format(time.RFC3339),
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPaymentRequired)
	_, _ = w.Write(body)
	return fmt.Errorf("restore quota exceeded")
}

// HandleGetRestoreQuota — GET /platform/projects/{id}/restore-quota.
// Cheap read the console polls on the Backups tab to render the
// "0 of 1 monthly restores used" badge. Same auth + plan gate as
// the other backup routes.
func (s *BackupService) HandleGetRestoreQuota() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if projectID == "" {
			http.Error(w, `{"error":"project id required"}`, http.StatusBadRequest)
			return
		}
		if err := s.limits.CheckDedicatedDB(r.Context(), projectID); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q,"code":"dedicated_db_required"}`, err.Error()), http.StatusPaymentRequired)
			return
		}
		limits, err := s.limits.GetProjectLimits(r.Context(), projectID)
		if err != nil {
			http.Error(w, `{"error":"limits lookup failed"}`, http.StatusInternalServerError)
			return
		}
		used, err := s.countMonthlyRestores(r.Context(), projectID)
		if err != nil {
			http.Error(w, `{"error":"count failed"}`, http.StatusInternalServerError)
			return
		}
		now := time.Now().UTC()
		resetsAt := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		q := RestoreQuota{
			Included:  limits.IncludedRestoresPerMonth,
			Used:      used,
			ResetsAt:  resetsAt,
			Exhausted: used >= limits.IncludedRestoresPerMonth,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(q)
	}
}

