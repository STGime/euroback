package tenant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// renderTransferEmail is pure — no DB needed.
func TestRenderTransferEmail(t *testing.T) {
	t.Setenv("CONSOLE_URL", "https://console.example.test")
	html := renderTransferEmail("Floorball <App>", "old@example.com", "rawtoken123")

	if !strings.Contains(html, "https://console.example.test/transfer?token=rawtoken123") {
		t.Errorf("accept URL missing or wrong:\n%s", html)
	}
	// Project name and sender must be HTML-escaped.
	if !strings.Contains(html, "Floorball &lt;App&gt;") {
		t.Errorf("project name not escaped")
	}
	if strings.Contains(html, "Floorball <App>") {
		t.Errorf("raw unescaped project name leaked into HTML")
	}
}

type transferFixture struct {
	pool       *pgxpool.Pool
	oldOwnerID string
	oldEmail   string
	newOwnerID string
	newEmail   string
	projectID  string
	rawToken   string
}

// seedTransfer creates two users, a project owned by the first, an owner
// membership row, and a pending transfer to the second user.
func seedTransfer(t *testing.T, pool *pgxpool.Pool, plan string, expiresAt time.Time) *transferFixture {
	t.Helper()
	ctx := context.Background()
	unique := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	oldEmail := "xfer-old-" + unique + "@eurobase.app"
	newEmail := "xfer-new-" + unique + "@eurobase.app"

	_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE email = ANY($1)`, []string{oldEmail, newEmail})

	var oldID, newID string
	if err := pool.QueryRow(ctx, `INSERT INTO platform_users (email) VALUES ($1) RETURNING id`, oldEmail).Scan(&oldID); err != nil {
		t.Skipf("seed old owner: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO platform_users (email) VALUES ($1) RETURNING id`, newEmail).Scan(&newID); err != nil {
		t.Skipf("seed new owner: %v", err)
	}

	var projectID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		 VALUES ($1,$2,$3,$4,$5,'fr-par',$6,'active') RETURNING id`,
		oldID, "Transfer Test", "test-xfer-"+unique, "tenant_test_xfer_"+unique, "eurobase-test-xfer-"+unique, plan,
	).Scan(&projectID); err != nil {
		t.Skipf("seed project: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO public.project_members (project_id, user_id, role) VALUES ($1,$2,'owner')`,
		projectID, oldID,
	); err != nil {
		t.Fatalf("seed owner membership: %v", err)
	}

	raw := "tok-" + unique
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.project_ownership_transfers
		     (project_id, new_owner_email, token_hash, initiated_by, expires_at, sent_at)
		 VALUES ($1,$2,$3,$4,$5, now() - interval '2 minutes')`,
		projectID, newEmail, hashToken(raw), oldID, expiresAt,
	); err != nil {
		t.Fatalf("seed transfer: %v", err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM public.project_ownership_transfers WHERE project_id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM public.subscriptions WHERE project_id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM public.project_members WHERE project_id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM public.projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE id = ANY($1)`, []string{oldID, newID})
	})

	return &transferFixture{pool: pool, oldOwnerID: oldID, oldEmail: oldEmail, newOwnerID: newID, newEmail: newEmail, projectID: projectID, rawToken: raw}
}

// initiateRequest drives HandleInitiateTransfer with a project route param,
// claims, and a stashed caller role (set by the real route middleware).
func initiateRequest(fx *transferFixture, asUserID, asEmail, role, toEmail, password string, reauth ReauthFunc) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"email": toEmail, "current_password": password})
	req := httptest.NewRequest(http.MethodPost, "/platform/projects/"+fx.projectID+"/transfer", strings.NewReader(string(body)))
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", fx.projectID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
	ctx = auth.ContextWithClaims(ctx, &auth.Claims{Subject: asUserID, Email: asEmail})
	ctx = WithRole(ctx, role)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	HandleInitiateTransfer(fx.pool, reauth, nil, nil)(w, req)
	return w
}

