package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// writeJSON writes a JSON body with the given status. Local helper — the
// package otherwise writes responses inline.
func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// Project ownership transfer (#846).
//
// Flow (mirrors project invitations, but reassigns the owner):
//   1. The current owner POSTs /platform/projects/{id}/transfer with the
//      recipient's email. We require a console session (not a PAT, not SSO)
//      and a fresh re-auth, because this hands the whole project — and its
//      future billing — to someone else.
//   2. The recipient gets an emailed, expiring token and POSTs it to
//      /platform/transfers/accept while signed in to their OWN account
//      (personal session; the signed-in email must match the invite).
//   3. On accept we reassign projects.owner_id, demote the old owner to
//      admin, and — for a paid project — cancel the old owner's subscription
//      AT PERIOD END (no refund). The project stays on its paid plan until
//      the period the old owner already paid for ends; the new owner starts
//      their own subscription from the next cycle. Nothing double-charges
//      the overlapping period (the one-live-subscription-per-project index
//      is what prevents a second concurrent charge).
//
// Deferred to a later iteration (#846 "later"): org-attached projects and
// Team/Legal-Team tiers, which carry org access and dedicated-instance
// billing. Those are refused here with a clear message.

// transferTokenTTL mirrors the project-invitation expiry.
const transferTokenTTL = 7 * 24 * time.Hour

// CancelSubAtPeriodEnd cancels a project's live subscription at period end,
// run as the current owner. Injected from the billing service so this
// package doesn't import billing (same pattern as SetSubscriptionCanceller).
// nil when billing is disabled.
type CancelSubAtPeriodEnd func(ctx context.Context, ownerID, subscriptionID string) error

// CheckOwnerProjectLimit returns a non-nil error when ownerID is already at
// their plan's project cap. Injected from plans.LimitsService.CheckProjectLimit.
// nil when limits aren't wired.
type CheckOwnerProjectLimit func(ctx context.Context, ownerID string) error

// ReauthFunc confirms the caller recently authenticated (or supplied their
// current password). Injected from PlatformAuthService.CheckPasskeyReauth.
// nil in dev where re-auth isn't enforced.
type ReauthFunc func(ctx context.Context, claims *auth.Claims, currentPassword string) error

// ── Initiate ──

type initiateTransferRequest struct {
	Email           string `json:"email"`
	CurrentPassword string `json:"current_password"`
}

