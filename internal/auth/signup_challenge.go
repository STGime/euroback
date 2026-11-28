package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	altcha "github.com/altcha-org/altcha-lib-go/v2"
)

// Signup proof-of-work check (ALTCHA, self-hosted). The console asks the
// gateway for a signed challenge, the browser searches for the counter that
// solves it (about a thousand PBKDF2 derivations, a second or two), and the
// signup request carries the solution. It makes each signup cost the client
// CPU time; it does not prove a human. Nothing leaves the gateway: no third
// party, no cookie.

// ErrSignupChallenge refuses a signup without a valid, unused solution.
var ErrSignupChallenge = errors.New("the browser check didn't complete — please reload the page and try again")

const (
	signupChallengeAlgorithm = "PBKDF2/SHA-256"
	// signupChallengeCost is the PBKDF2 iteration count of one try.
	signupChallengeCost = 5000
	// signupChallengeTTL covers solving plus filling in the form.
	signupChallengeTTL = 10 * time.Minute

	// The server picks the counter at random in [0, max): the client tries
	// counters from 0, so on average it does max/2 derivations, and a
	// script knowing the range can't skip any of them. Under load (many
	// signups in a short time) challenges get 5x harder.
	signupCounterMax     = 2000
	signupCounterBusyMax = 10000

	// signupBusyThreshold accounts created (with a solved challenge) within
	// signupBusyWindow switch new challenges to the busy range.
	signupBusyThreshold = 10
	signupBusyWindow    = 15 * time.Minute

	// SignupChallengeMinSecretLen is the minimum SIGNUP_CHALLENGE_SECRET length.
	SignupChallengeMinSecretLen = 32
)

// ChallengeClaimer records a solved challenge as used. It reports false when
// the key was already claimed (a replay). Backed by Redis SET NX in the
// gateway, so a restart or a second replica doesn't accept a payload twice.
type ChallengeClaimer func(ctx context.Context, key string, ttl time.Duration) (bool, error)

// SignupChallenge issues and verifies signup challenges.
type SignupChallenge struct {
	sigSecret string // signs the challenge parameters
	keySecret string // signs the expected derived key (fast verification)
	claim     ChallengeClaimer
	now       func() time.Time
	deriveKey altcha.DeriveKeyFunc

	mu     sync.Mutex
	recent []time.Time // accounts created within signupBusyWindow

	// Refusal log sampling: a flood of bad payloads gets a summary line
	// every refusalLogEvery instead of one line per request.
	logMu      sync.Mutex
	lastLog    time.Time
	suppressed map[string]int
}

const refusalLogEvery = 10 * time.Second

// LogRefusal logs a refused signup (reason, email domain), at most one line
// per refusalLogEvery; the line carries how many were left out since.
func (c *SignupChallenge) LogRefusal(reason, domain string) {
	c.logMu.Lock()
	now := c.now()
	if now.Sub(c.lastLog) < refusalLogEvery {
		if c.suppressed == nil {
			c.suppressed = map[string]int{}
		}
		c.suppressed[reason]++
		c.logMu.Unlock()
		return
	}
	skipped := c.suppressed
	c.suppressed = nil
	c.lastLog = now
	c.logMu.Unlock()
	slog.Info("signup refused: challenge", "reason", reason, "domain", domain, "not_logged_since_last", skipped)
}

// NewSignupChallenge builds the check from SIGNUP_CHALLENGE_SECRET. The two
// HMAC keys the protocol uses are derived from it, so one secret is enough.
func NewSignupChallenge(secret string) (*SignupChallenge, error) {
	if len(secret) < SignupChallengeMinSecretLen {
		return nil, fmt.Errorf("signup challenge secret must be at least %d bytes", SignupChallengeMinSecretLen)
	}
	derive := func(label string) string {
		m := hmac.New(sha256.New, []byte(secret))
		m.Write([]byte("eurobase/signup-challenge/" + label))
		return hex.EncodeToString(m.Sum(nil))
	}
	return &SignupChallenge{
		sigSecret: derive("signature"),
		keySecret: derive("key"),
		now:       time.Now,
		deriveKey: altcha.DeriveKeyPBKDF2(),
	}, nil
}

// SetClaimer sets the replay store. Without one (no Redis) a solved payload
// stays usable until it expires; the gateway logs that at startup.
func (c *SignupChallenge) SetClaimer(fn ChallengeClaimer) { c.claim = fn }

// busy reports whether recent signups reached the threshold.
func (c *SignupChallenge) busy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked()
	return len(c.recent) >= signupBusyThreshold
}

func (c *SignupChallenge) pruneLocked() {
	cutoff := c.now().Add(-signupBusyWindow)
	i := 0
	for i < len(c.recent) && c.recent[i].Before(cutoff) {
		i++
	}
	c.recent = c.recent[i:]
}

