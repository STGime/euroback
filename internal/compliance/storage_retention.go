package compliance

// Storage retention policies (Legal-Team M2b, issue #330).
//
// Per-prefix WORM retention for storage objects. A Legal-Team tenant
// declares "objects at prefix P retain for N years under basis B"
// and the storage upload path resolves the longest-matching prefix
// to a *storage.Retention, which gets baked into the S3 PUT via
// x-amz-object-lock-* headers. Scaleway (Object Lock enabled at
// bucket-create time) then enforces WORM at rest — delete attempts
// before the retain-until date return 403 AccessDenied, which the
// storage handler translates to HTTP 409 object_locked.
//
// Row-level erasure protection lives in retention_holds.go
// (target_type='object' one-off holds). This file covers the
// tenant-wide default: every upload under /invoices/* is retained
// for 10y under §257 HGB, no matter who uploaded it or when.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/plans"
	"github.com/eurobase/euroback/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StorageRetentionPolicy is the persisted view of a
// storage_retention_policies row.
type StorageRetentionPolicy struct {
	ID             string    `json:"id"`
	ProjectID      string    `json:"project_id"`
	Prefix         string    `json:"prefix"`
	Mode           string    `json:"mode"` // "compliance" | "governance"
	RetentionYears int       `json:"retention_years"`
	LegalBasis     string    `json:"legal_basis"`
	CreatedBy      *string   `json:"created_by,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// StorageRetentionService is the DB-facing side of
// storage_retention_policies + the upload-time resolver.
type StorageRetentionService struct {
	pool *pgxpool.Pool
}

func NewStorageRetentionService(pool *pgxpool.Pool) *StorageRetentionService {
	return &StorageRetentionService{pool: pool}
}

// Upsert inserts or updates a per-prefix policy. Editing a prefix is
// an UPDATE rather than DELETE+INSERT so the created_at + created_by
// history stays intact across policy tweaks.
func (s *StorageRetentionService) Upsert(ctx context.Context, projectID, prefix, mode string, retentionYears int, legalBasis, createdBy string) (*StorageRetentionPolicy, error) {
	if mode != string(storage.RetentionCompliance) && mode != string(storage.RetentionGovernance) &&
		mode != "compliance" && mode != "governance" {
		return nil, errors.New("mode must be compliance or governance")
	}
	if retentionYears <= 0 || retentionYears > 100 {
		return nil, errors.New("retention_years must be 1..100")
	}
	if strings.TrimSpace(legalBasis) == "" {
		return nil, errors.New("legal_basis is required")
	}
	// Normalise mode to lower-case for the DB CHECK constraint.
	mode = strings.ToLower(mode)

	var p StorageRetentionPolicy
	err := s.pool.QueryRow(ctx,
		`INSERT INTO public.storage_retention_policies
		    (project_id, prefix, mode, retention_years, legal_basis, created_by)
		 VALUES ($1::uuid, $2, $3, $4, $5, NULLIF($6, '')::uuid)
		 ON CONFLICT (project_id, prefix) DO UPDATE
		     SET mode = EXCLUDED.mode,
		         retention_years = EXCLUDED.retention_years,
		         legal_basis = EXCLUDED.legal_basis,
		         updated_at = now()
		 RETURNING id, project_id, prefix, mode, retention_years, legal_basis, created_by, created_at, updated_at`,
		projectID, prefix, mode, retentionYears, legalBasis, createdBy,
	).Scan(&p.ID, &p.ProjectID, &p.Prefix, &p.Mode, &p.RetentionYears, &p.LegalBasis, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("upsert storage retention policy: %w", err)
	}
	return &p, nil
}

// Remove drops a policy by (project_id, prefix). Not-found is not an
// error — an operator removing an already-removed policy shouldn't
// see a failure.
func (s *StorageRetentionService) Remove(ctx context.Context, projectID, prefix string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM public.storage_retention_policies WHERE project_id = $1::uuid AND prefix = $2`,
		projectID, prefix,
	)
	if err != nil {
		return fmt.Errorf("remove storage retention policy: %w", err)
	}
	return nil
}

