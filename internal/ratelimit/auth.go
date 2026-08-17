package ratelimit

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Auth endpoint rate limit configuration.
const (
	SignupLimit          = 5
	SignupWindow         = 1 * time.Hour
	SigninFailLimit      = 5
	SigninFailWindow     = 15 * time.Minute
	ForgotPasswordLimit  = 3
	ForgotPasswordWindow = 15 * time.Minute
	ResendVerifyLimit    = 1
	ResendVerifyWindow   = 5 * time.Minute
	MagicLinkLimit       = 3
	MagicLinkWindow      = 15 * time.Minute
	PhoneOTPLimit        = 3
	PhoneOTPWindow       = 15 * time.Minute
)

// CheckAuthRate checks the rate limit for an auth action and writes a 429
// response if exceeded. Returns true if the request should be blocked.
// If the limiter is nil (Redis not configured), always allows.
//
// Platform-wide identifier-keyed action (forgot-password, magic-link,
// resend-verify, phone OTP, signin-fail). Per-project knobs use
// CheckAuthRateForProject instead.
func CheckAuthRate(limiter *RateLimiter, w http.ResponseWriter, ctx context.Context, action, identifier string, limit int, window time.Duration) bool {
	if limiter == nil {
		return false
	}

	key := fmt.Sprintf("auth:%s:%s", action, identifier)
	allowed, info, _ := limiter.Allow(ctx, key, limit, window)
	if !allowed {
		resetTime := time.Unix(info.ResetAt, 0)
		retryAfter := time.Until(resetTime).Seconds()
		if retryAfter < 1 {
			retryAfter = 1
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", fmt.Sprintf("%.0f", retryAfter))
		w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", info.Limit))
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", info.ResetAt))
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprintf(w, `{"error":"too many requests, try again in %.0f seconds"}`, retryAfter)
		return true
	}
	return false
}

// RecordSigninFailure increments the signin failure counter for an email
// without checking the limit. Called after a failed signin attempt so that
// successful logins don't consume the budget.
func RecordSigninFailure(limiter *RateLimiter, ctx context.Context, email string) {
	if limiter == nil {
		return
	}
	key := fmt.Sprintf("auth:signin_fail:%s", email)
	_, _, _ = limiter.Allow(ctx, key, SigninFailLimit, SigninFailWindow)
}

// CheckSigninFailRate checks whether the signin failure limit has been
// exceeded for the given email. Returns true if blocked.
func CheckSigninFailRate(limiter *RateLimiter, w http.ResponseWriter, ctx context.Context, email string) bool {
	return CheckAuthRate(limiter, w, ctx, "signin_fail", email, SigninFailLimit, SigninFailWindow)
}

// CheckAuthRateForProject is the per-project sibling of CheckAuthRate. The
// caller supplies the action's (limit, window) — typically resolved from
// the project's AuthConfig.EffectiveRateLimits() — and the projectID
// becomes part of the Redis key so two tenants never share a counter.
//
// Same fail-open behaviour as the platform helper: when the limiter is
// nil (Redis not configured, dev), every request is allowed.
//
// The identifier is the per-call dimension (usually an IP, sometimes an
// email/phone). The key shape is:
//
//	auth:{action}:project:{projectID}:{identifier}
//
// distinct from the platform-keyed
//
//	auth:{action}:{identifier}
//
// so legacy and per-project counters can coexist during the rollout
// window without aliasing each other.
func CheckAuthRateForProject(limiter *RateLimiter, w http.ResponseWriter, ctx context.Context, action, projectID, identifier string, limit int, window time.Duration) bool {
	if limiter == nil {
		return false
	}

	key := fmt.Sprintf("auth:%s:project:%s:%s", action, projectID, identifier)
	allowed, info, _ := limiter.Allow(ctx, key, limit, window)
	if !allowed {
		resetTime := time.Unix(info.ResetAt, 0)
		retryAfter := time.Until(resetTime).Seconds()
		if retryAfter < 1 {
			retryAfter = 1
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", fmt.Sprintf("%.0f", retryAfter))
		w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", info.Limit))
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", info.ResetAt))
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprintf(w, `{"error":"too many requests, try again in %.0f seconds"}`, retryAfter)
		return true
	}
	return false
}

// FiveMinutes is the canonical window used by the per-IP knobs on the
// Rate Limits page (signup+signin, token refresh, token verification).
// Centralised so a future change to the contract is one constant, not a
// search-and-replace across handlers.
const FiveMinutes = 5 * time.Minute

// ClientIP extracts the client IP from a request, preferring X-Forwarded-For.
//
// This is the legacy / platform-wide helper — it ALWAYS trusts the
// leftmost X-Forwarded-For entry. Per-project gates should use
// ClientIPForProject instead, which honours the project's `trust_proxy`
// knob (#228).
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return leftmostXFF(xff)
	}
	return remoteAddrNoPort(r)
}

