package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrQuotaExceeded is returned by CheckProjectHourlyQuota when a project
// has hit its per-hour cap for an action (typically a send: email or
// SMS). The handler chain maps it to a 429 with Retry-After.
//
// errors.Is on this sentinel works for callers that want to surface a
// typed 429 vs. unwrapping the underlying TEM/GatewayAPI error.
var ErrQuotaExceeded = errors.New("project hourly quota exceeded")

// CheckProjectHourlyQuota gates an action on a per-project per-hour
// counter, distinct from the per-IP CheckAuthRateForProject limiter.
// Used by email and SMS send paths (#227) where the contract is "this
// project may issue at most N <action> events per hour, regardless of
// triggering IP or end-user".
//
// Key shape: quota:{action}:project:{projectID}:hour:{epoch_hour}
// Window: fixed 1 hour; the counter naturally resets at the hour
// boundary (Redis TTL).
//
// Same fail-open contract as CheckAuthRateForProject: a nil limiter
// (Redis not configured, dev) lets every call through and returns
// (0, nil). When the cap is hit, returns (retryAfterSec,
// ErrQuotaExceeded). retryAfterSec is the seconds until the hour
// boundary — clients can wait it out cleanly.
//
// IMPORTANT: this increments the counter atomically on every call,
// just like CheckAuthRateForProject. Callers MUST bail out on a non-
// nil err — they must NOT proceed to the send and pretend the call
// didn't happen, because the counter has already moved. A nil err
// means "you may send"; ErrQuotaExceeded means "do not send".
func CheckProjectHourlyQuota(limiter *RateLimiter, ctx context.Context, action, projectID string, limit int) (retryAfterSec int, err error) {
	if limiter == nil {
		return 0, nil
	}
	key := fmt.Sprintf("quota:%s:project:%s", action, projectID)
	allowed, info, allowErr := limiter.Allow(ctx, key, limit, time.Hour)
	if allowErr != nil {
		// Redis hiccup → fail-open (same as CheckAuthRateForProject).
		// A platform-wide quota outage should not silently disable
		// every email send; the per-hour ceiling is a self-DoS
		// guard, not a security control.
		return 0, nil
	}
	if !allowed {
		retryAfterSec = int(time.Until(time.Unix(info.ResetAt, 0)).Seconds())
		if retryAfterSec < 1 {
			retryAfterSec = 1
		}
		return retryAfterSec, ErrQuotaExceeded
	}
	return 0, nil
}