// RecordSignup counts an account created with a solved challenge, for the
// busy-mode difficulty. Only successful signups count: a solved payload
// refused afterwards (waitlist, rate limit, weak password) must not let a
// few solves push every real user onto harder challenges.
func (c *SignupChallenge) RecordSignup() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked()
	c.recent = append(c.recent, c.now())
}

// counterMax is the exclusive upper bound of the counter for a new
// challenge: the busy range while signups are frequent.
func (c *SignupChallenge) counterMax() int {
	if c.busy() {
		return signupCounterBusyMax
	}
	return signupCounterMax
}

// Create returns a new challenge for the widget.
func (c *SignupChallenge) Create() (altcha.Challenge, error) {
	counter := rand.IntN(c.counterMax())
	expires := c.now().Add(signupChallengeTTL)
	return altcha.CreateChallenge(altcha.CreateChallengeOptions{
		Algorithm:              signupChallengeAlgorithm,
		Cost:                   signupChallengeCost,
		KeyLength:              32,
		Counter:                &counter,
		DeriveKey:              c.deriveKey,
		ExpiresAt:              &expires,
		HMACSignatureSecret:    c.sigSecret,
		HMACKeySignatureSecret: c.keySecret,
	})
}

// Verify checks a widget payload (base64 JSON). It returns the reason for a
// refusal ("missing", "malformed", "expired", "invalid", "replayed") or ""
// when the payload is valid and now used up.
func (c *SignupChallenge) Verify(ctx context.Context, encoded string) string {
	if encoded == "" {
		return "missing"
	}
	if len(encoded) > 4096 {
		return "malformed"
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "malformed"
	}
	var p altcha.Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return "malformed"
	}
	// Only challenges of the kind we issue: the signature covers these
	// parameters, but refusing others early keeps a forged cost from
	// reaching the slow verification path.
	pp := p.Challenge.Parameters
	if pp.Algorithm != signupChallengeAlgorithm || pp.Cost != signupChallengeCost || pp.KeySignature == "" || pp.ExpiresAt == 0 {
		return "invalid"
	}
	res, err := altcha.VerifySolution(altcha.VerifySolutionOptions{
		Challenge:              p.Challenge,
		Solution:               p.Solution,
		DeriveKey:              c.deriveKey,
		HMACSignatureSecret:    c.sigSecret,
		HMACKeySignatureSecret: c.keySecret,
	})
	switch {
	case err != nil:
		return "invalid"
	case res.Expired:
		return "expired"
	case !res.Verified:
		return "invalid"
	}
	if c.claim != nil {
		// The signature is unique per challenge (random salt and nonce).
		ok, err := c.claim(ctx, "signup_challenge:"+p.Challenge.Signature, signupChallengeTTL+time.Minute)
		if err != nil {
			// Redis trouble: accept (availability), the payload is
			// still signed, solved and unexpired.
			slog.Warn("signup challenge replay check failed; accepting", "error", err)
		} else if !ok {
			return "replayed"
		}
	}
	return ""
}

// HandlePlatformSignupChallenge serves GET /platform/auth/signup/challenge.
// 404 when the check is off, so the console knows to sign up without it;
// `?check=1` only answers that (204 when on) without creating a challenge.
// Creating one costs a key derivation, so it is rate limited
// ("platform_signup_challenge", per client IP — product-wide until the
// load balancer passes real client addresses).
func HandlePlatformSignupChallenge(svc *PlatformAuthService, rateFn ...AuthRateLimiter) http.HandlerFunc {
	var check AuthRateLimiter
	if len(rateFn) > 0 {
		check = rateFn[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if svc.signupChallenge == nil {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("check") == "1" {
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if check != nil && check(w, r, "platform_signup_challenge", clientIP(r)) {
			return
		}
		ch, err := svc.signupChallenge.Create()
		if err != nil {
			slog.Error("create signup challenge", "error", err)
			writeJSONError(w, "could not create a challenge", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(ch)
	}
}

// EnableSignupChallenge makes POST /platform/auth/signup require a solved
// challenge (SIGNUP_CHALLENGE=required).
func (s *PlatformAuthService) EnableSignupChallenge(c *SignupChallenge) {
	s.signupChallenge = c
}

// SignupChallengeEnabled reports whether signup requires a challenge.
func (s *PlatformAuthService) SignupChallengeEnabled() bool { return s.signupChallenge != nil }

// signupEmailDomain is the part after the last "@" (lowercased), for logs
// that shouldn't carry the whole address.
func signupEmailDomain(email string) string {
	at := strings.LastIndexByte(email, '@')
	if at < 0 || at == len(email)-1 {
		return ""
	}
	d := strings.ToLower(email[at+1:])
	if len(d) > 253 {
		d = d[:253]
	}
	return d
}