// ClientIPForProject is the per-project sibling that honours
// auth_config.rate_limits.trust_proxy (#228) and
// auth_config.rate_limits.trusted_proxy_hops (#238).
//
//   - trustProxy=false → TCP peer only; XFF is ignored entirely.
//     Safe under any XFF configuration. The cost is that when the
//     gateway sits behind one shared hop (every request in Eurobase
//     prod arrives through one nginx pod), `r.RemoteAddr` is the
//     same value for every request — the per-IP gate effectively
//     becomes per-project total.
//
//   - trustProxy=true → **trusted-hop-count extraction (#238).**
//     Pick the XFF entry at index `len(entries) - trustedHops`
//     (0-indexed). Entries to the LEFT of that index are treated as
//     client-controlled and discarded. This is the difference between
//     the old leftmost-XFF behaviour (client can prepend arbitrary
//     entries and win) and the rightmost-with-known-N behaviour
//     (client-forged entries get pushed out of the trusted window
//     regardless of how many the attacker prepends).
//
//     Two supported shapes:
//       trustedHops=1 — nginx-ingress `use-forwarded-headers: false`,
//                       single XFF entry from the trusted proxy
//                       (via Scaleway LB proxy-protocol v2 or direct).
//       trustedHops=2 — LB and nginx both append their view of the
//                       source (LB appends real client, nginx appends
//                       LB's IP). Requires nginx
//                       `use-forwarded-headers: true`.
//
//     **Fail-closed**: fewer observed XFF entries than trustedHops
//     → return TCP peer instead of the leftmost entry. Catches
//     direct-to-gateway requests (shouldn't happen through our infra)
//     and header-strip attacks. A misconfigured `trustedHops` value
//     that's higher than the actual chain length becomes an
//     availability issue (all requests key on one TCP peer), not a
//     security issue.
//
// The Eurobase default is `TrustProxy=false, TrustedProxyHops=1`. See
// the field comments in internal/tenant/auth_config.go for the full
// trade-off walkthrough.
//
// trustedHops ≤ 0 is treated as 1 (belt + suspenders on top of the
// EffectiveRateLimits merge that already normalises ≤0 → default).
// An out-of-range value must not be usable as a bypass primitive.
func ClientIPForProject(r *http.Request, trustProxy bool, trustedHops int) string {
	if !trustProxy {
		return remoteAddrNoPort(r)
	}
	if trustedHops <= 0 {
		trustedHops = 1
	}
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return remoteAddrNoPort(r)
	}
	entries := splitXFF(xff)
	// entries indexed 0..len-1, left-to-right. Real client is at
	// index len-trustedHops (or before it, from the client's own
	// header). Anything earlier is untrusted.
	idx := len(entries) - trustedHops
	if idx < 0 {
		// Fewer observed entries than expected trusted hops — the
		// chain doesn't match our config assumption. Fail-closed to
		// TCP peer rather than trusting whatever leftmost entry
		// happens to be present.
		return remoteAddrNoPort(r)
	}
	return entries[idx]
}

// leftmostXFF returns the first entry of an X-Forwarded-For header,
// trimming any whitespace a comma-and-space separator would have left
// in. `"203.0.113.7, 10.0.0.5"` → `"203.0.113.7"`; a single-entry
// header is returned verbatim (also trimmed). Stable across ClientIP
// and ClientIPForProject so the extractor lives in one place.
func leftmostXFF(xff string) string {
	if i := indexByte(xff, ','); i >= 0 {
		return trimSpace(xff[:i])
	}
	return trimSpace(xff)
}

// splitXFF returns the comma-separated XFF entries, each trimmed of
// surrounding whitespace. Empty entries are dropped (an XFF like
// `"1.2.3.4,,5.6.7.8"` becomes two entries, not three) so a malformed
// header can't shift the trusted-hop index. `""` returns nil.
func splitXFF(xff string) []string {
	if xff == "" {
		return nil
	}
	out := make([]string, 0, 3)
	start := 0
	for i := 0; i < len(xff); i++ {
		if xff[i] == ',' {
			if v := trimSpace(xff[start:i]); v != "" {
				out = append(out, v)
			}
			start = i + 1
		}
	}
	if v := trimSpace(xff[start:]); v != "" {
		out = append(out, v)
	}
	return out
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

// remoteAddrNoPort returns r.RemoteAddr with any trailing ":port" stripped.
// Stable across both helpers so changes to address parsing (e.g. IPv6
// brackets) land in one place.
func remoteAddrNoPort(r *http.Request) string {
	addr := r.RemoteAddr
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i]
		}
	}
	return addr
}
