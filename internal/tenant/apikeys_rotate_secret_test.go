package tenant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/eurobase/euroback/internal/auth"
)

func rotateSecret(t *testing.T, h http.HandlerFunc, projectID string) string {
	t.Helper()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", projectID)
	req := httptest.NewRequest(http.MethodPost, "/platform/projects/"+projectID+"/api-keys/rotate-secret", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusCreated {
		t.Errorf("rotate secret: status %d: %s", rec.Code, rec.Body.String())
		return ""
	}
	var out APISecretKeyRotatedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Errorf("decode: %v", err)
	}
	return out.SecretKey
}

// TestAPIKeys_RotateSecret: rotating the secret key leaves the public key
// as it is (and working), the old secret key is refused on the next
// request, the new one is accepted, and concurrent rotations leave exactly
// one secret key.
func TestAPIKeys_RotateSecret(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	svc := &TenantService{pool: pool}

	uid := insertTestPlatformUser(t, pool, "rotate@test.eurobase.local")
	project, err := svc.CreateProject(ctx, uid, "rotate@test.eurobase.local", CreateProjectRequest{
		Name: "Rotate Secret", Slug: "test-rotate-secret", Region: "fr-par", Plan: "free",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	t.Cleanup(func() { cleanupProject(t, pool, project.ID) })
	if _, err := pool.Exec(ctx, `UPDATE projects SET status = 'active' WHERE id = $1`, project.ID); err != nil {
		t.Fatalf("activate: %v", err)
	}

	// In CI the handler and the middleware run on the real gateway login,
	// as in production.
	gwPool := pool
	if u := os.Getenv("APIKEY_TEST_GATEWAY_URL"); u != "" {
		gp, err := pgxpool.New(ctx, u)
		if err != nil {
			t.Fatalf("gateway pool: %v", err)
		}
		t.Cleanup(gp.Close)
		gwPool = gp
	}
	mw := auth.NewAPIKeyMiddleware(gwPool).Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	status := func(key string) int {
		req := httptest.NewRequest(http.MethodGet, "/v1/db/todos", nil)
		req.Header.Set("apikey", key)
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		return rec.Code
	}

	rotate := HandleRotateSecretKey(gwPool)
	newSecret := rotateSecret(t, rotate, project.ID)
	if !strings.HasPrefix(newSecret, "eb_sk_") || newSecret == project.SecretKey {
		t.Fatalf("new secret key = %q", newSecret)
	}
	if c := status(project.SecretKey); c != http.StatusUnauthorized {
		t.Fatalf("old secret key: status %d, want 401", c)
	}
	if c := status(newSecret); c != http.StatusNoContent {
		t.Fatalf("new secret key: status %d, want 204", c)
	}
	if c := status(project.PublicKey); c != http.StatusNoContent {
		t.Fatalf("public key after rotation: status %d, want 204", c)
	}
	keys, body := listKeys(t, HandleListAPIKeys(pool), project.ID)
	if got := publicOf(keys).PublicKey; got != project.PublicKey {
		t.Fatalf("public key changed: %q, want %q", got, project.PublicKey)
	}
	if strings.Contains(body, newSecret) {
		t.Fatal("new secret key appears in the key list")
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); rotateSecret(t, rotate, project.ID) }()
	}
	wg.Wait()
	var secrets, publics int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE type = 'secret'), count(*) FILTER (WHERE type = 'public')
		   FROM api_keys WHERE project_id = $1`, project.ID).Scan(&secrets, &publics); err != nil {
		t.Fatalf("count keys: %v", err)
	}
	if secrets != 1 || publics != 1 {
		t.Fatalf("after concurrent rotations: %d secret, %d public keys; want 1 and 1", secrets, publics)
	}
}
