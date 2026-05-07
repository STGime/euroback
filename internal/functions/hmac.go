package functions

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Closes layer 3 of advisory GHSA-7428-mvpp-rhr7 (C3): authenticated
// gateway → runner traffic.
//
// Without this layer, anything inside the cluster (compromised pod,
// side-car, future ingress mistake) could call `functions:8000/invoke`
// directly with forged `X-Project-ID` / `X-Schema-Name` / `X-User-ID`
// headers and run arbitrary functions in arbitrary tenants — bypassing
// the gateway's authentication entirely. Layer 1 (per-tenant DB role)
// limits SQL reach but the attacker still gets to execute any function
// in any tenant.
//
// With this layer, the gateway HMAC-signs the identity headers using a
// shared secret only the gateway and runner know. The runner verifies
// before it does anything else. A cluster-internal attacker without the
// secret cannot forge a valid request.
//
// Signature scheme: HMAC-SHA256 over a canonical message containing the
// signed headers + a unix timestamp. Timestamp ±5 minutes is accepted
// to allow for clock skew while preventing replay long after capture.

const (
	// signedHeaderTimestamp is the timestamp header the gateway adds and
	// the runner reads. Unix seconds in decimal.
	signedHeaderTimestamp = "X-Eurobase-Timestamp"
	// signedHeaderSignature is the hex-encoded HMAC-SHA256 of the
	// canonical message.
	signedHeaderSignature = "X-Eurobase-Signature"
	// minSecretLen is the minimum acceptable shared-secret length in
	// bytes. 32 bytes = 256 bits, matching the SHA-256 block-equivalent
	// security boundary.
	minSecretLen = 32
)

// Signer signs gateway → runner requests with HMAC-SHA256.
type Signer struct {
	secret []byte
}

// NewSigner constructs a Signer. Returns an error if the secret is
// shorter than 32 bytes — short secrets reduce the entropy of the
// HMAC's effective key and trivialise brute-forcing.
func NewSigner(secret string) (*Signer, error) {
	if len(secret) < minSecretLen {
		return nil, fmt.Errorf("functions runner HMAC secret must be at least %d bytes", minSecretLen)
	}
	return &Signer{secret: []byte(secret)}, nil
}

// Sign attaches X-Eurobase-Timestamp and X-Eurobase-Signature headers
// to the request. The timestamp is unix seconds at sign time; the
// signature is HMAC-SHA256 over the canonical message built from the
// already-set identity headers + that timestamp.
//
// All identity headers (X-Project-ID, X-Schema-Name, etc.) must be
// set BEFORE calling Sign. Modifying them after invalidates the
// signature.
func (s *Signer) Sign(h http.Header, now time.Time) {
	ts := strconv.FormatInt(now.Unix(), 10)
	h.Set(signedHeaderTimestamp, ts)
	msg := canonicalMessage(h, ts)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(msg))
	h.Set(signedHeaderSignature, hex.EncodeToString(mac.Sum(nil)))
}

// VerifyOptions configures verification.
type VerifyOptions struct {
	// MaxClockSkew is the maximum allowed difference between the
	// timestamp in the request and the verifier's local time. Recommended:
	// 5 * time.Minute. Zero defaults to that.
	MaxClockSkew time.Duration
	// Now overrides time.Now (test-only).
	Now func() time.Time
}

// ErrMissingSignature is returned when the request has no signature
// headers at all.
var ErrMissingSignature = errors.New("missing signature headers")

// ErrTimestampOutOfWindow is returned when the request's timestamp is
// outside the allowed clock-skew window (replay-or-very-skewed).
var ErrTimestampOutOfWindow = errors.New("timestamp out of allowed window")

// ErrSignatureMismatch is returned when the HMAC doesn't verify.
var ErrSignatureMismatch = errors.New("signature mismatch")

// Verify checks the request's signature. Implements the same scheme as
// Sign. Errors are sentinel values so callers can distinguish missing
// signature (e.g. soft-mode warn) from bad signature (always reject).
//
// Constant-time comparison via hmac.Equal protects against timing-side-
// channel signature forgery.
func (s *Signer) Verify(h http.Header, opts VerifyOptions) error {
	skew := opts.MaxClockSkew
	if skew == 0 {
		skew = 5 * time.Minute
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}

	ts := h.Get(signedHeaderTimestamp)
	sig := h.Get(signedHeaderSignature)
	if ts == "" || sig == "" {
		return ErrMissingSignature
	}

	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: invalid timestamp", ErrTimestampOutOfWindow)
	}
	delta := now().Unix() - tsInt
	if delta < 0 {
		delta = -delta
	}
	if time.Duration(delta)*time.Second > skew {
		return ErrTimestampOutOfWindow
	}

	expected := hmac.New(sha256.New, s.secret)
	expected.Write([]byte(canonicalMessage(h, ts)))
	expectedHex := hex.EncodeToString(expected.Sum(nil))

	// hmac.Equal is constant-time over equal-length inputs.
	if !hmac.Equal([]byte(sig), []byte(expectedHex)) {
		return ErrSignatureMismatch
	}
	return nil
}

// canonicalMessage builds the deterministic byte string the HMAC is
// computed over. Field order is fixed — must match the runner side
// (functions-runner/hmac.ts) byte-for-byte.
//
// Format:
//
//	v=1
//	ts=<unix-seconds>
//	project=<X-Project-ID>
//	schema=<X-Schema-Name>
//	function=<X-Function-ID>
//	user=<X-User-ID, or empty>
//	email=<X-User-Email, or empty>
//	plan=<X-Plan>
//	requestid=<X-Request-ID>
//
// Newlines are LF. Empty values are emitted as empty strings, NOT
// omitted, so a forged header that adds an unset value can't mutate
// the canonical form to match a different signature.
func canonicalMessage(h http.Header, ts string) string {
	return "v=1\n" +
		"ts=" + ts + "\n" +
		"project=" + h.Get("X-Project-ID") + "\n" +
		"schema=" + h.Get("X-Schema-Name") + "\n" +
		"function=" + h.Get("X-Function-ID") + "\n" +
		"user=" + h.Get("X-User-ID") + "\n" +
		"email=" + h.Get("X-User-Email") + "\n" +
		"plan=" + h.Get("X-Plan") + "\n" +
		"requestid=" + h.Get("X-Request-ID")
}
