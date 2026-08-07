package compliance

// Retention holds (M2b, hard blocker #3).
//
// Legal-Team tenants may place holds on specific rows / objects /
// tables to satisfy German statutory retention obligations that
// override Art 17 GDPR right-to-erasure:
//
//   * §50 BRAO — 6y lawyer file (Handakte) retention
//   * §257 HGB — 10y commercial records
//   * §147 AO  — 10y tax records
//
// When an erasure request lands, callers query IsHeld first and
// skip held items (and report them back to the user as "retained
// under <basis>, purgeable after <date>").

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
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TargetType discriminates what a hold covers. Kept as a small
// closed set — matches the CHECK constraint on
// public.retention_holds.target_type.
type TargetType string

const (
	TargetRow    TargetType = "row"    // one database row (target_ref = {"schema","table","pkey":{...}})
	TargetObject TargetType = "object" // one storage object (target_ref = {"bucket","key"})
	TargetTable  TargetType = "table"  // whole table blanket policy (target_ref = {"schema","table"})
)

// RetentionHold is the persisted view of a retention_holds row.
type RetentionHold struct {
	ID          string          `json:"id"`
	ProjectID   string          `json:"project_id"`
	TargetType  TargetType      `json:"target_type"`
	TargetRef   json.RawMessage `json:"target_ref"`
	LegalBasis  string          `json:"legal_basis"`
	ExpiresAt   time.Time       `json:"expires_at"`
	CreatedBy   *string         `json:"created_by,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
}

// HoldService is the DB-facing side of retention_holds. Kept small
// on purpose — the interesting shape is at the HTTP + integration
// layer, not the row CRUD.
type HoldService struct {
	pool *pgxpool.Pool
}

func NewHoldService(pool *pgxpool.Pool) *HoldService {
	return &HoldService{pool: pool}
}

// Place inserts a hold. Returns the generated ID + created_at.
// targetRef is any JSONB-serialisable value; enforcement lives at
// the query layer (target_type discriminates how targetRef is
// interpreted).
func (s *HoldService) Place(ctx context.Context, projectID string, targetType TargetType, targetRef any, legalBasis string, expiresAt time.Time, createdBy string) (*RetentionHold, error) {
	if legalBasis == "" {
		return nil, errors.New("legal_basis is required")
	}
	if expiresAt.Before(time.Now()) {
		return nil, errors.New("expires_at must be in the future")
	}
	targetRefJSON, err := json.Marshal(targetRef)
	if err != nil {
		return nil, fmt.Errorf("marshal target_ref: %w", err)
	}
	var h RetentionHold
	err = s.pool.QueryRow(ctx,
		`INSERT INTO public.retention_holds
		    (project_id, target_type, target_ref, legal_basis, expires_at, created_by)
		 VALUES ($1::uuid, $2, $3::jsonb, $4, $5, NULLIF($6, '')::uuid)
		 RETURNING id, project_id, target_type, target_ref, legal_basis, expires_at, created_by, created_at`,
		projectID, string(targetType), targetRefJSON, legalBasis, expiresAt, createdBy,
	).Scan(&h.ID, &h.ProjectID, &h.TargetType, &h.TargetRef, &h.LegalBasis, &h.ExpiresAt, &h.CreatedBy, &h.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert retention hold: %w", err)
	}
	return &h, nil
}

// Revoke deletes a hold by id. Returns nil (no error) on missing
// row — an operator revoking an already-expired hold should not
// see an error.
func (s *HoldService) Revoke(ctx context.Context, projectID, holdID string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM public.retention_holds WHERE id = $1::uuid AND project_id = $2::uuid`,
		holdID, projectID,
	)
	if err != nil {
		return fmt.Errorf("revoke retention hold: %w", err)
	}
	return nil
}

