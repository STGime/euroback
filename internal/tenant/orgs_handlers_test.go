package tenant

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/go-chi/chi/v5"
)

// Test coverage for HandleInviteMember's fire-and-forget invitation
// email hook. The send happens in a goroutine after the DB commit, so
// the fake mailer uses a channel + WaitGroup to synchronise cleanly
// (no time.Sleep).

// fakeOrgMailer records one Send call and satisfies PlatformOrgMailer.
type fakeOrgMailer struct {
	mu           sync.Mutex
	calls        int
	invitedEmail string
	orgName      string
	inviterEmail string
	done         chan struct{}
	returnErr    error
}

func newFakeOrgMailer() *fakeOrgMailer {
	return &fakeOrgMailer{done: make(chan struct{}, 1)}
}

func (f *fakeOrgMailer) SendPlatformOrgInvitationEmail(ctx context.Context, invitedEmail, orgName, inviterEmail string) error {
	f.mu.Lock()
	f.calls++
	f.invitedEmail = invitedEmail
	f.orgName = orgName
	f.inviterEmail = inviterEmail
	f.mu.Unlock()
	select {
	case f.done <- struct{}{}:
	default:
	}
	return f.returnErr
}

// waitCalled blocks until the mailer has been invoked at least once
// or the deadline expires. Keeps tests deterministic without sleeping.
func (f *fakeOrgMailer) waitCalled(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case <-f.done:
	case <-time.After(d):
		t.Fatal("mailer was not called within deadline")
	}
}

// TestHandleInviteMember_FiresInvitationEmail is the happy path: an
// admin invites a signed-up user; the DB row is committed AND the
// mailer sees one Send with the right fields.
func TestHandleInviteMember_FiresInvitationEmail(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	// Seed admin + invitee + org.
	adminEmail := "org-invite-admin@test.eurobase.local"
	inviteeEmail := "org-invite-target@test.eurobase.local"
	adminID := insertTestPlatformUser(t, pool, adminEmail)
	_ = insertTestPlatformUser(t, pool, inviteeEmail)
	// Grant Team beta so requireTeamBeta doesn't 403.
	if _, err := pool.Exec(ctx,
		`UPDATE public.platform_users SET team_beta_access = true WHERE id = $1::uuid`,
		adminID,
	); err != nil {
		t.Fatalf("grant team beta: %v", err)
	}

	svc, err := NewOrgsService(pool, nil) // no SSO write path exercised here
	if err != nil {
		t.Fatalf("NewOrgsService: %v", err)
	}
	org, err := svc.CreateOrg(ctx, adminID, "Invite-email fixture")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE id = $1::uuid`, org.ID)
	})

	mailer := newFakeOrgMailer()
	h := &OrgsHandler{Svc: svc, Mailer: mailer}

	body := bytes.NewBufferString(`{"email":"` + inviteeEmail + `","role":"member"}`)
	req := httptest.NewRequest(http.MethodPost, "/platform/orgs/"+org.ID+"/members", body)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", org.ID)
	rctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
	rctx = auth.ContextWithClaims(rctx, &auth.Claims{Subject: adminID, Email: adminEmail})
	req = req.WithContext(rctx)
	w := httptest.NewRecorder()
	h.HandleInviteMember().ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201 (body: %s)", w.Code, w.Body.String())
	}

	// Fire-and-forget: block up to 2s waiting for the goroutine to
	// hit the mailer, then assert the recorded fields match.
	mailer.waitCalled(t, 2*time.Second)
	mailer.mu.Lock()
	defer mailer.mu.Unlock()
	if mailer.calls != 1 {
		t.Errorf("calls: got %d, want 1", mailer.calls)
	}
	if mailer.invitedEmail != inviteeEmail {
		t.Errorf("invited_email: got %q, want %q", mailer.invitedEmail, inviteeEmail)
	}
	if mailer.orgName != "Invite-email fixture" {
		t.Errorf("org_name: got %q, want %q", mailer.orgName, "Invite-email fixture")
	}
	if mailer.inviterEmail != adminEmail {
		t.Errorf("inviter_email: got %q, want %q", mailer.inviterEmail, adminEmail)
	}
}

// TestHandleInviteMember_NoMailerConfigured pins the "skip send when
// nil mailer" branch — dev / test envs without SMTP boot with
// Mailer=nil and must still be able to invite members without
// crashing.
func TestHandleInviteMember_NoMailerConfigured(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	adminEmail := "org-invite-nomailer-admin@test.eurobase.local"
	inviteeEmail := "org-invite-nomailer-target@test.eurobase.local"
	adminID := insertTestPlatformUser(t, pool, adminEmail)
	_ = insertTestPlatformUser(t, pool, inviteeEmail)
	if _, err := pool.Exec(ctx,
		`UPDATE public.platform_users SET team_beta_access = true WHERE id = $1::uuid`,
		adminID,
	); err != nil {
		t.Fatalf("grant team beta: %v", err)
	}

	svc, err := NewOrgsService(pool, nil)
	if err != nil {
		t.Fatalf("NewOrgsService: %v", err)
	}
	org, err := svc.CreateOrg(ctx, adminID, "No-mailer fixture")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE id = $1::uuid`, org.ID)
	})

	h := &OrgsHandler{Svc: svc, Mailer: nil}

	body := bytes.NewBufferString(`{"email":"` + inviteeEmail + `","role":"member"}`)
	req := httptest.NewRequest(http.MethodPost, "/platform/orgs/"+org.ID+"/members", body)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", org.ID)
	rctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
	rctx = auth.ContextWithClaims(rctx, &auth.Claims{Subject: adminID, Email: adminEmail})
	req = req.WithContext(rctx)
	w := httptest.NewRecorder()
	h.HandleInviteMember().ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201 (body: %s)", w.Code, w.Body.String())
	}
	// If we're still here, the nil-mailer branch didn't panic.
}
