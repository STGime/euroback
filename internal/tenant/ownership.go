package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/mailaddr"
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

// errAlreadyAccepted is returned inside the accept transaction when the
// single-use claim of the transfer row loses a race (another accept of the
// same token committed first).
var errAlreadyAccepted = errors.New("transfer already accepted")

// BillingHandover runs inside the ownership transaction: it closes what the
// previous owner had open on the project and cancels their paid subscription
// at the end of the period they paid for, so the transfer and the billing
// change commit together. handedOver reports whether there was a paid
// subscription; paidUntil is when it ends. finish runs after the commit (the
// calls to the payment provider). Injected so this package doesn't import
// billing.
type BillingHandover func(ctx context.Context, tx pgx.Tx, projectID string) (handedOver bool, paidUntil *time.Time, finish func(context.Context), err error)

// CheckTransferable returns ErrTransferPaymentOverdue when the project's
// billing state doesn't allow a transfer (its last payment failed: there's
// no paid period to hand over).
type CheckTransferable func(ctx context.Context, projectID string) error

// ErrTransferPaymentOverdue: see CheckTransferable.
var ErrTransferPaymentOverdue = errors.New("transfer: the project's last payment failed")

// transferResendInterval throttles re-sending (or redirecting) an offer: each
// send emails an address the owner picks.
const transferResendInterval = time.Minute

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
func HandleInitiateTransfer(pool *pgxpool.Pool, reauth ReauthFunc, checkTransferable CheckTransferable, sendEmail func(ctx context.Context, to, subject, html string) error) http.HandlerFunc {
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
		if mailaddr.IsReserved(email) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "this address can't receive email (reserved test domain) — use a real address"})
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
		if !transferBillingOK(w, r, checkTransferable, projectID) {
			return
		}

		raw, hash, err := generateInviteToken()
		if err != nil {
			slog.Error("transfer: token gen", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create transfer"})
			return
		}
		expiresAt := time.Now().Add(transferTokenTTL)

		// A pending offer sent less than transferResendInterval ago isn't
		// replaced (the WHERE on the conflict update): each send emails an
		// address the owner chooses.
		tag, err := pool.Exec(r.Context(),
			`INSERT INTO public.project_ownership_transfers
			     (project_id, new_owner_email, token_hash, initiated_by, expires_at, sent_at)
			 VALUES ($1, $2, $3, $4, $5, now())
			 ON CONFLICT (project_id) DO UPDATE SET
			     new_owner_email = EXCLUDED.new_owner_email,
			     token_hash      = EXCLUDED.token_hash,
			     initiated_by    = EXCLUDED.initiated_by,
			     expires_at      = EXCLUDED.expires_at,
			     sent_at         = now(),
			     accepted_at     = NULL
			 WHERE project_ownership_transfers.accepted_at IS NOT NULL
			    OR project_ownership_transfers.sent_at < now() - $6::interval`,
			projectID, email, hash, claims.Subject, expiresAt,
			fmt.Sprintf("%d seconds", int(transferResendInterval.Seconds())),
		)
		if err != nil {
			slog.Error("transfer: upsert", "project_id", projectID, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create transfer"})
			return
		}
		if tag.RowsAffected() == 0 {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "an offer was just sent — wait a minute before sending another"})
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

		// Expire rather than delete: the row keeps sent_at, so cancelling
		// and re-sending stays under the re-send throttle. The emailed
		// link stops working either way.
		tag, err := pool.Exec(r.Context(),
			`UPDATE public.project_ownership_transfers
			    SET expires_at = now()
			  WHERE project_id = $1 AND accepted_at IS NULL AND expires_at > now()`,
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
func HandleAcceptTransfer(pool *pgxpool.Pool, handover BillingHandover, checkTransferable CheckTransferable, checkLimit CheckOwnerProjectLimit) http.HandlerFunc {
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

		// A suspended account can't acquire a project — same bar as
		// CreateProject (an existing JWT stays valid up to ~24h after
		// suspension, so this is the enforcement point, not the token).
		var suspendedAt *time.Time
		if err := pool.QueryRow(r.Context(),
			`SELECT suspended_at FROM public.platform_users WHERE id = $1`,
			claims.Subject,
		).Scan(&suspendedAt); err != nil {
			slog.Error("transfer: accept load account", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load account"})
			return
		}
		if suspendedAt != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "your account is suspended"})
			return
		}

		// Current owner + plan, re-checked under the live row. The project
		// must still be active and its owner not suspended: an offer made
		// before a project or account was suspended doesn't survive it.
		var (
			oldOwnerID     string
			plan           string
			projectStatus  string
			orgID          *string
			ownerSuspended bool
		)
		if err := pool.QueryRow(r.Context(),
			`SELECT p.owner_id, p.plan, p.status, p.org_id, u.suspended_at IS NOT NULL
			   FROM public.projects p
			   JOIN public.platform_users u ON u.id = p.owner_id
			  WHERE p.id = $1`,
			projectID,
		).Scan(&oldOwnerID, &plan, &projectStatus, &orgID, &ownerSuspended); err != nil {
			slog.Error("transfer: accept load project", "project_id", projectID, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load project"})
			return
		}
		if orgID != nil || plan == "team" || plan == "legal_team" || projectStatus != "active" || ownerSuspended {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "this project can no longer be transferred this way — contact support"})
			return
		}
		if oldOwnerID == claims.Subject {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "you already own this project"})
			return
		}
		if !transferBillingOK(w, r, checkTransferable, projectID) {
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

		// A paid subscription needs the billing hook to hand it over;
		// without it the previous owner would keep being charged for a
		// project they no longer own — refuse rather than transfer unsafely.
		if handover == nil {
			var paid bool
			if err := pool.QueryRow(r.Context(),
				`SELECT EXISTS (SELECT 1 FROM public.subscriptions
				                 WHERE project_id = $1
				                   AND status IN ('incomplete', 'scheduled', 'active', 'past_due'))`,
				projectID,
			).Scan(&paid); err != nil {
				slog.Error("transfer: find subscription", "project_id", projectID, "error", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load billing"})
				return
			}
			if paid {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "billing handover is unavailable right now — please try again later or contact support"})
				return
			}
		}

		// Reassign ownership atomically. The transfer row is claimed with a
		// compare-and-swap that also re-checks the offer itself (token,
		// address, expiry): the owner may have withdrawn or redirected it
		// since it was read above. The owner_id update is conditional on
		// the owner read above, so a concurrent change aborts the transfer.
		billingHandover := false
		var paidUntil *time.Time
		var finish func(context.Context)
		txErr := func() error {
			tx, err := pool.Begin(r.Context())
			if err != nil {
				return err
			}
			defer tx.Rollback(r.Context())

			tag, err := tx.Exec(r.Context(),
				`UPDATE public.project_ownership_transfers
				    SET accepted_at = now()
				  WHERE id = $1 AND accepted_at IS NULL
				    AND token_hash = $2
				    AND lower(new_owner_email) = lower($3)
				    AND expires_at > now()`,
				transferID, hashToken(req.Token), claims.Email,
			)
			if err != nil {
				return fmt.Errorf("stamp accepted_at: %w", err)
			}
			if tag.RowsAffected() == 0 {
				return errAlreadyAccepted
			}

			// Old owner becomes admin. Upsert (not a conditional UPDATE) so
			// a project somehow missing its owner membership row still
			// leaves the old owner with admin access rather than locked out.
			if _, err := tx.Exec(r.Context(),
				`INSERT INTO public.project_members (project_id, user_id, role, invited_by)
				 VALUES ($1, $2, 'admin', $2)
				 ON CONFLICT (project_id, user_id) DO UPDATE SET role = 'admin'`,
				projectID, oldOwnerID,
			); err != nil {
				return fmt.Errorf("demote old owner: %w", err)
			}
			if _, err := tx.Exec(r.Context(),
				`INSERT INTO public.project_members (project_id, user_id, role, invited_by)
				 VALUES ($1, $2, 'owner', $3)
				 ON CONFLICT (project_id, user_id) DO UPDATE SET role = 'owner'`,
				projectID, claims.Subject, oldOwnerID,
			); err != nil {
				return fmt.Errorf("promote new owner: %w", err)
			}
			// The scope guards again, on the row being changed: an org
			// attach or a Team upgrade may have committed since the read.
			tag, err = tx.Exec(r.Context(),
				`UPDATE public.projects SET owner_id = $1
				  WHERE id = $2 AND owner_id = $3 AND status = 'active'
				    AND org_id IS NULL AND plan NOT IN ('team', 'legal_team')`,
				claims.Subject, projectID, oldOwnerID,
			)
			if err != nil {
				return fmt.Errorf("reassign owner_id: %w", err)
			}
			if tag.RowsAffected() == 0 {
				return errAlreadyAccepted
			}
			// A pending invitation to the new owner would, if accepted
			// later, overwrite their owner membership with a lower role.
			if _, err := tx.Exec(r.Context(),
				`DELETE FROM public.project_invitations
				  WHERE project_id = $1 AND lower(email) = lower($2)`,
				projectID, claims.Email,
			); err != nil {
				return fmt.Errorf("drop pending invitation: %w", err)
			}
			if handover != nil {
				handed, until, fin, err := handover(r.Context(), tx, projectID)
				if err != nil {
					return fmt.Errorf("billing handover: %w", err)
				}
				billingHandover, paidUntil, finish = handed, until, fin
			}
			return tx.Commit(r.Context())
		}()
		if errors.Is(txErr, errAlreadyAccepted) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "this offer is no longer valid — it was accepted, withdrawn or changed. Ask the owner to send a new one"})
			return
		}
		if txErr != nil {
			slog.Error("transfer: complete", "project_id", projectID, "error", txErr)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not complete transfer"})
			return
		}

		// Committed: stop the previous owner's charges at the payment
		// provider. Detached from the request so a closed tab doesn't cut
		// it short; a failure here is covered by the webhook (later
		// charges are refunded) and the period-end retry.
		if finish != nil {
			fctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
			finish(fctx)
			cancel()
		}

		if auditSvc := audit.FromContext(r.Context()); auditSvc != nil {
			auditSvc.Log(r.Context(), projectID, claims.Subject, claims.Email,
				audit.ActionOwnershipTransferred,
				audit.WithTarget("project", projectID),
				audit.WithMetadata(map[string]interface{}{
					"from_owner_id":    oldOwnerID,
					"billing_handover": billingHandover,
					"paid_until":       paidUntil,
				}),
				audit.WithIP(r.RemoteAddr))
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":           "accepted",
			"project_id":       projectID,
			"billing_handover": billingHandover,
			"paid_until":       paidUntil,
		})
	}
}