// HandleInitiateTransfer starts a transfer of a project to another user.
// POST /platform/projects/{id}/transfer  (owner only, console session)
func HandleInitiateTransfer(pool *pgxpool.Pool, reauth ReauthFunc, sendEmail func(ctx context.Context, to, subject, html string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		claims, _, ok := RequireRole(w, r, pool, projectID, "owner")
		if !ok {
			return
		}

		var req initiateTransferRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		// Giving a project away is a sensitive, person-owned action: require
		// a recent login or the current password.
		if reauth != nil {
			if err := reauth(r.Context(), claims, req.CurrentPassword); err != nil {
				if errors.Is(err, auth.ErrReauthRequired) {
					writeJSON(w, http.StatusForbidden, map[string]string{
						"error": "please confirm your current password to transfer ownership",
						"code":  "reauth_required",
					})
					return
				}
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "re-authentication failed"})
				return
			}
		}

		email := strings.ToLower(strings.TrimSpace(req.Email))
		if email == "" || !strings.Contains(email, "@") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a valid recipient email is required"})
			return
		}
		if email == strings.ToLower(claims.Email) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "you cannot transfer a project to yourself"})
			return
		}

		// Load the project and apply the v1 scope guards.
		var (
			projectName string
			plan        string
			orgID       *string
		)
		err := pool.QueryRow(r.Context(),
			`SELECT name, plan, org_id FROM public.projects WHERE id = $1`,
			projectID,
		).Scan(&projectName, &plan, &orgID)
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
			return
		}
		if err != nil {
			slog.Error("transfer: load project", "project_id", projectID, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load project"})
			return
		}
		if orgID != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "projects attached to an organization can't be transferred this way yet — detach it first or contact support"})
			return
		}
		if plan == "team" || plan == "legal_team" {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "Team-tier projects can't be transferred yet — contact support"})
			return
		}

		raw, hash, err := generateInviteToken()
		if err != nil {
			slog.Error("transfer: token gen", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create transfer"})
			return
		}
		expiresAt := time.Now().Add(transferTokenTTL)

		if _, err := pool.Exec(r.Context(),
			`INSERT INTO public.project_ownership_transfers
			     (project_id, new_owner_email, token_hash, initiated_by, expires_at, sent_at)
			 VALUES ($1, $2, $3, $4, $5, now())
			 ON CONFLICT (project_id) DO UPDATE SET
			     new_owner_email = EXCLUDED.new_owner_email,
			     token_hash      = EXCLUDED.token_hash,
			     initiated_by    = EXCLUDED.initiated_by,
			     expires_at      = EXCLUDED.expires_at,
			     sent_at         = now(),
			     accepted_at     = NULL`,
			projectID, email, hash, claims.Subject, expiresAt,
		); err != nil {
			slog.Error("transfer: upsert", "project_id", projectID, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create transfer"})
			return
		}

		// Email is best-effort — the row exists and the owner can re-send.
		if sendEmail != nil {
			html := renderTransferEmail(projectName, claims.Email, raw)
			if err := sendEmail(r.Context(), email, "You've been offered ownership of a Eurobase project", html); err != nil {
				slog.Warn("transfer: send email failed", "project_id", projectID, "error", err)
			}
		}

		if auditSvc := audit.FromContext(r.Context()); auditSvc != nil {
			auditSvc.Log(r.Context(), projectID, claims.Subject, claims.Email,
				audit.ActionTransferInitiated,
				audit.WithTarget("transfer", email),
				audit.WithMetadata(map[string]interface{}{"email": email}),
				audit.WithIP(r.RemoteAddr))
		}

		writeJSON(w, http.StatusCreated, map[string]string{"status": "initiated", "email": email})
	}
}

// ── Status / cancel ──

