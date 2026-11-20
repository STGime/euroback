package enduser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// The SDK user admin API (/v1/auth/admin/users) against a provisioned
// tenant: every route, with the secret key, through the same pool role the
// shared cluster uses (eurobase_gateway, RLS applies: the service role
// admits). ENDUSER_PASSKEY_TEST_URL (superuser, setup) and optionally
// ENDUSER_GATEWAY_TEST_URL (the handlers' pool; default: the superuser).
func TestAdminAPI_DB(t *testing.T) {
	adminURL := os.Getenv("ENDUSER_PASSKEY_TEST_URL")
	if adminURL == "" {
		t.Skip("ENDUSER_PASSKEY_TEST_URL not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Skipf("connect: %v", err)
	}
	t.Cleanup(admin.Close)
	pool := admin
	if gwURL := os.Getenv("ENDUSER_GATEWAY_TEST_URL"); gwURL != "" {
		gw, err := pgxpool.New(ctx, gwURL)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(gw.Close)
		pool = gw
	}

	pid := uuid.NewString()
	ownerEmail := "admin-api-" + pid + "@t.local"
	var ownerID, schema string
	if err := admin.QueryRow(ctx, `INSERT INTO platform_users (email) VALUES ($1) RETURNING id`, ownerEmail).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		VALUES ($1,$2,'AdminAPI',$3,'tenant_adminapi',$4,'fr-par','free','provisioning')`, pid, ownerID, "adminapi-"+pid[:8], "adminapi-"+pid); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `SELECT provision_tenant($1,'AdminAPI','free')`, pid); err != nil {
		t.Fatal(err)
	}
	_ = admin.QueryRow(ctx, `SELECT schema_name FROM projects WHERE id=$1`, pid).Scan(&schema)
	t.Cleanup(func() {
		c := context.Background()
		_, _ = admin.Exec(c, `SELECT deprovision_tenant($1)`, pid)
		_, _ = admin.Exec(c, `DELETE FROM audit_log WHERE project_id=$1`, pid)
		_, _ = admin.Exec(c, `DELETE FROM projects WHERE id=$1`, pid)
		_, _ = admin.Exec(c, `DELETE FROM platform_users WHERE id=$1`, ownerID)
	})

	auditSvc := audit.NewService(admin)
	router := func(keyType string) http.Handler {
		r := chi.NewRouter()
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c := auth.ContextWithProject(r.Context(), &auth.ProjectContext{ProjectID: pid, SchemaName: schema, KeyType: keyType})
				next.ServeHTTP(w, r.WithContext(audit.WithContext(c, auditSvc)))
			})
		})
		r.Mount("/v1/auth/admin/users", AdminRoutes(pool))
		return r
	}
	secret, public := router("secret"), router("public")
	call := func(h http.Handler, method, path, body string) (int, string) {
		req := httptest.NewRequest(method, "/v1/auth/admin/users"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	if code, body := call(public, "POST", "", `{"email":"pk@example.com"}`); code != http.StatusForbidden {
		t.Fatalf("public key create: %d %s", code, body)
	}

	// Create: metadata, unconfirmed, with a password.
	code, body := call(secret, "POST", "", `{"email":"Partner@Example.com","password":"Partner-pass-1","email_confirmed":false,"display_name":"Partner","metadata":{"company":"Acme"}}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var u PlatformUser
	_ = json.Unmarshal([]byte(body), &u)
	if u.Email == nil || *u.Email != "partner@example.com" || u.EmailConfirmedAt != nil || u.Metadata["company"] != "Acme" {
		t.Fatalf("created user = %s", body)
	}
	if strings.Contains(body, "password") {
		t.Fatalf("response leaks password fields: %s", body)
	}
	if code, body := call(secret, "POST", "", `{"email":"partner@example.com"}`); code != http.StatusConflict || !strings.Contains(body, "email_exists") {
		t.Fatalf("duplicate: %d %s (want 409)", code, body)
	}
	if code, _ := call(secret, "POST", "", `{"email":"short@example.com","password":"short"}`); code != http.StatusBadRequest {
		t.Fatalf("short password: %d", code)
	}
	if code, _ := call(secret, "POST", "", `{"email":"not-an-email"}`); code != http.StatusBadRequest {
		t.Fatalf("bad email: %d", code)
	}
	for _, e := range []string{"b@example.com", "c@example.com"} {
		if code, body := call(secret, "POST", "", `{"email":"`+e+`"}`); code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", e, code, body)
		}
	}

	// List: page / per_page / search.
	var list PlatformUserList
	code, body = call(secret, "GET", "?page=2&per_page=2", "")
	_ = json.Unmarshal([]byte(body), &list)
	if code != 200 || list.Total != 3 || len(list.Users) != 1 {
		t.Fatalf("page 2 of 2-per-page: %d total=%d users=%d", code, list.Total, len(list.Users))
	}
	code, body = call(secret, "GET", "?search=partner", "")
	_ = json.Unmarshal([]byte(body), &list)
	if code != 200 || list.Total != 1 || list.Users[0].ID != u.ID {
		t.Fatalf("search: %d %s", code, body)
	}

	// Get / 404.
	if code, body := call(secret, "GET", "/"+u.ID, ""); code != 200 || !strings.Contains(body, u.ID) {
		t.Fatalf("get: %d %s", code, body)
	}
	if code, _ := call(secret, "GET", "/"+uuid.NewString(), ""); code != http.StatusNotFound {
		t.Fatalf("get unknown: %d", code)
	}
	if code, _ := call(secret, "PATCH", "/"+uuid.NewString(), `{"display_name":"x"}`); code != http.StatusNotFound {
		t.Fatalf("patch unknown: %d", code)
	}

	// Update.
	code, body = call(secret, "PATCH", "/"+u.ID, `{"display_name":"Partner GmbH","email_confirmed":true,"metadata":{"company":"Acme","tier":"gold"}}`)
	if code != 200 || !strings.Contains(body, "Partner GmbH") || strings.Contains(body, `"email_confirmed_at":null`) || !strings.Contains(body, "gold") {
		t.Fatalf("update: %d %s", code, body)
	}
	if code, _ := call(secret, "PATCH", "/"+u.ID, `{"email":"b@example.com"}`); code != http.StatusConflict {
		t.Fatalf("update to a taken email: %d (want 409)", code)
	}

	liveTokens := func() int {
		var n int
		_ = admin.QueryRow(ctx, `SELECT count(*) FROM `+quoteIdent(schema)+`.refresh_tokens WHERE user_id=$1 AND revoked_at IS NULL`, u.ID).Scan(&n)
		return n
	}
	addTokens := func(n int) {
		for i := 0; i < n; i++ {
			if _, err := admin.Exec(ctx, `INSERT INTO `+quoteIdent(schema)+`.refresh_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '1 day')`, u.ID, uuid.NewString()); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Sign out everywhere.
	addTokens(2)
	code, body = call(secret, "POST", "/"+u.ID+"/signout", "")
	if code != 200 || !strings.Contains(body, `"refresh_tokens_revoked":2`) || liveTokens() != 0 {
		t.Fatalf("signout: %d %s live=%d", code, body, liveTokens())
	}
	if code, _ := call(secret, "POST", "/"+uuid.NewString()+"/signout", ""); code != http.StatusNotFound {
		t.Fatalf("signout unknown: %d", code)
	}

	// Suspend revokes refresh tokens; twice → 404; unsuspend.
	addTokens(1)
	code, body = call(secret, "POST", "/"+u.ID+"/suspend", "")
	if code != 200 || strings.Contains(body, `"banned_at":null`) || liveTokens() != 0 {
		t.Fatalf("suspend: %d %s live=%d", code, body, liveTokens())
	}
	if code, _ := call(secret, "POST", "/"+u.ID+"/suspend", ""); code != http.StatusNotFound {
		t.Fatalf("suspend twice: %d", code)
	}
	if code, body := call(secret, "DELETE", "/"+u.ID+"/suspend", ""); code != 200 || !strings.Contains(body, `"banned_at":null`) {
		t.Fatalf("unsuspend: %d %s", code, body)
	}

	// Set password: rules, hash, refresh tokens revoked.
	if code, _ := call(secret, "POST", "/"+u.ID+"/password", `{"password":"short"}`); code != http.StatusBadRequest {
		t.Fatalf("short password: %d", code)
	}
	addTokens(1)
	if code, body := call(secret, "POST", "/"+u.ID+"/password", `{"password":"New-partner-pass-2"}`); code != 200 || liveTokens() != 0 {
		t.Fatalf("set password: %d %s live=%d", code, body, liveTokens())
	}
	var hash string
	_ = admin.QueryRow(ctx, `SELECT password_hash FROM `+quoteIdent(schema)+`.users WHERE id=$1`, u.ID).Scan(&hash)
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("New-partner-pass-2")) != nil {
		t.Fatal("password not set")
	}
	if cost, _ := bcrypt.Cost([]byte(hash)); cost != 12 {
		t.Fatalf("bcrypt cost %d", cost)
	}

	// Delete: a file the user uploaded still references them → 409.
	if _, err := admin.Exec(ctx, `INSERT INTO `+quoteIdent(schema)+`.storage_objects (key, uploaded_by) VALUES ('u/file.txt', $1)`, u.ID); err != nil {
		t.Fatal(err)
	}
	if code, body := call(secret, "DELETE", "/"+u.ID, ""); code != http.StatusConflict || !strings.Contains(body, "user_referenced") || !strings.Contains(body, "storage_objects") {
		t.Fatalf("delete with a file: %d %s (want 409 user_referenced)", code, body)
	}
	if _, err := admin.Exec(ctx, `DELETE FROM `+quoteIdent(schema)+`.storage_objects WHERE key = 'u/file.txt'`); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(secret, "DELETE", "/"+u.ID, ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := call(secret, "GET", "/"+u.ID, ""); code != http.StatusNotFound {
		t.Fatalf("get after delete: %d", code)
	}
	if code, _ := call(secret, "DELETE", "/"+u.ID, ""); code != http.StatusNotFound {
		t.Fatalf("delete twice: %d", code)
	}

	// Audit: every change, the key as actor, no actor id.
	rows, err := admin.Query(ctx, `SELECT action, actor_id IS NULL, actor_email, metadata->>'via', target_id FROM audit_log WHERE project_id=$1 ORDER BY seq`, pid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]int{}
	for rows.Next() {
		var action, email, via, target string
		var noActor bool
		if err := rows.Scan(&action, &noActor, &email, &via, &target); err != nil {
			t.Fatal(err)
		}
		if !noActor || email != "secret API key" || via != "secret_key" || target == "" {
			t.Errorf("audit row %s: actor_id null=%v email=%q via=%q target=%q", action, noActor, email, via, target)
		}
		seen[action]++
	}
	for _, a := range []string{audit.ActionEndUserCreated, audit.ActionEndUserUpdated, audit.ActionEndUserSignedOut,
		audit.ActionEndUserSuspended, audit.ActionEndUserUnsuspended, audit.ActionEndUserPasswordSet, audit.ActionEndUserDeleted} {
		if seen[a] == 0 {
			t.Errorf("no audit entry for %s (seen %v)", a, seen)
		}
	}
	if seen[audit.ActionEndUserCreated] != 3 {
		t.Errorf("created entries = %d, want 3 (refused creates aren't audited)", seen[audit.ActionEndUserCreated])
	}
}
