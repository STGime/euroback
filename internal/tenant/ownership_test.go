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
		     (project_id, new_owner_email, token_hash, initiated_by, expires_at)
		 VALUES ($1,$2,$3,$4,$5)`,
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
	HandleInitiateTransfer(fx.pool, reauth, nil)(w, req)
	return w
}

func acceptRequest(fx *transferFixture, asUserID, asEmail, token string, cancelSub CancelSubAtPeriodEnd, checkLimit CheckOwnerProjectLimit) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"token": token})
	req := httptest.NewRequest(http.MethodPost, "/platform/transfers/accept", strings.NewReader(string(body)))
	req = req.WithContext(auth.ContextWithClaims(req.Context(), &auth.Claims{Subject: asUserID, Email: asEmail}))
	w := httptest.NewRecorder()
	HandleAcceptTransfer(fx.pool, cancelSub, checkLimit)(w, req)
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

	var gotOwner, gotSub string
	cancelSub := func(ctx context.Context, ownerID, subscriptionID string) error {
		gotOwner, gotSub = ownerID, subscriptionID
		return nil
	}

	w := acceptRequest(fx, fx.newOwnerID, fx.newEmail, fx.rawToken, cancelSub, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if gotOwner != fx.oldOwnerID {
		t.Errorf("cancelSub owner = %s, want old owner %s (must run before the owner flip)", gotOwner, fx.oldOwnerID)
	}
	if gotSub != subID {
		t.Errorf("cancelSub subID = %s, want %s", gotSub, subID)
	}
	var resp struct {
		BillingHandover bool `json:"billing_handover"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.BillingHandover {
		t.Errorf("billing_handover = false, want true")
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

	w := initiateRequest(fx, fx.oldOwnerID, fx.oldEmail, "owner", "new-client@example.com", "", nil)
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
	w := initiateRequest(fx, fx.oldOwnerID, fx.oldEmail, "owner", "new-client@example.com", "", reauth)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "reauth_required") {
		t.Fatalf("status = %d body=%s, want 403 reauth_required", w.Code, w.Body.String())
	}
	// With password → succeeds (201).
	w = initiateRequest(fx, fx.oldOwnerID, fx.oldEmail, "owner", "new-client@example.com", "hunter2", reauth)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
}
