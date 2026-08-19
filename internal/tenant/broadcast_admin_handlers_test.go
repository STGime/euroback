package tenant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/email"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Regression coverage for the broadcast audience feature — the
// endpoint that returns the deduped union of platform_allowlist ∪
// platform_users so the Compose Broadcast modal can preview
// recipient counts + provenance without client-side merging.
//
// The critical property the endpoint MUST guarantee is dedup by
// lower(trim(email)) — a user typing `Foo@X.com` in the allowlist
// and `foo@x.com` in the signup form must collapse to ONE recipient,
// or the send handler will double-mail them.

// setupBroadcastFixture seeds a minimal audience mix into the shared
// test DB. Returns a cleanup that removes only the fixture rows so
// concurrent tests aren't disturbed. Fixture emails all end with
// @test.eurobase.local so a leftover from a crashed test doesn't
// leak into subsequent runs.
func setupBroadcastFixture(t *testing.T, pool *pgxpool.Pool) func() {
	t.Helper()
	ctx := context.Background()

	// Clean any stragglers.
	_, _ = pool.Exec(ctx, `DELETE FROM platform_allowlist WHERE email LIKE '%@test.eurobase.local'`)
	_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE email LIKE '%@test.eurobase.local'`)

	// Seed three provenance shapes + a case/whitespace edge case.
	_, err := pool.Exec(ctx, `
		INSERT INTO platform_allowlist (email, note) VALUES
		    ('allow-only@test.eurobase.local', 'invited, never signed up'),
		    ('both@test.eurobase.local',       'invited AND signed up'),
		    -- Case + whitespace variant of a signed-up email;
		    -- MUST collapse with the users row below in the union.
		    ('  BOTH-Case@test.eurobase.local  ', 'case + whitespace variant')
		ON CONFLICT (email) DO NOTHING
	`)
	if err != nil {
		t.Fatalf("seed allowlist: %v", err)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO platform_users (email, password_hash, email_confirmed_at) VALUES
		    ('signup-only@test.eurobase.local', 'x', now()),
		    ('both@test.eurobase.local',        'x', now()),
		    ('both-case@test.eurobase.local',   'x', now())
	`)
	if err != nil {
		t.Fatalf("seed users: %v", err)
	}

	return func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform_allowlist WHERE email LIKE '%@test.eurobase.local'`)
		_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE email LIKE '%@test.eurobase.local'`)
	}
}

