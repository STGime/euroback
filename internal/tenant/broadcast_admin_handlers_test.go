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
