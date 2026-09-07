package tenant

// Public marketing-site contact form.
//
// Route: POST /platform/public/contact  (NO auth — this is a
// widget on eurobase.app that anonymous visitors interact with).
// The security surface is entirely in this handler:
//
//   - Rate-limited per IP (5 / hour) AND per email (3 / hour). Both
//     apply — a distributed spammer can't burn one email's budget
//     from many IPs, and a single IP can't blast many emails.
//   - Field validation matches the DB CHECK constraints, so a
//     bypassed validator lands as a clean 400 rather than a 500 on
//     the pgx driver.
//   - Discord notification is fire-and-forget in a goroutine —
//     never blocks the response even if Discord is down.
//   - Inserted under the eurobase_gateway pool, which only holds
//     INSERT on contact_requests (per migration 000112). No SELECT
//     grant = a runtime SQL-injection cannot exfiltrate historical
//     submissions.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eurobase/euroback/internal/ratelimit"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Rate limits — deliberately tight. A real user contacts us once,
// maybe twice; anything above that pattern is either a form-fumble
// (they'll re-submit within seconds and hit the limit briefly) or a
// bot campaign.
const (
	contactRateLimitByIPCount     = 5
	contactRateLimitByIPWindow    = 1 * time.Hour
	contactRateLimitByEmailCount  = 3
	contactRateLimitByEmailWindow = 1 * time.Hour
)

// Field bounds match the DB CHECK constraints in migration 000112.
// Keep them in sync; if the DB rejects a row we accepted, the caller
// sees a 500 which is a bad experience for a legit user.
const (
	contactMaxNameLen    = 200
	contactMaxEmailLen   = 254 // RFC 5321
	contactMaxMessageLen = 5000
	contactMinMessageLen = 1
)

// PublicContactRequest is the JSON body of POST /platform/public/contact.
type PublicContactRequest struct {
	Name    string `json:"name,omitempty"` // optional
	Email   string `json:"email"`
	Message string `json:"message"`
}

// HandlePublicContactRequest is the marketing-widget entry point.
// Requires the shared rate limiter (nil = local dev, allowed
// silently) and the gateway pool (INSERT-only on contact_requests).
func HandlePublicContactRequest(pool *pgxpool.Pool, limiter *ratelimit.RateLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		var req PublicContactRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
			return
		}

		req.Name = strings.TrimSpace(req.Name)
		req.Email = strings.ToLower(strings.TrimSpace(req.Email))
		req.Message = strings.TrimSpace(req.Message)

		if err := validateContactRequest(&req); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}

		// Rate-limit gate — IP first (cheapest to enumerate), then
		// email. 429 body carries the field so the client can show a
		// specific message ("please wait an hour before sending
		// another message" vs "too many contact requests from your
		// network").
		clientIP := extractClientIP(r)
		if limiter != nil {
			if blocked := ratelimit.CheckAuthRate(
				limiter, w, r.Context(),
				"contact_by_ip", clientIP,
				contactRateLimitByIPCount, contactRateLimitByIPWindow,
			); blocked {
				return
			}
			if blocked := ratelimit.CheckAuthRate(
				limiter, w, r.Context(),
				"contact_by_email", req.Email,
				contactRateLimitByEmailCount, contactRateLimitByEmailWindow,
			); blocked {
				return
			}
		}

		var name *string
		if req.Name != "" {
			name = &req.Name
		}
		userAgent := r.Header.Get("User-Agent")
		var userAgentPtr *string
		if userAgent != "" {
			if len(userAgent) > 500 {
				userAgent = userAgent[:500]
			}
			userAgentPtr = &userAgent
		}

		insertCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		var id string
		err := pool.QueryRow(insertCtx, `
			INSERT INTO public.contact_requests
			    (name, email, message, source, user_agent, ip_address)
			VALUES ($1, $2, $3, 'marketing_site', $4, $5::inet)
			RETURNING id
		`, name, req.Email, req.Message, userAgentPtr, clientIP).Scan(&id)
		if err != nil {
			slog.Error("contact form: insert failed", "error", err, "email", req.Email)
			http.Error(w, `{"error":"internal error, please try again"}`, http.StatusInternalServerError)
			return
		}

		notifyContactAsync(req.Name, req.Email, req.Message)

		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"id":%q,"status":"received"}`, id)
	}
}

// validateContactRequest enforces the bounds that also live as DB
// CHECKs. Rune-count where the DB uses char_length() so multibyte
// characters count consistently (an "OÜ" name is 2 chars, not 3).
func validateContactRequest(req *PublicContactRequest) error {
	if req.Email == "" {
		return fmt.Errorf("email is required")
	}
	if utf8.RuneCountInString(req.Email) > contactMaxEmailLen {
		return fmt.Errorf("email must be %d characters or fewer", contactMaxEmailLen)
	}
	if _, err := mail.ParseAddress(req.Email); err != nil {
		return fmt.Errorf("email is not a valid address")
	}
	if utf8.RuneCountInString(req.Message) < contactMinMessageLen {
		return fmt.Errorf("message is required")
	}
	if utf8.RuneCountInString(req.Message) > contactMaxMessageLen {
		return fmt.Errorf("message must be %d characters or fewer", contactMaxMessageLen)
	}
	if req.Name != "" && utf8.RuneCountInString(req.Name) > contactMaxNameLen {
		return fmt.Errorf("name must be %d characters or fewer", contactMaxNameLen)
	}
	return nil
}

// extractClientIP prefers the platform's Envoy-forwarded original
// client IP (X-Forwarded-For, leftmost non-private) and falls back
// to r.RemoteAddr. Kept small — this handler is public so the
// header can't be trusted for security decisions, only for
// bucketing rate-limit keys and enriching the audit row.
func extractClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			// Return the first parseable address. Even a private
			// range is a useful rate-limit key from behind an
			// enterprise NAT.
			if ip := net.ParseIP(p); ip != nil {
				return ip.String()
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