// TestAdminListBroadcastAudience_DedupesByLowerTrim pins the
// load-bearing invariant: an email present in BOTH lists (or in one
// list with case/whitespace variants of the other's spelling) must
// appear as ONE recipient in the audience. Otherwise the Compose
// modal preview lies and the send handler would double-BCC.
func TestAdminListBroadcastAudience_DedupesByLowerTrim(t *testing.T) {
	pool := setupTestDB(t)
	cleanup := setupBroadcastFixture(t, pool)
	defer cleanup()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/platform/admin/broadcast/audience", nil)
	AdminListBroadcastAudience(pool).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	var body struct {
		Recipients []BroadcastRecipient `json:"recipients"`
		Total      int                  `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Filter to fixture rows only — the shared DB may have other rows.
	fixture := map[string]BroadcastRecipient{}
	for _, r := range body.Recipients {
		if strings.HasSuffix(r.Email, "@test.eurobase.local") {
			fixture[r.Email] = r
		}
	}

	// Expected 4 unique keys:
	//   allow-only        (allowlist only)
	//   signup-only       (users only)
	//   both              (both)
	//   both-case         (both — via case-variant allowlist entry)
	if got := len(fixture); got != 4 {
		t.Fatalf("expected 4 fixture recipients (dedup by lower(trim)), got %d: %+v", got, fixture)
	}

	assertRec := func(email string, wantOnAllowlist, wantHasAccount bool) {
		t.Helper()
		r, ok := fixture[email]
		if !ok {
			t.Fatalf("missing recipient %q; got %+v", email, fixture)
		}
		if r.OnAllowlist != wantOnAllowlist {
			t.Errorf("%s: on_allowlist=%v, want %v", email, r.OnAllowlist, wantOnAllowlist)
		}
		if r.HasAccount != wantHasAccount {
			t.Errorf("%s: has_account=%v, want %v", email, r.HasAccount, wantHasAccount)
		}
	}

	assertRec("allow-only@test.eurobase.local", true, false)
	assertRec("signup-only@test.eurobase.local", false, true)
	assertRec("both@test.eurobase.local", true, true)
	// Case+whitespace variant: allowlist had "  BOTH-Case@… ", users
	// had "both-case@…". Union under lower(trim) → one row keyed by
	// the normalized form, flagged in BOTH sources.
	assertRec("both-case@test.eurobase.local", true, true)
}

// stubBulkEmailer records recipients so tests can assert what the
// widened validation actually forwarded to SendBulkBCC. It always
// succeeds — the tests here care about which addresses the handler
// selected, not TEM behaviour.
type stubBulkEmailer struct {
	seen []string
}

func (s *stubBulkEmailer) SendBulkBCC(ctx context.Context, recipients []string, subject, htmlBody string) (email.BulkResult, error) {
	s.seen = append([]string(nil), recipients...)
	return email.BulkResult{Sent: len(recipients)}, nil
}

// TestAdminSendAllowlistEmail_AcceptsWidenedAudience is the mirror
// on the send path: after the audience-endpoint dedups, the send
// handler MUST accept recipients from either source. Pre-widening
// the handler validated ONLY against platform_allowlist so a
// signed-up user who wasn't invited would be silently dropped.
func TestAdminSendAllowlistEmail_AcceptsWidenedAudience(t *testing.T) {
	pool := setupTestDB(t)
	cleanup := setupBroadcastFixture(t, pool)
	defer cleanup()

	mailer := &stubBulkEmailer{}

	body := `{
		"emails": [
			"allow-only@test.eurobase.local",
			"signup-only@test.eurobase.local",
			"both@test.eurobase.local",
			"not-registered@test.eurobase.local"
		],
		"subject": "test",
		"body_html": "<p>hi</p>"
	}`
	req := httptest.NewRequest(http.MethodPost, "/platform/admin/allowlist/email", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	AdminSendAllowlistEmail(pool, mailer).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	// Should have forwarded three addresses (all three known ones);
	// the unregistered one must be silently filtered.
	got := map[string]bool{}
	for _, e := range mailer.seen {
		got[e] = true
	}
	for _, want := range []string{
		"allow-only@test.eurobase.local",
		"signup-only@test.eurobase.local", // ← would fail pre-widening
		"both@test.eurobase.local",
	} {
		if !got[want] {
			t.Errorf("expected %q to be forwarded to mailer; got %v", want, mailer.seen)
		}
	}
	if got["not-registered@test.eurobase.local"] {
		t.Errorf("unregistered email leaked through validation: %v", mailer.seen)
	}
}

// TestAdminListBroadcastAudience_DedupesWithinAllowlist is the
// gap the #433 review caught: platform_allowlist.email is a
// case-sensitive TEXT PRIMARY KEY, so a superadmin can insert
// `Foo@X.com` AND `foo@x.com` as two rows. Pre-fix the FULL OUTER
// JOIN would produce two output rows for the same normalized
// address → the send handler would double-BCC the address → the
// "each exactly once" preview would lie.
//
// The fix is `SELECT DISTINCT lower(trim(email))` inside the
// allowlist subquery (and GROUP BY on the users side for
// symmetry). This test would fail on the pre-fix SQL.
func TestAdminListBroadcastAudience_DedupesWithinAllowlist(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	// Clean + seed two case-variant allowlist rows for the same
	// visual address. Bypass the shared fixture so we know exactly
	// what's in the audience for the assertion.
	_, _ = pool.Exec(ctx, `DELETE FROM platform_allowlist WHERE email LIKE '%@test.eurobase.local'`)
	_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE email LIKE '%@test.eurobase.local'`)
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform_allowlist WHERE email LIKE '%@test.eurobase.local'`)
		_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE email LIKE '%@test.eurobase.local'`)
	}()

	_, err := pool.Exec(ctx, `
		INSERT INTO platform_allowlist (email, note) VALUES
		    ('Dup@test.eurobase.local',   'first entry, mixed case'),
		    ('dup@test.eurobase.local',   'second entry, lower case'),
		    ('  dup@test.eurobase.local  ', 'third entry, padded (edge)')
		ON CONFLICT (email) DO NOTHING
	`)
	if err != nil {
		t.Fatalf("seed allowlist: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/platform/admin/broadcast/audience", nil)
	AdminListBroadcastAudience(pool).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	var body struct {
		Recipients []BroadcastRecipient `json:"recipients"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Count fixture rows in the audience — MUST be exactly 1
	// (three case-variant allowlist entries collapse to one row).
	count := 0
	for _, r := range body.Recipients {
		if strings.HasSuffix(r.Email, "@test.eurobase.local") {
			count++
			if r.Email != "dup@test.eurobase.local" {
				t.Errorf("audience email not normalized: got %q, want %q", r.Email, "dup@test.eurobase.local")
			}
			if !r.OnAllowlist {
				t.Errorf("audience row missing on_allowlist=true: %+v", r)
			}
		}
	}
	if count != 1 {
		t.Fatalf("dedup broken: expected 1 audience row for the three case-variant allowlist entries, got %d", count)
	}
}

// TestAdminSendAllowlistEmail_DedupsCaseVariantRequest confirms the
// send-side belt: a caller passing `Foo@X.com` twice (or with a
// case-variant of a known DB email) is deduped BEFORE SendBulkBCC.
// Otherwise the same recipient could receive multiple copies of
// the same broadcast — the exact "each exactly once" ask.
func TestAdminSendAllowlistEmail_DedupsCaseVariantRequest(t *testing.T) {
	pool := setupTestDB(t)
	cleanup := setupBroadcastFixture(t, pool)
	defer cleanup()

	mailer := &stubBulkEmailer{}

	// Caller sends the same signed-up user THREE ways: lowercased,
	// mixed-case, and with trailing whitespace. Must collapse to
	// ONE forwarded recipient.
	body := `{
		"emails": [
			"signup-only@test.eurobase.local",
			"Signup-Only@test.eurobase.local",
			"  signup-only@test.eurobase.local  "
		],
		"subject": "test",
		"body_html": "<p>hi</p>"
	}`
	req := httptest.NewRequest(http.MethodPost, "/platform/admin/allowlist/email", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	AdminSendAllowlistEmail(pool, mailer).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	// Count how many times signup-only appears in the forwarded set.
	// Any form of the normalized `signup-only@…` counts.
	count := 0
	for _, e := range mailer.seen {
		if strings.EqualFold(strings.TrimSpace(e), "signup-only@test.eurobase.local") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("caller passed 3 variants of the same address; expected 1 forwarded, got %d (seen: %v)", count, mailer.seen)
	}
}

// TestAdminListBroadcastAudience_ExcludesReservedTLDs pins the
// 2026-08-18 fix: RFC 2606 reserved TLDs (.invalid, .test,
// .example) and .localhost are excluded from the audience so
// automated probe accounts (created by internal/auth/signup_notify)
// don't contaminate broadcast sends. Motivating incident: a
// single `@example.invalid` address in a chunk of 10 killed 9
// real customer sends because Scaleway TEM validates format
// server-side and rejects the whole batch on any RFC-invalid
// recipient.
//
// The split-retry in internal/email/client.go is the belt for any
// address that slips past this filter; this test locks in the
// primary defense.
func TestAdminListBroadcastAudience_ExcludesReservedTLDs(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	_, _ = pool.Exec(ctx, `DELETE FROM platform_allowlist WHERE email LIKE '%@test.eurobase.local' OR email LIKE '%.invalid' OR email LIKE '%.test' OR email LIKE '%.example' OR email LIKE '%@localhost'`)
	_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE email LIKE '%@test.eurobase.local' OR email LIKE '%.invalid' OR email LIKE '%.test' OR email LIKE '%.example' OR email LIKE '%@localhost'`)
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform_allowlist WHERE email LIKE '%@test.eurobase.local' OR email LIKE '%.invalid' OR email LIKE '%.test' OR email LIKE '%.example' OR email LIKE '%@localhost'`)
		_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE email LIKE '%@test.eurobase.local' OR email LIKE '%.invalid' OR email LIKE '%.test' OR email LIKE '%.example' OR email LIKE '%@localhost'`)
	}()

	// Seed one real address (must appear) + four reserved-TLD
	// variants (must NOT appear) on each side.
	_, err := pool.Exec(ctx, `
		INSERT INTO platform_allowlist (email, note) VALUES
		    ('real-allow@test.eurobase.local', 'must appear'),
		    ('probe-a@example.invalid', 'RFC 2606'),
		    ('probe-b@somewhere.test',  'RFC 2606'),
		    ('probe-c@thing.example',   'RFC 2606'),
		    ('probe-d@localhost',       'RFC 6762 (bare)'),
		    ('probe-e@app.localhost',   'RFC 6762 (subdomain — #434 review)')
		ON CONFLICT (email) DO NOTHING
	`)
	if err != nil {
		t.Fatalf("seed allowlist: %v", err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO platform_users (email, password_hash, email_confirmed_at) VALUES
		    ('real-user@test.eurobase.local', 'x', now()),
		    ('signup-notify-test-999@example.invalid', 'x', now()),
		    ('signup-probe-999@localhost',            'x', now())
	`)
	if err != nil {
		t.Fatalf("seed users: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/platform/admin/broadcast/audience", nil)
	AdminListBroadcastAudience(pool).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	var body struct {
		Recipients []BroadcastRecipient `json:"recipients"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Bucket the fixture rows.
	seen := map[string]bool{}
	for _, r := range body.Recipients {
		seen[r.Email] = true
	}

	// Real addresses MUST appear.
	for _, want := range []string{
		"real-allow@test.eurobase.local",
		"real-user@test.eurobase.local",
	} {
		if !seen[want] {
			t.Errorf("real address %q missing from audience", want)
		}
	}
	// Reserved-TLD addresses MUST NOT appear.
	for _, banned := range []string{
		"probe-a@example.invalid",
		"probe-b@somewhere.test",
		"probe-c@thing.example",
		"probe-d@localhost",
		"probe-e@app.localhost", // subdomain form — added post-review
		"signup-notify-test-999@example.invalid",
		"signup-probe-999@localhost",
	} {
		if seen[banned] {
			t.Errorf("reserved-TLD address %q leaked into audience (RFC 2606 filter broken)", banned)
		}
	}
}

// TestAdminSendAllowlistEmail_RejectsReservedTLDInSend is the
// send-side mirror. Even if the caller manages to include a
// reserved-TLD address in the request payload (bypassing the
// audience endpoint), the send handler's own validation must
// filter it. Defence in depth for the "Scaleway TEM rejects the
// whole chunk on any RFC-invalid address" hazard.
func TestAdminSendAllowlistEmail_RejectsReservedTLDInSend(t *testing.T) {
	pool := setupTestDB(t)
	cleanup := setupBroadcastFixture(t, pool)
	defer cleanup()

	// Seed a valid signed-up user + inject an .invalid entry into
	// platform_allowlist. The valid one must be forwarded; the
	// .invalid one must be filtered by the send-side validation.
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `INSERT INTO platform_allowlist (email, note) VALUES ('probe@example.invalid', 'should be filtered') ON CONFLICT (email) DO NOTHING`)
	defer pool.Exec(ctx, `DELETE FROM platform_allowlist WHERE email = 'probe@example.invalid'`)

	mailer := &stubBulkEmailer{}
	body := `{
		"emails": [
			"signup-only@test.eurobase.local",
			"probe@example.invalid"
		],
		"subject": "test",
		"body_html": "<p>hi</p>"
	}`
	req := httptest.NewRequest(http.MethodPost, "/platform/admin/allowlist/email", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	AdminSendAllowlistEmail(pool, mailer).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	seen := map[string]bool{}
	for _, e := range mailer.seen {
		seen[e] = true
	}
	if !seen["signup-only@test.eurobase.local"] {
		t.Errorf("real address missing from forwarded set: %v", mailer.seen)
	}
	if seen["probe@example.invalid"] {
		t.Errorf(".invalid address leaked past send validation: %v", mailer.seen)
	}
}

// TestAdminSendAllowlistEmail_RejectsIfNoValidRecipients confirms
// the endpoint isn't a generic mail relay — every address filtered
// out → 400, no send attempt. Same fence as pre-widening, just with
// a broader whitelist.
func TestAdminSendAllowlistEmail_RejectsIfNoValidRecipients(t *testing.T) {
	pool := setupTestDB(t)
	cleanup := setupBroadcastFixture(t, pool)
	defer cleanup()

	mailer := &stubBulkEmailer{}

	body := `{
		"emails": ["unknown-a@test.eurobase.local", "unknown-b@test.eurobase.local"],
		"subject": "test",
		"body_html": "<p>hi</p>"
	}`
	req := httptest.NewRequest(http.MethodPost, "/platform/admin/allowlist/email", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	AdminSendAllowlistEmail(pool, mailer).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if len(mailer.seen) != 0 {
		t.Errorf("mailer was called for %d recipients; expected 0 (no valid targets)", len(mailer.seen))
	}
}
