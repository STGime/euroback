package billing

import (
	"context"
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

// After a project changes owner, invoices must stay with the party that was
// billed (billed_to_user_id), not follow projects.owner_id — otherwise the
// new owner could read the previous owner's invoices (name / address / VAT)
// and on-demand re-render would corrupt them. Regression test for the
// ownership-transfer review finding H1.
func TestInvoiceAccess_FollowsBilledToNotOwner(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in -short mode")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://eurobase_api:localdev@localhost:5433/eurobase?sslmode=disable"
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect to test database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("cannot ping test database: %v", err)
	}
	defer pool.Close()

	unique := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	oldEmail := "inv-old-" + unique + "@eurobase.app"
	newEmail := "inv-new-" + unique + "@eurobase.app"
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
		 VALUES ($1,$2,$3,$4,$5,'fr-par','pro','active') RETURNING id`,
		oldID, "Invoice Test", "test-inv-"+unique, "tenant_test_inv_"+unique, "eurobase-test-inv-"+unique,
	).Scan(&projectID); err != nil {
		t.Skipf("seed project: %v", err)
	}

	var invoiceID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO public.invoices (project_id, mollie_payment_id, amount_cents, currency, status, billed_to_user_id)
		 VALUES ($1, $2, 1900, 'EUR', 'paid', $3) RETURNING id`,
		projectID, "tr_inv_"+unique, oldID,
	).Scan(&invoiceID); err != nil {
		t.Fatalf("seed invoice: %v", err)
	}

	// Simulate the transfer: ownership flips to the new owner.
	if _, err := pool.Exec(ctx, `UPDATE projects SET owner_id = $1 WHERE id = $2`, newID, projectID); err != nil {
		t.Fatalf("flip owner: %v", err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM public.invoices WHERE project_id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM public.projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE id = ANY($1)`, []string{oldID, newID})
	})

	svc := &Service{pool: pool, enabled: true}

	listAs := func(userID string) string {
		req := httptest.NewRequest(http.MethodGet, "/platform/billing/invoices", nil)
		req = req.WithContext(auth.ContextWithClaims(req.Context(), &auth.Claims{Subject: userID, Email: "x@x"}))
		w := httptest.NewRecorder()
		HandleListInvoices(svc)(w, req)
		return w.Body.String()
	}
	// The party billed (old owner) still sees the invoice; the new owner does not.
	if !strings.Contains(listAs(oldID), invoiceID) {
		t.Errorf("old owner (billed party) can't see their own invoice")
	}
	if strings.Contains(listAs(newID), invoiceID) {
		t.Errorf("LEAK: new owner can see the previous owner's invoice")
	}

	downloadAs := func(userID string) int {
		req := httptest.NewRequest(http.MethodGet, "/platform/billing/invoices/"+invoiceID+"/pdf", nil)
		rc := chi.NewRouteContext()
		rc.URLParams.Add("id", invoiceID)
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
		ctx = auth.ContextWithClaims(ctx, &auth.Claims{Subject: userID, Email: "x@x"})
		w := httptest.NewRecorder()
		HandleDownloadInvoicePDF(svc)(w, req.WithContext(ctx))
		return w.Code
	}
	// New owner → 404 (not their invoice). Old owner → not 404 (access granted;
	// 503 here because storage isn't configured in this test, which still
	// proves the ownership check passed).
	if code := downloadAs(newID); code != http.StatusNotFound {
		t.Errorf("new owner download = %d, want 404 (no access to previous owner's invoice)", code)
	}
	if code := downloadAs(oldID); code == http.StatusNotFound {
		t.Errorf("old owner (billed party) download = 404, want access granted")
	}
}