// List returns every policy for a project, ordered by prefix ASC so
// callers get a deterministic view. Small table (dozens per tenant).
func (s *StorageRetentionService) List(ctx context.Context, projectID string) ([]StorageRetentionPolicy, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, project_id, prefix, mode, retention_years, legal_basis, created_by, created_at, updated_at
		   FROM public.storage_retention_policies
		  WHERE project_id = $1::uuid
		  ORDER BY prefix ASC
		  LIMIT 500`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("list storage retention policies: %w", err)
	}
	defer rows.Close()
	out := make([]StorageRetentionPolicy, 0)
	for rows.Next() {
		var p StorageRetentionPolicy
		if err := rows.Scan(&p.ID, &p.ProjectID, &p.Prefix, &p.Mode, &p.RetentionYears, &p.LegalBasis, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan storage retention policy: %w", err)
		}
		out = append(out, p)
	}
	return out, nil
}

// Resolve returns the storage.Retention to apply for uploading `key`
// in `projectID`, or a zero-value Retention if no policy matches.
// Longest-prefix-wins: if both "" and "/invoices/" match, the more
// specific "/invoices/" prefix takes effect.
//
// Runs one query per upload — the table is small enough that this is
// fine for now. If the volume ever justifies it, cache List() output
// per project with a TTL similar to LimitsService.
func (s *StorageRetentionService) Resolve(ctx context.Context, projectID, key string) (storage.Retention, error) {
	policies, err := s.List(ctx, projectID)
	if err != nil {
		return storage.Retention{}, err
	}
	return resolveLongestPrefix(policies, key), nil
}

// resolveLongestPrefix is the pure logic: given a policy list and an
// object key, return the storage.Retention derived from the longest
// prefix that matches. Split out for unit testing without a DB.
func resolveLongestPrefix(policies []StorageRetentionPolicy, key string) storage.Retention {
	var best *StorageRetentionPolicy
	for i := range policies {
		p := &policies[i]
		if !strings.HasPrefix(key, p.Prefix) {
			continue
		}
		if best == nil || len(p.Prefix) > len(best.Prefix) {
			best = p
		}
	}
	if best == nil {
		return storage.Retention{}
	}
	return storage.Retention{
		Mode:        storage.RetentionMode(strings.ToUpper(best.Mode)),
		RetainUntil: time.Now().UTC().AddDate(best.RetentionYears, 0, 0),
	}
}

// ── HTTP handlers ─────────────────────────────────────────────────

// UpsertStoragePolicyRequest is the JSON body for POST
// /platform/projects/{id}/compliance/storage-retention-policies.
type UpsertStoragePolicyRequest struct {
	Prefix         string `json:"prefix"`
	Mode           string `json:"mode"`
	RetentionYears int    `json:"retention_years"`
	LegalBasis     string `json:"legal_basis"`
}

// HandleUpsertStorageRetentionPolicy — POST /platform/projects/{id}/compliance/storage-retention-policies.
// Gated by CheckLegalTeamTier.
func HandleUpsertStorageRetentionPolicy(pool *pgxpool.Pool, limits *plans.LimitsService, svc *StorageRetentionService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if projectID == "" {
			http.Error(w, `{"error":"project id required"}`, http.StatusBadRequest)
			return
		}
		if err := limits.CheckLegalTeamTier(r.Context(), projectID); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q,"code":"legal_team_required"}`, err.Error()), http.StatusPaymentRequired)
			return
		}
		var req UpsertStoragePolicyRequest
		if err := decodeJSON(r, &req); err != nil {
			http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
			return
		}

		var actorID string
		if claims, ok := auth.ClaimsFromContext(r.Context()); ok && claims != nil {
			actorID = claims.Subject
		}

		p, err := svc.Upsert(r.Context(), projectID, req.Prefix, req.Mode, req.RetentionYears, req.LegalBasis, actorID)
		if err != nil {
			slog.Error("upsert storage retention policy failed", "error", err, "project_id", projectID)
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}

		if a := audit.FromContext(r.Context()); a != nil {
			aid, aemail := audit.ActorFromContext(r.Context())
			a.Log(r.Context(), projectID, aid, aemail, audit.ActionStorageRetentionPolicySet,
				audit.WithTarget("storage_retention_policy", p.ID),
				audit.WithMetadata(map[string]any{
					"prefix":          req.Prefix,
					"mode":            req.Mode,
					"retention_years": req.RetentionYears,
					"legal_basis":     req.LegalBasis,
				}),
				audit.WithIP(r.RemoteAddr))
		}

		writeJSON(w, http.StatusOK, p)
	}
}

// HandleListStorageRetentionPolicies — GET /platform/projects/{id}/compliance/storage-retention-policies
func HandleListStorageRetentionPolicies(pool *pgxpool.Pool, limits *plans.LimitsService, svc *StorageRetentionService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if projectID == "" {
			http.Error(w, `{"error":"project id required"}`, http.StatusBadRequest)
			return
		}
		if err := limits.CheckLegalTeamTier(r.Context(), projectID); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q,"code":"legal_team_required"}`, err.Error()), http.StatusPaymentRequired)
			return
		}
		items, err := svc.List(r.Context(), projectID)
		if err != nil {
			slog.Error("list storage retention policies failed", "error", err, "project_id", projectID)
			http.Error(w, `{"error":"list failed"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"policies": items, "total": len(items)})
	}
}

// HandleRemoveStorageRetentionPolicy — DELETE /platform/projects/{id}/compliance/storage-retention-policies?prefix=...
// Using ?prefix= rather than a path param because prefixes contain
// slashes and would be awkward to URL-encode into a path segment.
func HandleRemoveStorageRetentionPolicy(pool *pgxpool.Pool, limits *plans.LimitsService, svc *StorageRetentionService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		prefix := r.URL.Query().Get("prefix")
		if projectID == "" {
			http.Error(w, `{"error":"project id required"}`, http.StatusBadRequest)
			return
		}
		if err := limits.CheckLegalTeamTier(r.Context(), projectID); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q,"code":"legal_team_required"}`, err.Error()), http.StatusPaymentRequired)
			return
		}
		if err := svc.Remove(r.Context(), projectID, prefix); err != nil {
			slog.Error("remove storage retention policy failed", "error", err, "project_id", projectID, "prefix", prefix)
			http.Error(w, `{"error":"remove failed"}`, http.StatusInternalServerError)
			return
		}

		if a := audit.FromContext(r.Context()); a != nil {
			aid, aemail := audit.ActorFromContext(r.Context())
			a.Log(r.Context(), projectID, aid, aemail, audit.ActionStorageRetentionPolicyRemoved,
				audit.WithTarget("storage_retention_policy_prefix", prefix),
				audit.WithIP(r.RemoteAddr))
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

// ── Small local helpers ───────────────────────────────────────────

func decodeJSON(r *http.Request, out any) error {
	if r.Body == nil {
		return errors.New("no body")
	}
	return json.NewDecoder(r.Body).Decode(out)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