// transferBillingOK refuses (and writes the response) when the project's
// billing state doesn't allow a transfer. A nil check (billing disabled)
// allows it.
func transferBillingOK(w http.ResponseWriter, r *http.Request, check CheckTransferable, projectID string) bool {
	if check == nil {
		return true
	}
	err := check(r.Context(), projectID)
	if err == nil {
		return true
	}
	if errors.Is(err, ErrTransferPaymentOverdue) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "this project's last payment failed — it can be transferred once the payment has come through or the project has moved to the Free plan",
			"code":  "payment_overdue",
		})
		return false
	}
	slog.Error("transfer: billing check", "project_id", projectID, "error", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load billing"})
	return false
}

// renderTransferEmail builds the recipient email with a /transfer?token= link.
func renderTransferEmail(projectName, fromEmail, token string) string {
	acceptURL := consoleURL() + "/transfer?token=" + token
	return `<!DOCTYPE html>
<html>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; max-width: 600px; margin: 0 auto; padding: 24px; color: #111;">
  <h2 style="color: #1e40af; margin-top: 0;">You've been offered ownership of a Eurobase project</h2>
  <p><strong>` + html.EscapeString(fromEmail) + `</strong> wants to transfer the project <strong>` + html.EscapeString(projectName) + `</strong> to you.</p>
  <p>If you accept, you become the project's owner: you'll manage its settings, members and billing. The previous owner stays on as an admin unless you remove them.</p>
  <p style="margin: 24px 0;">
    <a href="` + acceptURL + `" style="display: inline-block; background: #2563eb; color: #fff; padding: 12px 24px; border-radius: 8px; text-decoration: none; font-weight: 600;">
      Review &amp; accept transfer
    </a>
  </p>
  <p style="color: #6b7280; font-size: 13px;">You'll accept while signed in to your own Eurobase account. If this project is on a paid plan, it stays on that plan until the end of the period the current owner has paid for. You can set up your own subscription in Billing any time before then: your card isn't charged until that period ends, so there's no gap and no double charge. This offer expires in 7 days.</p>
  <hr style="border: none; border-top: 1px solid #e5e7eb; margin: 32px 0 16px;">
  <p style="color: #9ca3af; font-size: 12px; margin: 16px 0 0;">If you didn't expect this, you can safely ignore this email — nothing changes unless you accept.</p>
</body>
</html>`
}