// List returns every active (non-expired) hold for a project.
// Ordered by expires_at ascending so the soonest-to-expire is first.
func (s *HoldService) List(ctx context.Context, projectID string) ([]RetentionHold, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, project_id, target_type, target_ref, legal_basis, expires_at, created_by, created_at
		   FROM public.retention_holds
		  WHERE project_id = $1::uuid
		    AND expires_at > now()
		  ORDER BY expires_at ASC
		  LIMIT 1000`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("list retention holds: %w", err)
	}
	defer rows.Close()
	out := make([]RetentionHold, 0)
	for rows.Next() {
		var h RetentionHold
		if err := rows.Scan(&h.ID, &h.ProjectID, &h.TargetType, &h.TargetRef, &h.LegalBasis, &h.ExpiresAt, &h.CreatedBy, &h.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan retention hold: %w", err)
		}
		out = append(out, h)
	}
	return out, nil
}

// IsHeld returns a matching hold (non-nil) if the given (targetType,
// targetRef) pair is currently under retention hold for the project.
// Callers on the erasure path invoke this once per candidate row /
// object and skip when non-nil.
//
// Matching semantics:
//   * Exact (target_type, target_ref) match wins.
//   * Additionally, a "table" hold matches any row/object below it
//     when target_ref specifies the same schema+table (row) or
//     bucket (object). Blanket policy support without needing to
//     backfill individual holds.
func (s *HoldService) IsHeld(ctx context.Context, projectID string, targetType TargetType, targetRef any) (*RetentionHold, error) {
	targetRefJSON, err := json.Marshal(targetRef)
	if err != nil {
		return nil, fmt.Errorf("marshal target_ref: %w", err)
	}
	// Two-part check: exact match on the specific target OR blanket
	// "table" hold whose target_ref is a subset of this target's ref
	// (JSONB @> containment).
	var h RetentionHold
	err = s.pool.QueryRow(ctx,
		`SELECT id, project_id, target_type, target_ref, legal_basis, expires_at, created_by, created_at
		   FROM public.retention_holds
		  WHERE project_id = $1::uuid
		    AND expires_at > now()
		    AND (
		         (target_type = $2 AND target_ref = $3::jsonb)
		      OR (target_type = 'table' AND $3::jsonb @> target_ref)
		    )
		  ORDER BY expires_at DESC
		  LIMIT 1`,
		projectID, string(targetType), targetRefJSON,
	).Scan(&h.ID, &h.ProjectID, &h.TargetType, &h.TargetRef, &h.LegalBasis, &h.ExpiresAt, &h.CreatedBy, &h.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("check retention hold: %w", err)
	}
	return &h, nil
}

// IsHeldObject is a storage-facing convenience over IsHeld that
// serialises {bucket, key} into the canonical object-hold target_ref
// shape and reports the retain-until. Returns (zero, false, nil) when
// no hold matches.
//
// Storage-facing interface (storage.HoldChecker) — kept here so the
// storage package doesn't need to depend on compliance's internal
// TargetType/RetentionHold shapes.
func (s *HoldService) IsHeldObject(ctx context.Context, projectID, bucket, key string) (time.Time, bool, error) {
	ref := map[string]any{"bucket": bucket, "key": key}
	h, err := s.IsHeld(ctx, projectID, TargetObject, ref)
	if err != nil {
		return time.Time{}, false, err
	}
	if h == nil {
		return time.Time{}, false, nil
	}
	return h.ExpiresAt, true, nil
}

// SweepExpired deletes holds past their expires_at. Called by a
// daily cron so held items become erasable promptly after the
// retention window closes without waiting for the next erasure
// request. Returns the number of holds swept.
func (s *HoldService) SweepExpired(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM public.retention_holds WHERE expires_at < now()`,
	)
	if err != nil {
		return 0, fmt.Errorf("sweep expired holds: %w", err)
	}
	if tag.RowsAffected() > 0 {
		slog.Info("retention holds swept", "count", tag.RowsAffected())
	}
	return tag.RowsAffected(), nil
}

// ── HTTP handlers ─────────────────────────────────────────────────

