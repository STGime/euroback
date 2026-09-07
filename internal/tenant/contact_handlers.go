package tenant

// Public marketing-site contact form.
//
// Route: POST /platform/public/contact  (NO auth — this is a
// widget on eurobase.app that anonymous visitors interact with).
// The security surface is entirely in this handler + migration 000112:
//
//   - Rate-limited per IP (5 / hour) AND per email (3 / hour). Both
//     apply — a distributed spammer can't burn one email's budget
//     from many IPs, and a single IP can't blast many emails.
//   - Request body capped by http.MaxBytesReader before JSON decode
//     so an anonymous caller can't push a 50 MB body (nginx cap) into
//     a runaway allocation.
//   - Email is canonicalised via mail.ParseAddress().Address so
//     "Alice <a@b.com>" and "Bob <a@b.com>" both hit the same
//     rate-limit key + land as one row per address in the DB.
//   - Field validation matches the DB CHECK constraints, so a
//     bypassed validator lands as a clean 400 rather than a 500 on
//     the pgx driver.
//   - Discord notification is fire-and-forget in a goroutine —
//     never blocks the response even if Discord is down.
//   - Inserted under the eurobase_gateway pool, which only holds
//     INSERT on contact_requests (migration 000112 REVOKEs the
//     default SELECT/UPDATE/DELETE grant first — see the migration's
//     #443-class-pitfall comment). No SELECT grant means a runtime
//     SQL-injection cannot exfiltrate historical submissions.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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

// contactMaxRequestBytes caps the whole JSON body before decode.
// 32 KiB is generous for the largest legit payload: 5,000-char
// message (~20 KiB in worst-case UTF-8) + 200-char name + 254-char
// email + JSON overhead. Rejects with 413 Request Entity Too Large
// before the parser allocates anything.
const contactMaxRequestBytes = 32 * 1024

// PublicContactRequest is the JSON body of POST /platform/public/contact.
type PublicContactRequest struct {
	Name    string `json:"name,omitempty"` // optional
	Email   string `json:"email"`
	Message string `json:"message"`
}

// HandlePublicContactRequest is the marketing-widget entry point.
// Requires the shared rate limiter (nil = local dev, allowed
// silently with a warn) and the gateway pool (INSERT-only on
// contact_requests per migration 000112).
func HandlePublicContactRequest(pool *pgxpool.Pool, limiter *ratelimit.RateLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Cap the body before we touch the JSON parser — public
		// endpoint on an ingress that allows 50 MB requests.
		r.Body = http.MaxBytesReader(w, r.Body, contactMaxRequestBytes)

		var req PublicContactRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			// MaxBytesReader wraps its own overflow error type; the
			// message is fine to surface as a 400 either way.
			writeJSONError(w, http.StatusBadRequest, "invalid body")
			return
		}

		req.Name = strings.TrimSpace(req.Name)
		req.Email = strings.ToLower(strings.TrimSpace(req.Email))
		req.Message = strings.TrimSpace(req.Message)

		if err := validateContactRequest(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		// Rate-limit gate — IP first (cheapest to enumerate), then
		// email. The IP key trusts leftmost-XFF, which is safe here
		// because deploy/k8s/nginx-ingress-config.yaml sets
		// use-forwarded-headers: "false" — nginx OVERWRITES the
		// client-supplied XFF with a single trusted entry before it
		// reaches the gateway. If that ingress config ever flips,
		// this rate limit becomes trivially bypassable.
		clientIP := ratelimit.ClientIP(r)
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
		} else {
			// Convention across the codebase (see signup handler): a
			// missing limiter falls open, not closed — a Redis outage
			// shouldn't take signups + contact form down. Log warn so
			// ops sees the unrate-limited window in the aggregator.
			slog.Warn("contact form: rate limiter unavailable, request allowed unrated", "path", r.URL.Path)
		}

		var name *string
		if req.Name != "" {
			name = &req.Name
		}
		userAgent := truncateRunes(r.Header.Get("User-Agent"), 500)
		var userAgentPtr *string
		if userAgent != "" {
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
			// Do NOT log req.Email — it's PII, and this line runs on
			// the log-noise path. We DO include the client IP because
			// that's already captured in ip_address and helps ops
			// correlate an outage across sources.
			slog.Error("contact form: insert failed", "error", err, "client_ip", clientIP)
			writeJSONError(w, http.StatusInternalServerError, "internal error, please try again")
			return
		}

		notifyContactAsync(req.Name, req.Email, req.Message)

		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"id":%q,"status":"received"}`, id)
	}
}

// writeJSONError is a small helper because http.Error unconditionally
// overwrites Content-Type back to text/plain — we want the whole
// error surface to stay under application/json for consistent client
// parsing.
func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}

// truncateRunes clips s to at most n runes, so a UTF-8 boundary is
// never cut mid-codepoint. Plain s[:n] would produce invalid UTF-8
// if a multibyte rune straddles the cut, which Discord rejects (400)
// and PG stores as garbage.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n])
}

// validateContactRequest enforces the bounds that also live as DB
// CHECKs. Rune-count where the DB uses char_length() so multibyte
// characters count consistently (an "OÜ" name is 2 chars, not 3).
//
// Also canonicalises Email in-place to `addr.Address` so the
// rate-limit key + row value use the same address for
// "Alice <a@b.com>" and "Bob <a@b.com>" — see the review-round
// bypass finding on the initial version.
func validateContactRequest(req *PublicContactRequest) error {
	if req.Email == "" {
		return fmt.Errorf("email is required")
	}
	if utf8.RuneCountInString(req.Email) > contactMaxEmailLen {
		return fmt.Errorf("email must be %d characters or fewer", contactMaxEmailLen)
	}
	addr, err := mail.ParseAddress(req.Email)
	if err != nil {
		return fmt.Errorf("email is not a valid address")
	}
	// Canonicalise: strip display-name form, lowercase. Bounds re-checked
	// on the canonical form because the display-name expansion could in
	// theory shrink AND grow the address slot (empty display-name +
	// angle brackets vs bare address).
	req.Email = strings.ToLower(addr.Address)
	if utf8.RuneCountInString(req.Email) > contactMaxEmailLen {
		return fmt.Errorf("email must be %d characters or fewer", contactMaxEmailLen)
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
