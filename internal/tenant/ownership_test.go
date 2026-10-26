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

	return &transferFixture{pool: pool, oldOwnerID: oldID, newOwnerID: newID, newEmail: newEmail, projectID: projectID, rawToken: raw}
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