// PlaceHoldRequest is the JSON body for POST /platform/projects/{id}
// /compliance/retention-holds.
type PlaceHoldRequest struct {
	TargetType TargetType      `json:"target_type"`
	TargetRef  json.RawMessage `json:"target_ref"`
	LegalBasis string          `json:"legal_basis"`
	// ExpiresAt is the absolute wall-clock deadline. Alternative
	// forms (RetentionYears) can be added later without breaking
	// clients that already send ExpiresAt.
	ExpiresAt time.Time `json:"expires_at"`
}

// HandlePlaceRetentionHold — POST /platform/projects/{id}/compliance/retention-holds.
// Gated by CheckLegalTeamTier upstream.
func HandlePlaceRetentionHold(pool *pgxpool.Pool, limits *plans.LimitsService, svc *HoldService) http.HandlerFunc {
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
		var req PlaceHoldRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
			return
		}
		switch req.TargetType {
		case TargetRow, TargetObject, TargetTable:
			// ok
		default:
			http.Error(w, `{"error":"target_type must be one of row|object|table"}`, http.StatusBadRequest)
			return
		}
		if len(req.TargetRef) == 0 {
			http.Error(w, `{"error":"target_ref is required"}`, http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.LegalBasis) == "" {
			http.Error(w, `{"error":"legal_basis is required"}`, http.StatusBadRequest)
			return
		}

		var actorID string
		if claims, ok := auth.ClaimsFromContext(r.Context()); ok && claims != nil {
			actorID = claims.Subject
		}

		var targetRefAny any
		_ = json.Unmarshal(req.TargetRef, &targetRefAny)

		hold, err := svc.Place(r.Context(), projectID, req.TargetType, targetRefAny, req.LegalBasis, req.ExpiresAt, actorID)
		if err != nil {
			slog.Error("place retention hold failed", "error", err, "project_id", projectID)
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}

		if a := audit.FromContext(r.Context()); a != nil {
			aid, aemail := audit.ActorFromContext(r.Context())
			a.Log(r.Context(), projectID, aid, aemail, audit.ActionRetentionHoldPlaced,
				audit.WithTarget(string(req.TargetType), hold.ID),
				audit.WithMetadata(map[string]any{
					"legal_basis": req.LegalBasis,
					"expires_at":  req.ExpiresAt.Format(time.RFC3339),
				}),
				audit.WithIP(r.RemoteAddr))
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(hold)
	}
}

// HandleListRetentionHolds — GET /platform/projects/{id}/compliance/retention-holds
func HandleListRetentionHolds(pool *pgxpool.Pool, limits *plans.LimitsService, svc *HoldService) http.HandlerFunc {
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
		holds, err := svc.List(r.Context(), projectID)
		if err != nil {
			slog.Error("list retention holds failed", "error", err, "project_id", projectID)
			http.Error(w, `{"error":"list failed"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"holds": holds, "total": len(holds)})
	}
}

// HandleRevokeRetentionHold — DELETE /platform/projects/{id}/compliance/retention-holds/{holdId}
func HandleRevokeRetentionHold(pool *pgxpool.Pool, limits *plans.LimitsService, svc *HoldService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		holdID := chi.URLParam(r, "holdId")
		if projectID == "" || holdID == "" {
			http.Error(w, `{"error":"project id and hold id required"}`, http.StatusBadRequest)
			return
		}
		if err := limits.CheckLegalTeamTier(r.Context(), projectID); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q,"code":"legal_team_required"}`, err.Error()), http.StatusPaymentRequired)
			return
		}
		if err := svc.Revoke(r.Context(), projectID, holdID); err != nil {
			slog.Error("revoke retention hold failed", "error", err, "project_id", projectID, "hold_id", holdID)
			http.Error(w, `{"error":"revoke failed"}`, http.StatusInternalServerError)
			return
		}

		if a := audit.FromContext(r.Context()); a != nil {
			aid, aemail := audit.ActorFromContext(r.Context())
			a.Log(r.Context(), projectID, aid, aemail, audit.ActionRetentionHoldRevoked,
				audit.WithTarget("retention_hold", holdID),
				audit.WithIP(r.RemoteAddr))
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
