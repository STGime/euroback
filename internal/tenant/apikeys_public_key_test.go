package tenant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/eurobase/euroback/internal/auth"
)

// listKeys calls HandleListAPIKeys for projectID and returns the response
// plus its raw body (to assert the secret key never appears in it).
func listKeys(t *testing.T, h http.HandlerFunc, projectID string) ([]APIKeyResponse, string) {
	t.Helper()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", projectID)
	req := httptest.NewRequest(http.MethodGet, "/platform/projects/"+projectID+"/api-keys", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list api keys: status %d: %s", rec.Code, rec.Body.String())
	}
	var keys []APIKeyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &keys); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return keys, rec.Body.String()
}

func publicOf(keys []APIKeyResponse) APIKeyResponse {
	for _, k := range keys {
		if k.Type == "public" {
			return k
		}
	}
	return APIKeyResponse{}
}

// TestAPIKeys_PublicKeyRetrievable: the public key is stored in plaintext on
// creation and listed again; the secret key is never stored or listed; a key
// created before migration 000132 (public_key NULL) is backfilled the first
// time the API-key middleware sees it.
func TestAPIKeys_PublicKeyRetrievable(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	svc := &TenantService{pool: pool}

	uid := insertTestPlatformUser(t, pool, "pubkey@test.eurobase.local")
	project, err := svc.CreateProject(ctx, uid, "pubkey@test.eurobase.local", CreateProjectRequest{
		Name: "Public Key", Slug: "test-public-key", Region: "fr-par", Plan: "free",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	t.Cleanup(func() { cleanupProject(t, pool, project.ID) })
	if _, err := pool.Exec(ctx, `UPDATE projects SET status = 'active' WHERE id = $1`, project.ID); err != nil {
		t.Fatalf("activate: %v", err)
	}

	list := HandleListAPIKeys(pool)
	keys, body := listKeys(t, list, project.ID)
	if got := publicOf(keys).PublicKey; got != project.PublicKey {
		t.Fatalf("listed public key = %q, want %q", got, project.PublicKey)
	}
	if strings.Contains(body, project.SecretKey) {
		t.Fatal("secret key appears in the key list")
	}
	var secretStored int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM api_keys WHERE project_id = $1 AND type = 'secret' AND public_key IS NOT NULL`,
		project.ID).Scan(&secretStored); err != nil || secretStored != 0 {
		t.Fatalf("secret row has a plaintext key (count %d, err %v)", secretStored, err)
	}
	// The CHECK refuses a plaintext value on a secret row.
	if _, err := pool.Exec(ctx,
		`UPDATE api_keys SET public_key = 'eb_pk_x' WHERE project_id = $1 AND type = 'secret'`,
		project.ID); err == nil {
		t.Fatal("CHECK allowed public_key on a secret row")
	}

	// The CHECK also refuses a non-eb_pk_ value on a public row.
	if _, err := pool.Exec(ctx,
		`UPDATE api_keys SET public_key = 'eb_sk_x' WHERE project_id = $1 AND type = 'public'`,
		project.ID); err == nil {
		t.Fatal("CHECK allowed a non-eb_pk_ public_key")
	}

	// Simulate a pre-000132 key, then use it (and the secret key) once.
	if _, err := pool.Exec(ctx, `UPDATE api_keys SET public_key = NULL WHERE project_id = $1`, project.ID); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if k, _ := listKeys(t, list, project.ID); publicOf(k).PublicKey != "" {
		t.Fatal("expected no public key before backfill")
	}
	// In CI the middleware runs on the real gateway login, as in
	// production, so a missing UPDATE grant on the new column would show
	// (the middleware's UPDATE error is swallowed).
	mwPool := pool
	if u := os.Getenv("APIKEY_TEST_GATEWAY_URL"); u != "" {
		gp, err := pgxpool.New(ctx, u)
		if err != nil {
			t.Fatalf("gateway pool: %v", err)
		}
		t.Cleanup(gp.Close)
		mwPool = gp
	}
	if _, err := pool.Exec(ctx, `UPDATE api_keys SET last_used_at = NULL WHERE project_id = $1`, project.ID); err != nil {
		t.Fatalf("reset last_used_at: %v", err)
	}
	mw := auth.NewAPIKeyMiddleware(mwPool).Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, key := range []string{project.SecretKey, project.PublicKey} {
		req := httptest.NewRequest(http.MethodGet, "/v1/db/todos", nil)
		req.Header.Set("apikey", key)
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("middleware: status %d: %s", rec.Code, rec.Body.String())
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		k, body := listKeys(t, list, project.ID)
		if strings.Contains(body, project.SecretKey) {
			t.Fatal("secret key appears in the key list after use")
		}
		// The secret key's use must still record last_used_at (its
		// UPDATE passes a NULL $2 — a typing error there would be silent).
		var secretUsed bool
		if err := pool.QueryRow(ctx,
			`SELECT last_used_at IS NOT NULL FROM api_keys WHERE project_id = $1 AND type = 'secret'`,
			project.ID).Scan(&secretUsed); err != nil {
			t.Fatalf("secret last_used_at: %v", err)
		}
		if publicOf(k).PublicKey == project.PublicKey && secretUsed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("public key not backfilled (got %q) or secret last_used_at not set (%v)", publicOf(k).PublicKey, secretUsed)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// A stored value is never overwritten by a later use.
	if _, err := pool.Exec(ctx,
		`UPDATE api_keys SET public_key = 'eb_pk_sentinel' WHERE project_id = $1 AND type = 'public'`,
		project.ID); err != nil {
		t.Fatalf("set sentinel: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/db/todos", nil)
	req.Header.Set("apikey", project.PublicKey)
	mw.ServeHTTP(httptest.NewRecorder(), req)
	time.Sleep(500 * time.Millisecond)
	if k, _ := listKeys(t, list, project.ID); publicOf(k).PublicKey != "eb_pk_sentinel" {
		t.Fatalf("stored public key overwritten: %q", publicOf(k).PublicKey)
	}
}
