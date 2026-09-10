package tenant

// Team-tier priority-support ticket surface.
//
// Route: POST /platform/support/request  (authenticated + Team-tier gated).
// Shape mirrors contact_handlers.go but with:
//   - team_beta_access enforcement (403 for non-Team callers)
//   - Per-user AND per-IP rate limits (auth'd users, so per-user is
//     the primary key; IP kept as belt-and-braces)
//   - Extra fields — subject, category ('question'|'bug'|'billing'|
//     'feature'|'other'), priority ('normal'|'urgent'), optional
//     org_id / project_id context
//   - Longer message ceiling (10,000 chars) — Team callers routinely
//     paste stack traces + queries
//   - Insert under the developer pool (support_requests grants sit
//     on eurobase_developer, not eurobase_gateway — see migration 115).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/ratelimit"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Rate limits — a bit looser than the anonymous contact form because
// the caller is authenticated + on a paying tier. A real user files a
// handful of tickets a day at most; 15/hour per user is generous for
// legit use but stops a script.
const (
	supportRateLimitByUserCount  = 15
	supportRateLimitByUserWindow = 1 * time.Hour
	supportRateLimitByIPCount    = 30
	supportRateLimitByIPWindow   = 1 * time.Hour
)

const (
	supportMaxSubjectLen = 200
	supportMaxMessageLen = 10000
	supportMaxRequestBytes = 64 * 1024
)

// Category + priority sets. Keep in sync with the DB CHECK constraint
// in migration 000115. A mismatch surfaces as a 500 on the pgx driver
// rather than a clean 400.
var (
	supportCategories = map[string]bool{
		"question": true,
		"bug":      true,
		"billing":  true,
		"feature":  true,
		"other":    true,
	}
	supportPriorities = map[string]bool{
		"normal": true,
		"urgent": true,
	}
)

// SupportRequestBody is the JSON payload of the submit endpoint.
type SupportRequestBody struct {
	Subject   string  `json:"subject"`
	Message   string  `json:"message"`
	Category  string  `json:"category,omitempty"`  // default: "question"
	Priority  string  `json:"priority,omitempty"`  // default: "normal"
	OrgID     *string `json:"org_id,omitempty"`
	ProjectID *string `json:"project_id,omitempty"`
}

// SupportHandler wraps the developer pool + rate limiter for the
// submit endpoint. Constructed once at gateway startup and shared.
type SupportHandler struct {
	Pool    *pgxpool.Pool
	Limiter *ratelimit.RateLimiter
}

