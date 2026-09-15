package tenant

// Team-tier beta-access request from the console pricing page.
//
// Route: POST /platform/team-beta-request  (auth-required — the caller
// is a logged-in console user; email and user_id come from JWT claims
// so they can't be spoofed).
//
// Reuses the contact_requests table + Discord ping used by the public
// marketing widget:
//   - Row lands in public.contact_requests with source='team_beta_request'
//     so the admin panel can filter these separately from generic
//     contact-form submissions.
//   - notifyTeamBetaRequestAsync fires a distinct-coloured Discord embed
//     so ops see the ask in real time.
//   - Rate limit: 1 request per hour per user — a legitimate user asks
//     once; anything above that is a form-fumble and a retry a minute
//     later is fine, but we don't want a distracted user filling the
//     table with duplicates.
//
// Runs on the gateway pool. Migration 000112 grants gateway INSERT-
// only on contact_requests; SELECT is REVOKE'd. Same security posture
// as the public contact widget — a runtime SQL-injection couldn't
// enumerate past requests.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/ratelimit"
	"github.com/jackc/pgx/v5/pgxpool"
)

// teamBetaRequestRate is deliberately tight. A user should have to
// ask us once; if they hit this limit they can email support at
// contact@eurobase.app instead.
const (
	teamBetaRequestRateLimitCount  = 1
	teamBetaRequestRateLimitWindow = 1 * time.Hour
)

// Field bounds — reuse the contact_requests DB CHECK constraints
// (they're the same table, so the same caps apply).
const (
	teamBetaRequestMinMessageLen = 10
	teamBetaRequestMaxMessageLen = 5000
	teamBetaRequestMaxBodyBytes  = 32 * 1024
)

// TeamBetaRequest is the JSON body of POST /platform/team-beta-request.
// Only message is supplied by the client — email and user_id are read
// from JWT claims by the handler.
type TeamBetaRequest struct {
	Message string `json:"message"`
}

// HandleTeamBetaRequest returns the handler. Requires the platform
// gateway pool (INSERT on contact_requests) and the shared rate
// limiter (nil = local dev; falls open with a warn like the other
// public endpoints).
func HandleTeamBetaRequest(pool *pgxpool.Pool, limiter *ratelimit.RateLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok || claims == nil || claims.Subject == "" {
			writeJSONError(w, http.StatusUnauthorized, "session missing")
			return
		}
		email := strings.ToLower(strings.TrimSpace(claims.Email))
		if email == "" {
			// Defence-in-depth: every claims-issuing path populates
			// Email today, but if a future auth path skipped it we
			// don't want to end up with an empty-email row.
			writeJSONError(w, http.StatusBadRequest, "session missing email")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, teamBetaRequestMaxBodyBytes)

		var req TeamBetaRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid body")
			return
		}
		req.Message = strings.TrimSpace(req.Message)

		if utf8.RuneCountInString(req.Message) < teamBetaRequestMinMessageLen {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("message must be at least %d characters", teamBetaRequestMinMessageLen))
			return
		}
		if utf8.RuneCountInString(req.Message) > teamBetaRequestMaxMessageLen {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("message must be %d characters or fewer", teamBetaRequestMaxMessageLen))
			return
		}

		// Rate-limit per user_id (from the JWT sub). More specific
		// than "per email" — same account, no matter what email
		// address the profile has been renamed to.
		if limiter != nil {
			if blocked := ratelimit.CheckAuthRate(
				limiter, w, r.Context(),
				"team_beta_request_by_user", claims.Subject,
				teamBetaRequestRateLimitCount, teamBetaRequestRateLimitWindow,
			); blocked {
				return
			}
		} else {
			slog.Warn("team-beta request: rate limiter unavailable, request allowed unrated", "path", r.URL.Path)
		}

		// Best-effort display name: pull it from platform_users so
		// the Discord embed reads well ("Alice <a@…>"). Not fatal if
		// it's missing.
		var displayName *string
		_ = pool.QueryRow(r.Context(), `
			SELECT NULLIF(TRIM(display_name), '')
			  FROM public.platform_users
			 WHERE id = $1::uuid
		`, claims.Subject).Scan(&displayName)
		nameStr := ""
		if displayName != nil {
			nameStr = *displayName
		}

		userAgent := truncateRunes(r.Header.Get("User-Agent"), 500)
		var userAgentPtr *string
		if userAgent != "" {
			userAgentPtr = &userAgent
		}
		clientIP := ratelimit.ClientIP(r)

		insertCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		var id string
		err := pool.QueryRow(insertCtx, `
			INSERT INTO public.contact_requests
			    (name, email, message, source, user_agent, ip_address)
			VALUES ($1, $2, $3, 'team_beta_request', $4, $5::inet)
			RETURNING id
		`, displayName, email, req.Message, userAgentPtr, clientIP).Scan(&id)
		if err != nil {
			// Do NOT log the message (potentially PII use-case) or the
			// email. Client IP + user_id are enough for correlation.
			slog.Error("team-beta request: insert failed",
				"error", err,
				"user_id", claims.Subject,
				"client_ip", clientIP)
			writeJSONError(w, http.StatusInternalServerError, "internal error, please try again")
			return
		}

		notifyTeamBetaRequestAsync(nameStr, email, req.Message)

		slog.Info("team-beta access request received",
			"user_id", claims.Subject,
			"email", email)

		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"id":%q,"status":"received"}`, id)
	}
}