type pendingTransfer struct {
	Email     string    `json:"email"`
	SentAt    time.Time `json:"sent_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// HandleGetTransfer returns the pending transfer for a project, or null.
// GET /platform/projects/{id}/transfer  (owner only)
func HandleGetTransfer(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if _, _, ok := RequireRole(w, r, pool, projectID, "owner"); !ok {
			return
		}

		var t pendingTransfer
		err := pool.QueryRow(r.Context(),
			`SELECT new_owner_email, sent_at, expires_at
			   FROM public.project_ownership_transfers
			  WHERE project_id = $1 AND accepted_at IS NULL AND expires_at > now()`,
			projectID,
		).Scan(&t.Email, &t.SentAt, &t.ExpiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, map[string]interface{}{"transfer": nil})
			return
		}
		if err != nil {
			slog.Error("transfer: get", "project_id", projectID, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load transfer"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"transfer": t})
	}
}

// HandleCancelTransfer cancels a pending (not yet accepted) transfer.
// DELETE /platform/projects/{id}/transfer  (owner only, console session)
func HandleCancelTransfer(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		claims, _, ok := RequireRole(w, r, pool, projectID, "owner")
		if !ok {
			return
		}

		tag, err := pool.Exec(r.Context(),
			`DELETE FROM public.project_ownership_transfers
			  WHERE project_id = $1 AND accepted_at IS NULL`,
			projectID,
		)
		if err != nil {
			slog.Error("transfer: cancel", "project_id", projectID, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not cancel transfer"})
			return
		}
		if tag.RowsAffected() == 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no pending transfer"})
			return
		}

		if auditSvc := audit.FromContext(r.Context()); auditSvc != nil {
			auditSvc.Log(r.Context(), projectID, claims.Subject, claims.Email,
				audit.ActionTransferCancelled,
				audit.WithTarget("transfer", projectID),
				audit.WithIP(r.RemoteAddr))
		}

		writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
	}
}

// ── Accept ──

type acceptTransferRequest struct {
	Token string `json:"token"`
}

// HandleAcceptTransfer accepts a transfer: the signed-in recipient becomes
// the project's owner. POST /platform/transfers/accept  (personal session)
func HandleAcceptTransfer(pool *pgxpool.Pool, cancelSub CancelSubAtPeriodEnd, checkLimit CheckOwnerProjectLimit) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, hasAuth := auth.ClaimsFromContext(r.Context())
		if !hasAuth || claims == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		var req acceptTransferRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Token) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a transfer token is required"})
			return
		}

		var (
			transferID string
			projectID  string
			email      string
			expiresAt  time.Time
			acceptedAt *time.Time
		)
		err := pool.QueryRow(r.Context(),
			`SELECT id, project_id, new_owner_email, expires_at, accepted_at
			   FROM public.project_ownership_transfers
			  WHERE token_hash = $1`,
			hashToken(req.Token),
		).Scan(&transferID, &projectID, &email, &expiresAt, &acceptedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "transfer not found"})
			return
		}
		if err != nil {
			slog.Error("transfer: accept lookup", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load transfer"})
			return
		}
		if acceptedAt != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "this transfer has already been accepted"})
			return
		}
		if time.Now().After(expiresAt) {
			writeJSON(w, http.StatusGone, map[string]string{"error": "this transfer has expired — ask the owner to send a new one"})
			return
		}
		if strings.ToLower(claims.Email) != strings.ToLower(email) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "this transfer was sent to a different email address"})
			return
		}

		// Current owner + plan, re-checked under the live row.
		var (
			oldOwnerID string
			plan       string
			orgID      *string
		)
		if err := pool.QueryRow(r.Context(),
			`SELECT owner_id, plan, org_id FROM public.projects WHERE id = $1`,
			projectID,
		).Scan(&oldOwnerID, &plan, &orgID); err != nil {
			slog.Error("transfer: accept load project", "project_id", projectID, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load project"})
			return
		}
		if orgID != nil || plan == "team" || plan == "legal_team" {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "this project can no longer be transferred this way — contact support"})
			return
		}
		if oldOwnerID == claims.Subject {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "you already own this project"})
			return
		}

		// The recipient must have room for another owned project.
		if checkLimit != nil {
			if err := checkLimit(r.Context(), claims.Subject); err != nil {
				writeJSON(w, http.StatusForbidden, map[string]string{
					"error": "you've reached your plan's project limit — upgrade or remove a project before accepting",
					"code":  "project_limit_reached",
				})
				return
			}
		}

		// Billing handover: cancel the old owner's live subscription at
		// period end (no refund). Done BEFORE the ownership flip so the
		// cancel runs as the still-current owner. The project keeps its
		// paid plan until next_charge_at; the downgrade sweep flips it to
		// Free then if the new owner hasn't started their own subscription.
		billingHandover := false
		if cancelSub != nil {
			var subID string
			subErr := pool.QueryRow(r.Context(),
				`SELECT id FROM public.subscriptions
				  WHERE project_id = $1 AND status IN ('incomplete','active','past_due')
				  ORDER BY created_at DESC LIMIT 1`,
				projectID,
			).Scan(&subID)
			if subErr == nil {
				if err := cancelSub(r.Context(), oldOwnerID, subID); err != nil {
					slog.Error("transfer: cancel old subscription", "project_id", projectID, "subscription_id", subID, "error", err)
					writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not hand over billing — please try again"})
					return
				}
				billingHandover = true
			} else if !errors.Is(subErr, pgx.ErrNoRows) {
				slog.Error("transfer: find live subscription", "project_id", projectID, "error", subErr)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not hand over billing"})
				return
			}
		}

		// Reassign ownership atomically.
		tx, err := pool.Begin(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not complete transfer"})
			return
		}
		defer tx.Rollback(r.Context())

		if _, err := tx.Exec(r.Context(),
			`UPDATE public.project_members SET role = 'admin'
			  WHERE project_id = $1 AND user_id = $2 AND role = 'owner'`,
			projectID, oldOwnerID,
		); err != nil {
			slog.Error("transfer: demote old owner", "project_id", projectID, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not complete transfer"})
			return
		}
		if _, err := tx.Exec(r.Context(),
			`INSERT INTO public.project_members (project_id, user_id, role, invited_by)
			 VALUES ($1, $2, 'owner', $3)
			 ON CONFLICT (project_id, user_id) DO UPDATE SET role = 'owner'`,
			projectID, claims.Subject, oldOwnerID,
		); err != nil {
			slog.Error("transfer: promote new owner", "project_id", projectID, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not complete transfer"})
			return
		}
		if _, err := tx.Exec(r.Context(),
			`UPDATE public.projects SET owner_id = $1 WHERE id = $2`,
			claims.Subject, projectID,
		); err != nil {
			slog.Error("transfer: reassign owner_id", "project_id", projectID, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not complete transfer"})
			return
		}
		if _, err := tx.Exec(r.Context(),
			`UPDATE public.project_ownership_transfers SET accepted_at = now() WHERE id = $1`,
			transferID,
		); err != nil {
			slog.Error("transfer: stamp accepted_at", "transfer_id", transferID, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not complete transfer"})
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			slog.Error("transfer: commit", "project_id", projectID, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not complete transfer"})
			return
		}

		if auditSvc := audit.FromContext(r.Context()); auditSvc != nil {
			auditSvc.Log(r.Context(), projectID, claims.Subject, claims.Email,
				audit.ActionOwnershipTransferred,
				audit.WithTarget("project", projectID),
				audit.WithMetadata(map[string]interface{}{
					"from_owner_id":    oldOwnerID,
					"billing_handover": billingHandover,
				}),
				audit.WithIP(r.RemoteAddr))
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":           "accepted",
			"project_id":       projectID,
			"billing_handover": billingHandover,
		})
	}
}

// renderTransferEmail builds the recipient email with a /transfer?token= link.
func renderTransferEmail(projectName, fromEmail, token string) string {
	acceptURL := consoleURL() + "/transfer?token=" + token
	return `<!DOCTYPE html>
<html>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; max-width: 600px; margin: 0 auto; padding: 24px; color: #111;">
  <h2 style="color: #1e40af; margin-top: 0;">You've been offered ownership of a Eurobase project</h2>
  <p><strong>` + html.EscapeString(fromEmail) + `</strong> wants to transfer the project <strong>` + html.EscapeString(projectName) + `</strong> to you.</p>
  <p>If you accept, you become the project's owner: you'll manage its settings, members and billing. The previous owner stays on as an admin unless they leave.</p>
  <p style="margin: 24px 0;">
    <a href="` + acceptURL + `" style="display: inline-block; background: #2563eb; color: #fff; padding: 12px 24px; border-radius: 8px; text-decoration: none; font-weight: 600;">
      Review &amp; accept transfer
    </a>
  </p>
  <p style="color: #6b7280; font-size: 13px;">You'll accept while signed in to your own Eurobase account. If this project is on a paid plan, it stays active through the period the previous owner already paid for, and you'll set up your own billing from the next cycle. This offer expires in 7 days.</p>
  <hr style="border: none; border-top: 1px solid #e5e7eb; margin: 32px 0 16px;">
  <p style="color: #9ca3af; font-size: 12px; margin: 16px 0 0;">If you didn't expect this, you can safely ignore this email — nothing changes unless you accept.</p>
</body>
</html>`
}