// HandleSubmitSupportRequest — POST /platform/support/request
func (h *SupportHandler) HandleSubmitSupportRequest() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok || claims == nil || claims.Subject == "" {
			writeJSONErr(w, http.StatusUnauthorized, "session missing")
			return
		}
		userID := claims.Subject
		callerEmail := strings.ToLower(strings.TrimSpace(claims.Email))

		// Team-tier gate. Fresh DB read so revocation via
		// AdminRevokeTeamBeta takes effect immediately.
		granted, err := UserHasTeamBetaAccess(r.Context(), h.Pool, userID)
		if err != nil {
			slog.Error("support: tier check failed", "error", err, "user_id", userID)
			writeJSONErr(w, http.StatusInternalServerError, "tier check failed")
			return
		}
		if !granted {
			writeJSONErr(w, http.StatusForbidden, "priority support is a Team-tier feature — upgrade to file a ticket")
			return
		}

		// Cap body BEFORE parse so a malicious client can't push a
		// 50 MB body (nginx ingress cap) into a runaway allocation.
		r.Body = http.MaxBytesReader(w, r.Body, supportMaxRequestBytes)

		var body SupportRequestBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		body.Subject = strings.TrimSpace(body.Subject)
		body.Message = strings.TrimSpace(body.Message)
		body.Category = strings.TrimSpace(body.Category)
		body.Priority = strings.TrimSpace(body.Priority)
		if body.Category == "" {
			body.Category = "question"
		}
		if body.Priority == "" {
			body.Priority = "normal"
		}
		if err := validateSupportRequest(&body); err != nil {
			writeJSONErr(w, http.StatusBadRequest, err.Error())
			return
		}

		clientIP := ratelimit.ClientIP(r)
		if h.Limiter != nil {
			if blocked := ratelimit.CheckAuthRate(
				h.Limiter, w, r.Context(),
				"support_by_user", userID,
				supportRateLimitByUserCount, supportRateLimitByUserWindow,
			); blocked {
				return
			}
			if blocked := ratelimit.CheckAuthRate(
				h.Limiter, w, r.Context(),
				"support_by_ip", clientIP,
				supportRateLimitByIPCount, supportRateLimitByIPWindow,
			); blocked {
				return
			}
		} else {
			// Falls open on missing limiter (matches contact form
			// convention). Redis outage shouldn't take support down.
			slog.Warn("support form: rate limiter unavailable, request allowed unrated", "path", r.URL.Path)
		}

		userAgent := truncateRunes(r.Header.Get("User-Agent"), 500)
		var userAgentPtr *string
		if userAgent != "" {
			userAgentPtr = &userAgent
		}

		// Resolve email — JWT claims can be stale if a user changed
		// email between token issue and now. Prefer a fresh DB read
		// so the ticket carries the *current* address. Fall back to
		// the JWT claim on non-fatal lookup outcomes so ops still gets
		// the ping. `errSSOUserNotProvisioned`-style ErrNoRows is a
		// legitimate empty; any OTHER error (timeout, permission, etc.)
		// is worth logging so a systematic stale-email bug is
		// observable in aggregation.
		email := callerEmail
		freshEmail, err := lookupUserEmail(r.Context(), h.Pool, userID)
		if err != nil {
			slog.Warn("support: fresh email lookup failed, using JWT claim", "error", err, "user_id", userID)
		} else if freshEmail != "" {
			email = freshEmail
		}
		if email == "" {
			// Shouldn't happen for a JWT-verified caller; guard so we
			// don't insert an empty email row (violates CHECK).
			writeJSONErr(w, http.StatusInternalServerError, "user email lookup failed")
			return
		}

		// Verify optional org / project context against caller membership.
		// The FK constraint just checks existence; without this check a
		// Team user could probe UUIDs (200 vs 500) or spam ops with
		// unrelated org/project ids attached to their ticket. Null out
		// on any negative — the ticket still submits, just without
		// the context line in the Discord embed.
		if body.OrgID != nil && *body.OrgID != "" {
			isMember, err := userIsOrgMember(r.Context(), h.Pool, userID, *body.OrgID)
			if err != nil {
				slog.Warn("support: org membership check failed", "error", err, "user_id", userID, "org_id", *body.OrgID)
				body.OrgID = nil
			} else if !isMember {
				body.OrgID = nil
			}
		}
		if body.ProjectID != nil && *body.ProjectID != "" {
			isOwner, err := userIsProjectMember(r.Context(), h.Pool, userID, *body.ProjectID)
			if err != nil {
				slog.Warn("support: project membership check failed", "error", err, "user_id", userID, "project_id", *body.ProjectID)
				body.ProjectID = nil
			} else if !isOwner {
				body.ProjectID = nil
			}
		}

		insertCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		var id string
		err = h.Pool.QueryRow(insertCtx, `
			INSERT INTO public.support_requests
			    (platform_user_id, email, subject, message, category, priority,
			     org_id, project_id, user_agent, ip_address)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7::uuid, $8::uuid, $9, $10::inet)
			RETURNING id
		`, userID, email, body.Subject, body.Message, body.Category, body.Priority,
			body.OrgID, body.ProjectID, userAgentPtr, clientIP).Scan(&id)
		if err != nil {
			slog.Error("support: insert failed", "error", err, "user_id", userID)
			writeJSONErr(w, http.StatusInternalServerError, "internal error, please try again")
			return
		}

		notifySupportAsync(email, body.Subject, body.Message, body.Category, body.Priority, body.OrgID, body.ProjectID)

		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"id":%q,"status":"received"}`, id)
	}
}

// validateSupportRequest enforces the same bounds as the DB CHECK.
func validateSupportRequest(b *SupportRequestBody) error {
	if utf8.RuneCountInString(b.Subject) == 0 {
		return errors.New("subject is required")
	}
	if utf8.RuneCountInString(b.Subject) > supportMaxSubjectLen {
		return fmt.Errorf("subject must be %d characters or fewer", supportMaxSubjectLen)
	}
	if utf8.RuneCountInString(b.Message) == 0 {
		return errors.New("message is required")
	}
	if utf8.RuneCountInString(b.Message) > supportMaxMessageLen {
		return fmt.Errorf("message must be %d characters or fewer", supportMaxMessageLen)
	}
	if !supportCategories[b.Category] {
		return fmt.Errorf("category must be one of question, bug, billing, feature, other")
	}
	if !supportPriorities[b.Priority] {
		return fmt.Errorf("priority must be one of normal, urgent")
	}
	return nil
}

// lookupUserEmail — fresh read of platform_users.email so a ticket
// carries the current address even if the JWT was issued before an
// email change. `ErrNoRows` is a legitimate empty (caller sees no
// row → returns "", nil). Any other error surfaces to the caller
// so the handler can log it.
func lookupUserEmail(ctx context.Context, pool *pgxpool.Pool, userID string) (string, error) {
	var email string
	err := pool.QueryRow(ctx,
		`SELECT email FROM public.platform_users WHERE id = $1::uuid`,
		userID,
	).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.ToLower(strings.TrimSpace(email)), nil
}

// userIsOrgMember reports whether the caller is in the org's member
// list. Used to gate the optional org_id context field so a Team
// user can't attach an unrelated org id to their ticket.
func userIsOrgMember(ctx context.Context, pool *pgxpool.Pool, userID, orgID string) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM public.org_members
			 WHERE org_id = $1::uuid AND platform_user_id = $2::uuid
		)
	`, orgID, userID).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// userIsProjectMember reports whether the caller can see the project
// via the same access rules ListProjects uses — owner_id OR
// project_members. Deliberately does NOT extend to org-membership
// because that path is Phase-2 (see follow-up issue #539); org
// context should be supplied via org_id instead.
func userIsProjectMember(ctx context.Context, pool *pgxpool.Pool, userID, projectID string) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM public.projects p
			 WHERE p.id = $1::uuid AND (
			     p.owner_id = $2::uuid
			     OR EXISTS(SELECT 1 FROM public.project_members pm
			                WHERE pm.project_id = p.id AND pm.user_id = $2::uuid)
			 )
		)
	`, projectID, userID).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}