func acceptRequest(fx *transferFixture, asUserID, asEmail, token string, handover BillingHandover, checkLimit CheckOwnerProjectLimit) *httptest.ResponseRecorder {
	return acceptRequestWith(fx, asUserID, asEmail, token, handover, nil, checkLimit)
}

func acceptRequestWith(fx *transferFixture, asUserID, asEmail, token string, handover BillingHandover, check CheckTransferable, checkLimit CheckOwnerProjectLimit) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"token": token})
	req := httptest.NewRequest(http.MethodPost, "/platform/transfers/accept", strings.NewReader(string(body)))
	req = req.WithContext(auth.ContextWithClaims(req.Context(), &auth.Claims{Subject: asUserID, Email: asEmail}))
	w := httptest.NewRecorder()
	HandleAcceptTransfer(fx.pool, handover, check, checkLimit)(w, req)
	return w
}

func TestAcceptTransfer_FreeProject(t *testing.T) {
	pool := setupTestDB(t)
	fx := seedTransfer(t, pool, "free", time.Now().Add(time.Hour))

	w := acceptRequest(fx, fx.newOwnerID, fx.newEmail, fx.rawToken, nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	ctx := context.Background()
	var ownerID string
	_ = pool.QueryRow(ctx, `SELECT owner_id FROM projects WHERE id=$1`, fx.projectID).Scan(&ownerID)
	if ownerID != fx.newOwnerID {
		t.Errorf("owner_id = %s, want new owner %s", ownerID, fx.newOwnerID)
	}
	var newRole, oldRole string
	_ = pool.QueryRow(ctx, `SELECT role FROM project_members WHERE project_id=$1 AND user_id=$2`, fx.projectID, fx.newOwnerID).Scan(&newRole)
	_ = pool.QueryRow(ctx, `SELECT role FROM project_members WHERE project_id=$1 AND user_id=$2`, fx.projectID, fx.oldOwnerID).Scan(&oldRole)
	if newRole != "owner" {
		t.Errorf("new member role = %q, want owner", newRole)
	}
	if oldRole != "admin" {
		t.Errorf("old owner role = %q, want admin", oldRole)
	}
	var acceptedAt *time.Time
	_ = pool.QueryRow(ctx, `SELECT accepted_at FROM project_ownership_transfers WHERE project_id=$1`, fx.projectID).Scan(&acceptedAt)
	if acceptedAt == nil {
		t.Errorf("accepted_at not stamped")
	}
}

func TestAcceptTransfer_EmailMismatch(t *testing.T) {
	pool := setupTestDB(t)
	fx := seedTransfer(t, pool, "free", time.Now().Add(time.Hour))

	w := acceptRequest(fx, fx.newOwnerID, "someone-else@eurobase.app", fx.rawToken, nil, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	// Ownership must not have changed.
	var ownerID string
	_ = pool.QueryRow(context.Background(), `SELECT owner_id FROM projects WHERE id=$1`, fx.projectID).Scan(&ownerID)
	if ownerID != fx.oldOwnerID {
		t.Errorf("owner_id changed on a rejected transfer")
	}
}

func TestAcceptTransfer_Expired(t *testing.T) {
	pool := setupTestDB(t)
	fx := seedTransfer(t, pool, "free", time.Now().Add(-time.Hour))

	w := acceptRequest(fx, fx.newOwnerID, fx.newEmail, fx.rawToken, nil, nil)
	if w.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410; body=%s", w.Code, w.Body.String())
	}
}

func TestAcceptTransfer_BillingHandover(t *testing.T) {
	pool := setupTestDB(t)
	fx := seedTransfer(t, pool, "pro", time.Now().Add(time.Hour))

	// A live subscription on the project.
	var subID string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO public.subscriptions (project_id, plan, status) VALUES ($1,'pro','active') RETURNING id`,
		fx.projectID,
	).Scan(&subID); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	_ = subID
	paidUntil := time.Now().Add(10 * 24 * time.Hour).UTC().Truncate(time.Second)
	var gotProject, ownerAtHandover string
	finished := false
	handover := func(ctx context.Context, tx pgx.Tx, projectID string) (bool, *time.Time, func(context.Context), error) {
		gotProject = projectID
		// Runs in the ownership transaction, after the owner change.
		_ = tx.QueryRow(ctx, `SELECT owner_id FROM public.projects WHERE id = $1`, projectID).Scan(&ownerAtHandover)
		return true, &paidUntil, func(context.Context) { finished = true }, nil
	}

	w := acceptRequest(fx, fx.newOwnerID, fx.newEmail, fx.rawToken, handover, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if gotProject != fx.projectID {
		t.Errorf("handover project = %q, want %s", gotProject, fx.projectID)
	}
	if ownerAtHandover != fx.newOwnerID {
		t.Errorf("owner at handover = %s, want new owner %s (handover runs after the flip)", ownerAtHandover, fx.newOwnerID)
	}
	if !finished {
		t.Error("the post-commit part of the handover didn't run")
	}
	var resp struct {
		BillingHandover bool       `json:"billing_handover"`
		PaidUntil       *time.Time `json:"paid_until"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.BillingHandover {
		t.Errorf("billing_handover = false, want true")
	}
	if resp.PaidUntil == nil || !resp.PaidUntil.Equal(paidUntil) {
		t.Errorf("paid_until = %v, want %v", resp.PaidUntil, paidUntil)
	}
}

func TestAcceptTransfer_ProjectLimitReached(t *testing.T) {
	pool := setupTestDB(t)
	fx := seedTransfer(t, pool, "free", time.Now().Add(time.Hour))

	checkLimit := func(ctx context.Context, ownerID string) error {
		return fmt.Errorf("at limit")
	}
	w := acceptRequest(fx, fx.newOwnerID, fx.newEmail, fx.rawToken, nil, checkLimit)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	var ownerID string
	_ = pool.QueryRow(context.Background(), `SELECT owner_id FROM projects WHERE id=$1`, fx.projectID).Scan(&ownerID)
	if ownerID != fx.oldOwnerID {
		t.Errorf("owner_id changed despite project-limit rejection")
	}
}

// A second accept of an already-accepted token is rejected (single-use).
func TestAcceptTransfer_DoubleAccept(t *testing.T) {
	pool := setupTestDB(t)
	fx := seedTransfer(t, pool, "free", time.Now().Add(time.Hour))

	if w := acceptRequest(fx, fx.newOwnerID, fx.newEmail, fx.rawToken, nil, nil); w.Code != http.StatusOK {
		t.Fatalf("first accept status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	w := acceptRequest(fx, fx.newOwnerID, fx.newEmail, fx.rawToken, nil, nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("second accept status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
}

// The recipient is already a member (admin) → promoted to owner, not duplicated.
func TestAcceptTransfer_RecipientAlreadyMember(t *testing.T) {
	pool := setupTestDB(t)
	fx := seedTransfer(t, pool, "free", time.Now().Add(time.Hour))
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO public.project_members (project_id, user_id, role) VALUES ($1,$2,'admin')`,
		fx.projectID, fx.newOwnerID); err != nil {
		t.Fatalf("seed existing membership: %v", err)
	}

	if w := acceptRequest(fx, fx.newOwnerID, fx.newEmail, fx.rawToken, nil, nil); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var role string
	var n int
	_ = pool.QueryRow(context.Background(), `SELECT role FROM project_members WHERE project_id=$1 AND user_id=$2`, fx.projectID, fx.newOwnerID).Scan(&role)
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM project_members WHERE project_id=$1 AND user_id=$2`, fx.projectID, fx.newOwnerID).Scan(&n)
	if role != "owner" || n != 1 {
		t.Errorf("recipient role=%q count=%d, want owner x1", role, n)
	}
}

// Team-tier projects are refused at accept (deferred scope).
func TestAcceptTransfer_TeamPlanRefused(t *testing.T) {
	pool := setupTestDB(t)
	fx := seedTransfer(t, pool, "team", time.Now().Add(time.Hour))

	w := acceptRequest(fx, fx.newOwnerID, fx.newEmail, fx.rawToken, nil, nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
	var ownerID string
	_ = pool.QueryRow(context.Background(), `SELECT owner_id FROM projects WHERE id=$1`, fx.projectID).Scan(&ownerID)
	if ownerID != fx.oldOwnerID {
		t.Errorf("owner_id changed on a refused Team transfer")
	}
}

// A suspended recipient can't acquire the project (M3).
func TestAcceptTransfer_SuspendedRecipient(t *testing.T) {
	pool := setupTestDB(t)
	fx := seedTransfer(t, pool, "free", time.Now().Add(time.Hour))
	if _, err := pool.Exec(context.Background(),
		`UPDATE platform_users SET suspended_at = now() WHERE id = $1`, fx.newOwnerID); err != nil {
		t.Fatalf("suspend recipient: %v", err)
	}

	w := acceptRequest(fx, fx.newOwnerID, fx.newEmail, fx.rawToken, nil, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	var ownerID string
	_ = pool.QueryRow(context.Background(), `SELECT owner_id FROM projects WHERE id=$1`, fx.projectID).Scan(&ownerID)
	if ownerID != fx.oldOwnerID {
		t.Errorf("owner_id changed for a suspended recipient")
	}
}

// A live subscription with no billing hook wired is refused, not transferred
// unsafely (M4).
func TestAcceptTransfer_LiveSubNoBillingHook(t *testing.T) {
	pool := setupTestDB(t)
	fx := seedTransfer(t, pool, "pro", time.Now().Add(time.Hour))
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO public.subscriptions (project_id, plan, status) VALUES ($1,'pro','active')`,
		fx.projectID); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	w := acceptRequest(fx, fx.newOwnerID, fx.newEmail, fx.rawToken, nil /* no billing hook */, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
	var ownerID string
	_ = pool.QueryRow(context.Background(), `SELECT owner_id FROM projects WHERE id=$1`, fx.projectID).Scan(&ownerID)
	if ownerID != fx.oldOwnerID {
		t.Errorf("owner_id changed despite unavailable billing handover")
	}
}

func TestInitiateTransfer_SelfRefused(t *testing.T) {
	pool := setupTestDB(t)
	fx := seedTransfer(t, pool, "free", time.Now().Add(time.Hour))

	w := initiateRequest(fx, fx.oldOwnerID, fx.oldEmail, "owner", fx.oldEmail, "", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (self-transfer); body=%s", w.Code, w.Body.String())
	}
}

func TestInitiateTransfer_TeamPlanRefused(t *testing.T) {
	pool := setupTestDB(t)
	fx := seedTransfer(t, pool, "legal_team", time.Now().Add(time.Hour))

	w := initiateRequest(fx, fx.oldOwnerID, fx.oldEmail, "owner", "new-client@eurobase.app", "", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (team plan); body=%s", w.Code, w.Body.String())
	}
}

func TestInitiateTransfer_ReauthRequired(t *testing.T) {
	pool := setupTestDB(t)
	fx := seedTransfer(t, pool, "free", time.Now().Add(time.Hour))

	reauth := func(ctx context.Context, c *auth.Claims, pw string) error {
		if pw == "" {
			return auth.ErrReauthRequired
		}
		return nil
	}
	// No password → reauth_required.
	w := initiateRequest(fx, fx.oldOwnerID, fx.oldEmail, "owner", "new-client@eurobase.app", "", reauth)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "reauth_required") {
		t.Fatalf("status = %d body=%s, want 403 reauth_required", w.Code, w.Body.String())
	}
	// With password → succeeds (201).
	w = initiateRequest(fx, fx.oldOwnerID, fx.oldEmail, "owner", "new-client@eurobase.app", "hunter2", reauth)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
}

// The owner redirected (or withdrew) the offer after the recipient opened
// it: the old token no longer matches the row and the accept is refused.
func TestAcceptTransfer_OfferChangedMeanwhile(t *testing.T) {
	pool := setupTestDB(t)
	fx := seedTransfer(t, pool, "free", time.Now().Add(time.Hour))
	if _, err := pool.Exec(context.Background(),
		`UPDATE public.project_ownership_transfers SET token_hash = $2 WHERE project_id = $1`,
		fx.projectID, hashToken("a-newer-offer"),
	); err != nil {
		t.Fatal(err)
	}
	w := acceptRequest(fx, fx.newOwnerID, fx.newEmail, fx.rawToken, nil, nil)
	if w.Code == http.StatusOK {
		t.Fatalf("accept with a replaced token succeeded; body=%s", w.Body.String())
	}
	assertOwner(t, fx, fx.oldOwnerID)
}

func TestAcceptTransfer_SuspendedProjectOrSender(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	fx := seedTransfer(t, pool, "free", time.Now().Add(time.Hour))
	if _, err := pool.Exec(ctx, `UPDATE public.projects SET status = 'suspended' WHERE id = $1`, fx.projectID); err != nil {
		t.Fatal(err)
	}
	if w := acceptRequest(fx, fx.newOwnerID, fx.newEmail, fx.rawToken, nil, nil); w.Code != http.StatusConflict {
		t.Fatalf("suspended project: status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
	assertOwner(t, fx, fx.oldOwnerID)

	fx2 := seedTransfer(t, pool, "free", time.Now().Add(time.Hour))
	if _, err := pool.Exec(ctx, `UPDATE public.platform_users SET suspended_at = now() WHERE id = $1`, fx2.oldOwnerID); err != nil {
		t.Fatal(err)
	}
	if w := acceptRequest(fx2, fx2.newOwnerID, fx2.newEmail, fx2.rawToken, nil, nil); w.Code != http.StatusConflict {
		t.Fatalf("suspended sender: status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
	assertOwner(t, fx2, fx2.oldOwnerID)
}

func TestAcceptTransfer_PaymentOverdueRefused(t *testing.T) {
	pool := setupTestDB(t)
	fx := seedTransfer(t, pool, "pro", time.Now().Add(time.Hour))
	overdue := func(ctx context.Context, projectID string) error { return ErrTransferPaymentOverdue }
	called := false
	handover := func(ctx context.Context, tx pgx.Tx, projectID string) (bool, *time.Time, func(context.Context), error) {
		called = true
		return false, nil, nil, nil
	}
	w := acceptRequestWith(fx, fx.newOwnerID, fx.newEmail, fx.rawToken, handover, overdue, nil)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "payment_overdue") {
		t.Fatalf("status = %d, want 409 payment_overdue; body=%s", w.Code, w.Body.String())
	}
	if called {
		t.Error("billing handover ran for a refused transfer")
	}
	assertOwner(t, fx, fx.oldOwnerID)
}

func TestInitiateTransfer_ReservedDomainAndThrottle(t *testing.T) {
	pool := setupTestDB(t)
	fx := seedTransfer(t, pool, "free", time.Now().Add(time.Hour))
	ok := func(context.Context, *auth.Claims, string) error { return nil }

	if w := initiateRequest(fx, fx.oldOwnerID, fx.oldEmail, "owner", "someone@example.com", "", ok); w.Code != http.StatusBadRequest {
		t.Fatalf("reserved domain: status = %d, want 400; body=%s", w.Code, w.Body.String())
	}

	// An offer sent just now can't be replaced within a minute.
	if _, err := pool.Exec(context.Background(),
		`UPDATE public.project_ownership_transfers SET sent_at = now() WHERE project_id = $1`,
		fx.projectID,
	); err != nil {
		t.Fatal(err)
	}
	if w := initiateRequest(fx, fx.oldOwnerID, fx.oldEmail, "owner", "other-"+fx.newEmail, "", ok); w.Code != http.StatusTooManyRequests {
		t.Fatalf("immediate re-send: status = %d, want 429; body=%s", w.Code, w.Body.String())
	}
	if _, err := pool.Exec(context.Background(),
		`UPDATE public.project_ownership_transfers SET sent_at = now() - interval '2 minutes' WHERE project_id = $1`,
		fx.projectID,
	); err != nil {
		t.Fatal(err)
	}
	if w := initiateRequest(fx, fx.oldOwnerID, fx.oldEmail, "owner", "other-"+fx.newEmail, "", ok); w.Code != http.StatusCreated {
		t.Fatalf("re-send after a minute: status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
}

func assertOwner(t *testing.T, fx *transferFixture, want string) {
	t.Helper()
	var owner string
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT owner_id FROM public.projects WHERE id = $1`, fx.projectID,
	).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != want {
		t.Errorf("owner = %s, want %s", owner, want)
	}
}

// A failing billing handover rolls the whole transfer back: ownership and
// billing change together or not at all.
func TestAcceptTransfer_HandoverFailureRollsBack(t *testing.T) {
	pool := setupTestDB(t)
	fx := seedTransfer(t, pool, "pro", time.Now().Add(time.Hour))
	failing := func(ctx context.Context, tx pgx.Tx, projectID string) (bool, *time.Time, func(context.Context), error) {
		return false, nil, nil, fmt.Errorf("boom")
	}
	w := acceptRequest(fx, fx.newOwnerID, fx.newEmail, fx.rawToken, failing, nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", w.Code, w.Body.String())
	}
	assertOwner(t, fx, fx.oldOwnerID)
	// The offer is still open: the recipient can retry.
	if w := acceptRequest(fx, fx.newOwnerID, fx.newEmail, fx.rawToken, nil, nil); w.Code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

// A pending invitation to the recipient is dropped on accept, and an
// invitation can't lower an owner's membership.
func TestAcceptTransfer_DropsRecipientInvitation(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	fx := seedTransfer(t, pool, "free", time.Now().Add(time.Hour))
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.project_invitations (project_id, email, role, token_hash, invited_by, expires_at)
		 VALUES ($1, upper($2), 'viewer', $3, $4, now() + interval '1 day')`,
		fx.projectID, fx.newEmail, hashToken("invite-"+fx.rawToken), fx.oldOwnerID); err != nil {
		t.Fatalf("seed invitation: %v", err)
	}
	if w := acceptRequest(fx, fx.newOwnerID, fx.newEmail, fx.rawToken, nil, nil); w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.project_invitations WHERE project_id = $1`, fx.projectID).Scan(&n)
	if n != 0 {
		t.Errorf("pending invitations for the new owner = %d, want 0", n)
	}
}

// Cancelling doesn't reset the re-send throttle.
func TestCancelTransfer_KeepsThrottle(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	fx := seedTransfer(t, pool, "free", time.Now().Add(time.Hour))
	if _, err := pool.Exec(ctx, `UPDATE public.project_ownership_transfers SET sent_at = now() WHERE project_id = $1`, fx.projectID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/platform/projects/"+fx.projectID+"/transfer", nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", fx.projectID)
	rctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
	rctx = auth.ContextWithClaims(rctx, &auth.Claims{Subject: fx.oldOwnerID, Email: fx.oldEmail})
	rctx = WithRole(rctx, "owner")
	w := httptest.NewRecorder()
	HandleCancelTransfer(pool)(w, req.WithContext(rctx))
	if w.Code != http.StatusOK {
		t.Fatalf("cancel status = %d; body=%s", w.Code, w.Body.String())
	}
	// The link no longer works.
	if w := acceptRequest(fx, fx.newOwnerID, fx.newEmail, fx.rawToken, nil, nil); w.Code == http.StatusOK {
		t.Fatalf("accept after cancel succeeded")
	}
	ok := func(context.Context, *auth.Claims, string) error { return nil }
	if w := initiateRequest(fx, fx.oldOwnerID, fx.oldEmail, "owner", "other-"+fx.newEmail, "", ok); w.Code != http.StatusTooManyRequests {
		t.Fatalf("re-send right after cancel: status = %d, want 429; body=%s", w.Code, w.Body.String())
	}
}
