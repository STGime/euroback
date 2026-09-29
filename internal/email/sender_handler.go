package email

// HTTP handlers for /platform/projects/{id}/email-sender (#235 Part 1).
//
// Surface:
//   GET    /email-sender         load config (no password)
//   PUT    /email-sender         upsert config + seal new password
//   DELETE /email-sender         clear config (fall back to platform)
//   POST   /email-sender/test    send a verification email, mark verified
//   GET    /email-sender/status  platform conditions (outbound SMTP blocked)
//
// Also GET /email-log (HandleDeliveryLog): the auth email log.
//
// All routes are admin-only; the wiring lives in internal/gateway/router.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/eurobase/euroback/internal/plans"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// HandleGetSender returns the current sender config for the project, or
// 404 if none is set. Password is never returned (the JSON struct
// omits it via the `-` tag; HasPassword flags whether bytes exist).
func HandleGetSender(svc *SenderService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		sender, err := svc.LoadConfig(r.Context(), projectID)
		if err != nil {
			if errors.Is(err, ErrNotConfigured) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "no custom email sender configured"})
				return
			}
			slog.Error("get project email sender failed", "project_id", projectID, "error", err)
			httpJSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sender)
	}
}

// PlanGate says whether the project's plan includes custom SMTP
// (plans.LimitsService.CheckBYOSMTP; Pro and up).
type PlanGate interface {
	CheckBYOSMTP(ctx context.Context, projectID string) error
}

// refuseOffPlan answers 403 when the plan doesn't include custom SMTP.
// nil gate = no plan enforcement (tests, dev).
func refuseOffPlan(w http.ResponseWriter, r *http.Request, gate PlanGate, projectID string) bool {
	if gate == nil {
		return false
	}
	if err := gate.CheckBYOSMTP(r.Context(), projectID); err != nil {
		if !errors.Is(err, plans.ErrNotOnPlan) {
			slog.Error("custom SMTP: plan check failed", "project_id", projectID, "error", err)
			httpJSONError(w, "couldn't check the project's plan, try again", http.StatusServiceUnavailable)
			return true
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "Custom SMTP is available on the Pro plan and up. Upgrade the project to use your own SMTP server.",
			"code":  "plan_required",
		})
		return true
	}
	return false
}

// HandlePutSender upserts the sender config + seals a new password.
// An empty password on an existing sender preserves the stored one.
// Pro and up (the plan gate); reading and removing a config work on
// every plan, so a downgraded project can still see and clear it.
func HandlePutSender(svc *SenderService, gate PlanGate) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if refuseOffPlan(w, r, gate, projectID) {
			return
		}
		var req UpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpJSONError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		out, err := svc.Upsert(r.Context(), projectID, req)
		if err != nil {
			if errors.Is(err, ErrVaultNotConfigured) {
				httpJSONError(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			// Validation errors carry an operator-readable message;
			// surface as 400.
			httpJSONError(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

// HandleDeleteSender clears the sender so the project falls back to
// the platform sender. Idempotent — 204 even if nothing was set.
func HandleDeleteSender(svc *SenderService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if err := svc.Delete(r.Context(), projectID); err != nil {
			httpJSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleSenderStatus reports platform-side conditions for custom SMTP:
// whether outbound SMTP is currently blocked (and the notice to show).
func HandleSenderStatus() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := map[string]any{"egress_blocked": SMTPEgressBlocked()}
		if SMTPEgressBlocked() {
			out["notice"] = EgressBlockedNotice
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

// HandleDeliveryLog returns the project's auth email log, newest first
// (?limit=, default 100, max 500). pool must be able to read
// project_email_log — the developer pool.
func HandleDeliveryLog(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		entries, err := ListDeliveryLog(r.Context(), pool, projectID, limit)
		if err != nil {
			slog.Error("list email log failed", "project_id", projectID, "error", err)
			httpJSONError(w, "failed to load the email log", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"entries":        entries,
			"retention_days": int(DeliveryLogRetention.Hours() / 24),
		})
	}
}

// testSendRequest is the payload for POST /email-sender/test.
type testSendRequest struct {
	To string `json:"to"`
}

// HandleTestSender dispatches a verification email through the
// configured custom SMTP. On success, the sender's verified_at is
// bumped and the send-path is allowed to use it for real auth email.
// On failure, last_error / last_error_at are recorded so the console
// can show exactly what went wrong without re-running the test.
//
// The test email is a small "your custom SMTP works" notice — the
// goal is to exercise the dial + auth + RCPT path, not to look pretty.
func HandleTestSender(svc *SenderService, gate PlanGate) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if refuseOffPlan(w, r, gate, projectID) {
			return
		}
		var req testSendRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.To) == "" {
			httpJSONError(w, "request body must include {to: \"...\"}", http.StatusBadRequest)
			return
		}

		// Load with sealed password decrypt — but bypass the
		// verified-at gate (we're testing it FOR the first time).
		// LoadForSend returns ErrSenderNotVerified for unverified
		// senders along with a populated struct; for the test path
		// we want to use it anyway.
		sender, err := svc.loadForTest(r.Context(), projectID)
		if err != nil {
			if errors.Is(err, ErrNotConfigured) {
				httpJSONError(w, "no custom email sender configured", http.StatusNotFound)
				return
			}
			httpJSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		const subject = "Eurobase test send"
		body := fmt.Sprintf(`<p>This is a test from your Eurobase project.</p>
<p>If you received it, your custom SMTP is wired up correctly and the project will use it for auth emails from now on.</p>
<p style="color:#666;font-size:12px;">Project ID: %s · Sent via %s:%d</p>`, projectID, sender.Host, sender.Port)

		if err := sendViaCustomSMTP(r.Context(), sender, req.To, subject, body); err != nil {
			slog.Warn("custom SMTP test send failed", "project_id", projectID, "host", sender.Host, "error", err)
			// The explanation first, then the raw error — stored too, so
			// the console shows it again later.
			described := DescribeSMTPError(err)
			_ = svc.MarkFailed(r.Context(), projectID, described)
			// 422 — the request was understood and the sender exists,
			// but the test failed for a config reason.
			httpJSONError(w, described, http.StatusUnprocessableEntity)
			return
		}
		if err := svc.MarkVerified(r.Context(), projectID); err != nil {
			slog.Error("mark verified failed", "project_id", projectID, "error", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "to": req.To})
	}
}

// loadForTest is the test-send variant of LoadForSend: it pulls the
// sealed password but does NOT refuse on verified_at IS NULL (the
// whole point of the test is to verify an unverified sender). Kept
// internal because the regular send-path must always honour the
// verified gate.
func (s *SenderService) loadForTest(ctx context.Context, projectID string) (*ProjectSender, error) {
	sender, err := s.LoadForSend(ctx, projectID)
	if err != nil && !errors.Is(err, ErrSenderNotVerified) {
		return nil, err
	}
	return sender, nil
}

func httpJSONError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
