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
// auth_config.rate_limits.trust_proxy (#228).
//
//   - trustProxy=true  → leftmost X-Forwarded-For (fall back to TCP
//     peer if XFF is absent). Correct only when exactly one trusted
//     hop authoritatively overwrites XFF with the real client IP
//     (nginx-ingress with `use-forwarded-headers: false` is the
//     canonical example). If anything in the chain APPENDS to XFF
//     instead of overwriting, leftmost-XFF becomes client-controlled
//     and the per-IP gate becomes header-rotation-bypassable.
//
//   - trustProxy=false → TCP peer only; XFF is ignored entirely.
//     Safe under any XFF configuration. The cost is that when the
//     gateway sits behind one shared hop (every request in Eurobase
//     prod arrives through one nginx pod), `r.RemoteAddr` is the
//     same value for every request — the per-IP gate effectively
//     becomes per-project total.
//
// The Eurobase default is `false` for safety until the Scaleway LB ↔
// nginx-ingress XFF behavior is verified empirically (follow-up issue
// tracks the verification + the trusted-hop-count hardening that's
// robust to both modes). Project owners who know their deployment
// trusts XFF can opt in today; the field comment in
// internal/tenant/auth_config.go walks through the full decision.
func ClientIPForProject(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return leftmostXFF(xff)
		}
	}
	return remoteAddrNoPort(r)
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
