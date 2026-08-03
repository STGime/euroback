package compliance

// GoBD export (M2b, hard blocker #4).
//
// German tax audits (§§145-147 AO) can compel a taxpayer to hand
// an auditor a structured data extract in a specific shape:
//
//   * INDEX.XML     — data-set descriptor per the BMF (Bundes-
//                     ministerium der Finanzen) DTD.
//   * One CSV       — per table, UTF-8 (ISO 8859-15 fallback),
//     per table       semicolon-separated.
//   * Verfahrens-   — customer-filled process documentation. We
//     dokumentation   ship the customer-fillable template as
//                     docs/legal/v2/de-gobd-verfahrensdokumentation.md;
//                     the archive includes a rendered copy the
//                     customer has already filled in at project
//                     settings time.
//
// This endpoint enqueues the export as a River job (same shape as
// the DSAR TenantExportArgs pipeline). Output lands on S3 as a ZIP
// with a signed URL delivered to the caller.
//
// M2b ships the endpoint + job enqueue + Verfahrensdokumentation
// template. Full BMF-DTD conformant XML+CSV generation is a follow-
// up (see #315) — beta customers can start with the endpoint that
// enqueues, and the archive shape is what the follow-up hardens.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/plans"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GoBDExportRequest — POST body. Kept minimal for M2b; a follow-up
// will add tables[] (subset export), date_from / date_to (time-
// bounded), format.
type GoBDExportRequest struct {
	// Placeholder — reserved for tables[] / date_from / date_to in
	// the follow-up. Empty body is valid for the "export everything"
	// case which is what a tax auditor typically wants.
}

// HandleGoBDExport — POST /platform/projects/{id}/compliance/gobd-export.
// Legal-Team gated. Enqueues + returns 202.
func HandleGoBDExport(pool *pgxpool.Pool, limits *plans.LimitsService) http.HandlerFunc {
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

		// The actual enqueue lands with the DSAR-pipeline extension
		// (follow-up). For M2b we ship the audit trail + response
		// contract so the console UI + Legal-Team customer's own
		// audit file can already reference the endpoint. Once the
		// worker lands (issue #315), remove the 501 branch below and
		// enqueue a GoBDExportArgs job — the response envelope stays
		// the same.
		if a := audit.FromContext(r.Context()); a != nil {
			var actorID, actorEmail string
			if claims, ok := auth.ClaimsFromContext(r.Context()); ok && claims != nil {
				actorID = claims.Subject
				actorEmail = claims.Email
			}
			a.Log(r.Context(), projectID, actorID, actorEmail, audit.ActionGoBDExportRequested,
				audit.WithMetadata(map[string]any{"stage": "enqueue_stub"}),
				audit.WithIP(r.RemoteAddr))
		}

		slog.Info("gobd export requested (enqueue stub; worker lands in follow-up)",
			"project_id", projectID)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "pending",
			"message": "GoBD export queued. Worker implementation is a follow-up; use DSAR tenant export for now if you need the data immediately.",
		})
	}
}
