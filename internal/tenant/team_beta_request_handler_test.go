package tenant

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
)

// Test coverage for HandleTeamBetaRequest — pins the source column
// value (so admin panel filters work) and the auth-from-claims
// contract (email cannot be spoofed by a client that overrode its
// profile in the console).
//
// setupTestDB skips without DATABASE_URL, so CI's test-go without a
// Postgres attached still passes the compile-and-vet check.

// TestHandleTeamBetaRequest_HappyPath asserts that a valid message
// from an authenticated user inserts a contact_requests row with
// source='team_beta_request' and the email from the JWT claims.
func TestHandleTeamBetaRequest_HappyPath(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	// Seed the caller as a platform user so the display-name lookup
	// finds a row.
	callerEmail := "team-beta-req@test.eurobase.local"
	callerID := insertTestPlatformUser(t, pool, callerEmail)
	if _, err := pool.Exec(ctx,
		`UPDATE public.platform_users SET display_name = 'Beta Tester' WHERE id = $1::uuid`,
		callerID,
	); err != nil {
		t.Fatalf("set display_name: %v", err)
	}
	t.Cleanup(func() {
		// Sweep the fixture row plus anything the handler inserted
		// (the test asserts insertion — this is belt-and-braces on
		// the shared harness).
		_, _ = pool.Exec(ctx,
			`DELETE FROM public.contact_requests WHERE email = $1`,
			callerEmail,
		)
	})

	// The handler builds its own claims-driven payload; the request
	// body only carries the message.
	body := bytes.NewBufferString(`{"message":"Building a legal-tech SaaS; need dedicated Postgres + SSO for our pilot firm."}`)
	req := httptest.NewRequest(http.MethodPost, "/platform/team-beta-request/", body)
	rctx := auth.ContextWithClaims(req.Context(), &auth.Claims{
		Subject: callerID,
		Email:   callerEmail,
	})
	req = req.WithContext(rctx)
	w := httptest.NewRecorder()
	// nil limiter — the handler's fallback path logs a warn and
	// allows the request through, which is what we want in the test
	// harness (no Redis).
	HandleTeamBetaRequest(pool, nil).ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201 (body: %s)", w.Code, w.Body.String())
	}
	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ID == "" || out.Status != "received" {
		t.Errorf("response mismatch: %+v", out)
	}

	// Row lands with source='team_beta_request' — this is the
	// column the admin panel will filter on.
	var storedSource, storedEmail string
	var storedName *string
	if err := pool.QueryRow(ctx,
		`SELECT source, email, name FROM public.contact_requests WHERE id = $1::uuid`,
		out.ID,
	).Scan(&storedSource, &storedEmail, &storedName); err != nil {
		t.Fatalf("read back row: %v", err)
	}
	if storedSource != "team_beta_request" {
		t.Errorf("source: got %q, want %q", storedSource, "team_beta_request")
	}
	if storedEmail != callerEmail {
		t.Errorf("email: got %q, want %q", storedEmail, callerEmail)
	}
	if storedName == nil || *storedName != "Beta Tester" {
		t.Errorf("name: got %v, want %q", storedName, "Beta Tester")
	}
}

// TestHandleTeamBetaRequest_RejectsShortMessage pins the input
// validation — a one-word "please" doesn't help ops decide.
func TestHandleTeamBetaRequest_RejectsShortMessage(t *testing.T) {
	pool := setupTestDB(t)

	callerID := insertTestPlatformUser(t, pool, "team-beta-short@test.eurobase.local")

	body := bytes.NewBufferString(`{"message":"please"}`)
	req := httptest.NewRequest(http.MethodPost, "/platform/team-beta-request/", body)
	rctx := auth.ContextWithClaims(req.Context(), &auth.Claims{
		Subject: callerID,
		Email:   "team-beta-short@test.eurobase.local",
	})
	req = req.WithContext(rctx)
	w := httptest.NewRecorder()
	HandleTeamBetaRequest(pool, nil).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
	if !contains(w.Body.String(), "at least 10 characters") {
		t.Errorf("expected 'at least 10 characters' in message, got: %s", w.Body.String())
	}
}

// TestHandleTeamBetaRequest_RejectsUnauthenticated pins that the
// handler requires JWT claims. Without a session, no insert.
func TestHandleTeamBetaRequest_RejectsUnauthenticated(t *testing.T) {
	pool := setupTestDB(t)

	body := bytes.NewBufferString(`{"message":"legitimate use case here"}`)
	req := httptest.NewRequest(http.MethodPost, "/platform/team-beta-request/", body)
	w := httptest.NewRecorder()
	HandleTeamBetaRequest(pool, nil).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401 (body: %s)", w.Code, w.Body.String())
	}
}
