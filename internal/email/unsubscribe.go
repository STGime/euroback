package email

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Phase C of the public-beta launch plan (docs/public-beta-launch-plan.md).
// HMAC-signed opt-out tokens for outbound platform mail.
//
// Design goals:
//   - Stateless: no DB write on issue; verification is a single HMAC
//     check. Every drip / beta-update mail can carry a unique link.
//   - Expires: 90 days so links in an archived inbox eventually stop
//     working. GDPR-safe — resubscribe is a separate console action.
//   - Domain-separated from PLATFORM_JWT_SECRET: derived via
//     SHA-256(jwt || "|mailing_unsubscribe|v1") so a leaked
//     unsubscribe secret does not compromise session tokens (and
//     vice versa). No new env var required.
//   - Category-scoped: user opts out of "onboarding" or
//     "beta_updates" independently. Category is part of the signed
//     payload; a token for one category can't be swapped to
//     unsubscribe from another.

// UnsubscribeSigner signs + verifies opt-out tokens. Constructed
// from the platform's PLATFORM_JWT_SECRET at startup via
// NewUnsubscribeSigner.
type UnsubscribeSigner struct {
	key []byte
}

// NewUnsubscribeSigner derives a domain-separated HMAC key from the
// platform JWT secret. A leak of one secret does not leak the other
// because the derivation is one-way.
func NewUnsubscribeSigner(platformJWTSecret string) *UnsubscribeSigner {
	sum := sha256.Sum256([]byte(platformJWTSecret + "|mailing_unsubscribe|v1"))
	return &UnsubscribeSigner{key: sum[:]}
}

// UnsubscribeTokenTTL is how long an issued opt-out link stays
// valid. 90 days = a reasonable inbox-archive window; past that,
// users can visit the console + resubscribe / re-unsubscribe.
const UnsubscribeTokenTTL = 90 * 24 * time.Hour

// Sign returns an opaque base64 token encoding (user_id, category,
// expires). Format: `userID|category|expiresUnix|base64HMAC`, then
// the whole thing base64-url-encoded so it survives as a query
// parameter without percent-escaping.
func (s *UnsubscribeSigner) Sign(userID, category string, expires time.Time) string {
	payload := userID + "|" + category + "|" + strconv.FormatInt(expires.Unix(), 10)
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	full := payload + "|" + sig
	return base64.RawURLEncoding.EncodeToString([]byte(full))
}

// Verify decodes + validates a token issued by Sign. Returns the
// user_id + category on success. Errors on: malformed token, bad
// signature, or expired timestamp.
func (s *UnsubscribeSigner) Verify(token string) (userID, category string, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", "", ErrInvalidUnsubscribeToken
	}
	parts := strings.SplitN(string(raw), "|", 4)
	if len(parts) != 4 {
		return "", "", ErrInvalidUnsubscribeToken
	}
	userID, category = parts[0], parts[1]
	expiresUnix, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return "", "", ErrInvalidUnsubscribeToken
	}
	// Verify signature FIRST (constant-time) so a wrong-signature
	// attacker can't distinguish "expired but valid" from "expired
	// and forged" via timing.
	payload := parts[0] + "|" + parts[1] + "|" + parts[2]
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(payload))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[3])) {
		return "", "", ErrInvalidUnsubscribeToken
	}
	if time.Now().Unix() > expiresUnix {
		return "", "", ErrExpiredUnsubscribeToken
	}
	// Category allowlist — matches the CHECK constraint on
	// mailing_preferences (migration 000077). A signature-valid
	// token for an unknown category is treated as invalid; either
	// we misconfigured or someone forged with a stale key.
	switch category {
	case "onboarding", "beta_updates", "usage_alerts", "all":
		// ok
	default:
		return "", "", ErrInvalidUnsubscribeToken
	}
	return userID, category, nil
}

// BuildUnsubscribeURL constructs the absolute opt-out URL that goes
// in the mail footer. `baseURL` is the platform's public gateway
// URL (e.g. https://api.eurobase.app), typically read from env.
func BuildUnsubscribeURL(signer *UnsubscribeSigner, baseURL, userID, category string) string {
	token := signer.Sign(userID, category, time.Now().Add(UnsubscribeTokenTTL))
	base := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf("%s/platform/mailing/unsubscribe?token=%s", base, token)
}

// Sentinel errors — the HTTP handler distinguishes these so the
// user-facing page can render "link expired" vs "link invalid"
// differently.
var (
	ErrInvalidUnsubscribeToken = errors.New("invalid unsubscribe token")
	ErrExpiredUnsubscribeToken = errors.New("unsubscribe link has expired")
)
