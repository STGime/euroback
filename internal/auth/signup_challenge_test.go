package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	altcha "github.com/altcha-org/altcha-lib-go/v2"
)

const testChallengeSecret = "0123456789abcdef0123456789abcdef"

func newTestChallenge(t *testing.T) *SignupChallenge {
	t.Helper()
	c, err := NewSignupChallenge(testChallengeSecret)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// memClaimer is an in-memory ChallengeClaimer.
func memClaimer() ChallengeClaimer {
	var mu sync.Mutex
	seen := map[string]bool{}
	return func(_ context.Context, key string, _ time.Duration) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		if seen[key] {
			return false, nil
		}
		seen[key] = true
		return true, nil
	}
}

// solve solves a challenge the way the widget does and encodes the payload.
func solve(t *testing.T, ch altcha.Challenge) string {
	t.Helper()
	sol, err := altcha.SolveChallenge(altcha.SolveChallengeOptions{Challenge: ch, DeriveKey: altcha.DeriveKeyPBKDF2()})
	if err != nil || sol == nil {
		t.Fatalf("solve: %v", err)
	}
	return encodePayload(t, altcha.Payload{Challenge: ch, Solution: *sol})
}

func encodePayload(t *testing.T, p altcha.Payload) string {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestNewSignupChallenge_ShortSecret(t *testing.T) {
	if _, err := NewSignupChallenge("short"); err == nil {
		t.Fatal("short secret accepted")
	}
}

func TestSignupChallenge_RoundTrip(t *testing.T) {
	c := newTestChallenge(t)
	c.SetClaimer(memClaimer())
	ch, err := c.Create()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	payload := solve(t, ch)
	t.Logf("solved in %v (single thread, Go)", time.Since(start))

	if r := c.Verify(context.Background(), payload); r != "" {
		t.Fatalf("valid payload refused: %s", r)
	}
	if r := c.Verify(context.Background(), payload); r != "replayed" {
		t.Fatalf("second use: %q, want replayed", r)
	}
}

func TestSignupChallenge_Refusals(t *testing.T) {
	c := newTestChallenge(t)
	ch, err := c.Create()
	if err != nil {
		t.Fatal(err)
	}
	sol, err := altcha.SolveChallenge(altcha.SolveChallengeOptions{Challenge: ch, DeriveKey: altcha.DeriveKeyPBKDF2()})
	if err != nil || sol == nil {
		t.Fatal(err)
	}

	wrongKey := *sol
	wrongKey.DerivedKey = strings.Repeat("ab", 32)
	tampered := ch
	tampered.Parameters.Cost = 1 // cheaper challenge, same signature
	unsigned := ch
	unsigned.Signature = ""

	other, _ := NewSignupChallenge("ffffffffffffffffffffffffffffffff")
	foreign, _ := other.Create()
	foreignSol, _ := altcha.SolveChallenge(altcha.SolveChallengeOptions{Challenge: foreign, DeriveKey: altcha.DeriveKeyPBKDF2()})

	cases := map[string]struct {
		payload string
		want    string
	}{
		"missing":        {"", "missing"},
		"not base64":     {"%%%", "malformed"},
		"not json":       {base64.StdEncoding.EncodeToString([]byte("nope")), "malformed"},
		"too long":       {strings.Repeat("A", 5000), "malformed"},
		"wrong solution": {encodePayload(t, altcha.Payload{Challenge: ch, Solution: wrongKey}), "invalid"},
		"tampered cost":  {encodePayload(t, altcha.Payload{Challenge: tampered, Solution: *sol}), "invalid"},
		"unsigned":       {encodePayload(t, altcha.Payload{Challenge: unsigned, Solution: *sol}), "invalid"},
		"other secret":   {encodePayload(t, altcha.Payload{Challenge: foreign, Solution: *foreignSol}), "invalid"},
	}
	for name, tc := range cases {
		if got := c.Verify(context.Background(), tc.payload); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestSignupChallenge_Expired(t *testing.T) {
	c := newTestChallenge(t)
	c.now = func() time.Time { return time.Now().Add(-signupChallengeTTL - time.Minute) }
	ch, err := c.Create()
	if err != nil {
		t.Fatal(err)
	}
	c.now = time.Now
	if got := c.Verify(context.Background(), solve(t, ch)); got != "expired" {
		t.Fatalf("got %q, want expired", got)
	}
}

// A replay store error doesn't block signup (availability); a valid
// payload is accepted.
func TestSignupChallenge_ClaimerError(t *testing.T) {
	c := newTestChallenge(t)
	c.SetClaimer(func(context.Context, string, time.Duration) (bool, error) { return false, errors.New("redis down") })
	ch, _ := c.Create()
	if got := c.Verify(context.Background(), solve(t, ch)); got != "" {
		t.Fatalf("got %q, want accepted", got)
	}
}

func TestSignupChallenge_BusyRaisesDifficulty(t *testing.T) {
	c := newTestChallenge(t)
	if c.busy() || c.counterMax() != signupCounterMax {
		t.Fatal("busy at start")
	}
	for i := 0; i < signupBusyThreshold; i++ {
		c.RecordSignup()
	}
	if !c.busy() {
		t.Fatal("not busy after the threshold")
	}
	if got := c.counterMax(); got != signupCounterBusyMax {
		t.Fatalf("busy counter max %d, want %d", got, signupCounterBusyMax)
	}
	// The window passes: back to normal.
	c.now = func() time.Time { return time.Now().Add(signupBusyWindow + time.Minute) }
	if c.busy() {
		t.Fatal("still busy after the window")
	}
}

// With the check on, a signup without a valid solution is refused before
// the rate limits run, so it can't use up the signup budget.
func TestHandlePlatformSignUp_ChallengeBeforeRateLimit(t *testing.T) {
	svc := &PlatformAuthService{}
	svc.EnableSignupChallenge(newTestChallenge(t))
	var limited []string
	check := func(w http.ResponseWriter, r *http.Request, action, id string) bool {
		limited = append(limited, action)
		return false
	}
	body := `{"email":"a@example.org","password":"x","accepted_documents":[]}`
	rec := httptest.NewRecorder()
	HandlePlatformSignUp(svc, check)(rec, httptest.NewRequest(http.MethodPost, "/platform/auth/signup", strings.NewReader(body)))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"code":"challenge_failed"`) {
		t.Fatalf("got %d %s, want 403 challenge_failed", rec.Code, rec.Body.String())
	}
	if len(limited) != 0 {
		t.Fatalf("rate limits ran before the challenge: %v", limited)
	}
}

func TestHandlePlatformSignupChallenge(t *testing.T) {
	svc := &PlatformAuthService{}
	rec := httptest.NewRecorder()
	HandlePlatformSignupChallenge(svc)(rec, httptest.NewRequest(http.MethodGet, "/platform/auth/signup/challenge", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("check off: %d, want 404", rec.Code)
	}

	svc.EnableSignupChallenge(newTestChallenge(t))
	rec = httptest.NewRecorder()
	HandlePlatformSignupChallenge(svc)(rec, httptest.NewRequest(http.MethodGet, "/platform/auth/signup/challenge", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("got %d (cache %q)", rec.Code, rec.Header().Get("Cache-Control"))
	}
	var ch altcha.Challenge
	if err := json.Unmarshal(rec.Body.Bytes(), &ch); err != nil || ch.Signature == "" || ch.Parameters.KeySignature == "" {
		t.Fatalf("challenge: %v %+v", err, ch)
	}
	if ch.Parameters.Algorithm != signupChallengeAlgorithm || ch.Parameters.ExpiresAt == 0 {
		t.Fatalf("parameters: %+v", ch.Parameters)
	}
}

func TestSignupEmailDomain(t *testing.T) {
	for in, want := range map[string]string{"a@Example.ORG": "example.org", "nope": "", "a@": "", "x@y@z.de": "z.de"} {
		if got := signupEmailDomain(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// The console's widget (npm altcha) solves challenges the Go library issues,
// and its payload verifies here. Runs only where the console's node_modules
// are installed; skipped otherwise.
func TestSignupChallenge_WidgetInterop(t *testing.T) {
	lib, err := filepath.Abs("../../console/node_modules/altcha/dist/lib/index.js")
	if err != nil {
		t.Skip(err)
	}
	// CI's console job sets SIGNUP_INTEROP_REQUIRED=1, so the check can't
	// silently skip there.
	skip := t.Skipf
	if os.Getenv("SIGNUP_INTEROP_REQUIRED") == "1" {
		skip = t.Fatalf
	}
	if _, err := os.Stat(lib); err != nil {
		skip("console node_modules not installed: %v", err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		skip("node not installed: %v", err)
	}
	c := newTestChallenge(t)
	c.SetClaimer(memClaimer())
	ch, err := c.Create()
	if err != nil {
		t.Fatal(err)
	}
	chJSON, _ := json.Marshal(ch)
	script := `
const { solveChallenge, pbkdf2 } = await import(process.argv[1]);
const challenge = JSON.parse(process.argv[2]);
const solution = await solveChallenge({ challenge, deriveKey: pbkdf2.deriveKey });
process.stdout.write(Buffer.from(JSON.stringify({ challenge, solution })).toString('base64'));
`
	out, err := exec.Command(node, "--input-type=module", "-e", script, lib, string(chJSON)).Output()
	if err != nil {
		t.Fatalf("node solve: %v", err)
	}
	if got := c.Verify(context.Background(), strings.TrimSpace(string(out))); got != "" {
		t.Fatalf("widget payload refused: %q", got)
	}
}

// The same payload re-encoded (whitespace, field order) is still a replay:
// the key is the challenge's signature, not the payload bytes.
func TestSignupChallenge_ReplayReencoded(t *testing.T) {
	c := newTestChallenge(t)
	c.SetClaimer(memClaimer())
	ch, _ := c.Create()
	payload := solve(t, ch)
	if r := c.Verify(context.Background(), payload); r != "" {
		t.Fatalf("first use refused: %s", r)
	}
	raw, _ := base64.StdEncoding.DecodeString(payload)
	var generic map[string]any
	_ = json.Unmarshal(raw, &generic)
	pretty, _ := json.MarshalIndent(map[string]any{"solution": generic["solution"], "challenge": generic["challenge"]}, "", "  ")
	if r := c.Verify(context.Background(), base64.StdEncoding.EncodeToString(pretty)); r != "replayed" {
		t.Fatalf("re-encoded payload: %q, want replayed", r)
	}
}

// A valid payload passes the challenge, then the rate limits still apply.
func TestHandlePlatformSignUp_ValidPayloadThenRateLimit(t *testing.T) {
	svc := &PlatformAuthService{}
	c := newTestChallenge(t)
	svc.EnableSignupChallenge(c)
	ch, _ := c.Create()
	var limited []string
	check := func(w http.ResponseWriter, r *http.Request, action, id string) bool {
		limited = append(limited, action)
		w.WriteHeader(http.StatusTooManyRequests)
		return true
	}
	body, _ := json.Marshal(map[string]any{"email": "a@example.org", "password": "x", "altcha": solve(t, ch)})
	rec := httptest.NewRecorder()
	HandlePlatformSignUp(svc, check)(rec, httptest.NewRequest(http.MethodPost, "/platform/auth/signup", strings.NewReader(string(body))))
	if rec.Code != http.StatusTooManyRequests || len(limited) != 1 || limited[0] != "platform_signup" {
		t.Fatalf("got %d, limits %v; want 429 from platform_signup", rec.Code, limited)
	}
	if c.busy() {
		t.Fatal("a refused signup counted towards busy mode")
	}
}

func TestHandlePlatformSignupChallenge_ProbeAndRateLimit(t *testing.T) {
	svc := &PlatformAuthService{}
	svc.EnableSignupChallenge(newTestChallenge(t))
	var limited []string
	check := func(w http.ResponseWriter, r *http.Request, action, id string) bool {
		limited = append(limited, action)
		w.WriteHeader(http.StatusTooManyRequests)
		return true
	}
	h := HandlePlatformSignupChallenge(svc, check)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/platform/auth/signup/challenge?check=1", nil))
	if rec.Code != http.StatusNoContent || len(limited) != 0 {
		t.Fatalf("probe: %d, limits %v; want 204 without a limit", rec.Code, limited)
	}
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/platform/auth/signup/challenge", nil))
	if rec.Code != http.StatusTooManyRequests || len(limited) != 1 || limited[0] != "platform_signup_challenge" {
		t.Fatalf("create: %d, limits %v; want 429 from platform_signup_challenge", rec.Code, limited)
	}
}

func TestSignupChallenge_RefusalLogSampled(t *testing.T) {
	c := newTestChallenge(t)
	t0 := time.Now()
	c.now = func() time.Time { return t0 }
	c.LogRefusal("missing", "a.example")
	c.LogRefusal("missing", "a.example")
	c.LogRefusal("invalid", "a.example")
	if c.suppressed["missing"] != 1 || c.suppressed["invalid"] != 1 {
		t.Fatalf("suppressed %v, want missing:1 invalid:1", c.suppressed)
	}
	c.now = func() time.Time { return t0.Add(refusalLogEvery) }
	c.LogRefusal("missing", "a.example")
	if c.suppressed != nil {
		t.Fatalf("summary line not written: %v", c.suppressed)
	}
}
