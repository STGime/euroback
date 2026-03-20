package gateway

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/eurobase/euroback/internal/tenant"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var testRSAKey *rsa.PrivateKey

func init() {
	var err error
	testRSAKey, err = rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(fmt.Sprintf("failed to generate test RSA key: %v", err))
	}
}

// generateTestJWT creates a valid JWT signed with the test RSA key.
func generateTestJWT(sub, email string) string {
	claims := jwt.MapClaims{
		"sub":   sub,
		"email": email,
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(1 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tokenStr, err := token.SignedString(testRSAKey)
	if err != nil {
		panic(fmt.Sprintf("failed to sign test JWT: %v", err))
	}
	return tokenStr
}

// testAuthMiddleware is a middleware that validates JWTs signed with the test RSA key.
// This replaces the Hanko JWKS-based middleware in tests.
func testAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, `{"error":"missing authorization header"}`, http.StatusUnauthorized)
			return
		}

		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
		if tokenStr == authHeader {
			http.Error(w, `{"error":"malformed authorization header"}`, http.StatusUnauthorized)
			return
		}

		token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return &testRSAKey.PublicKey, nil
		})
		if err != nil || !token.Valid {
			http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			http.Error(w, `{"error":"invalid token claims"}`, http.StatusUnauthorized)
			return
		}

		sub, _ := claims.GetSubject()
		email, _ := claims["email"].(string)

		if sub == "" {
			http.Error(w, `{"error":"token missing subject"}`, http.StatusUnauthorized)
			return
		}

		ctx := auth.ContextWithClaims(r.Context(), &auth.Claims{
			Subject: sub,
			Email:   email,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// setupTestDB connects to the local test database and returns the pool.
func setupTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://eurobase_api:localdev@localhost:5433/eurobase?sslmode=disable"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Skipf("cannot connect to test database: %v", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("cannot ping test database: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
	})

	return pool
}

// setupTestServer builds a chi router with a test auth middleware (bypassing Hanko JWKS)
// and returns an httptest.Server.
func setupTestServer(t *testing.T, pool *pgxpool.Pool) *httptest.Server {
	t.Helper()

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)

	// Health check.
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	tenantSvc := tenant.NewTenantService(pool)

	// V1 routes with test auth.
	r.Route("/v1", func(r chi.Router) {
		r.Use(testAuthMiddleware)
		r.Post("/tenants", tenant.HandleCreateProject(pool, tenantSvc))
		r.Get("/tenants", tenant.HandleListProjects(pool, tenantSvc))
	})

	ts := httptest.NewServer(r)
	t.Cleanup(func() {
		ts.Close()
	})

	return ts
}

func TestHealthEndpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}

	pool := setupTestDB(t)
	ts := setupTestServer(t, pool)

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health returned error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("expected status 'ok', got %q", body["status"])
	}
}

func TestCreateTenantUnauthorized(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}

	pool := setupTestDB(t)
	ts := setupTestServer(t, pool)

	reqBody := strings.NewReader(`{"name":"Unauthorized Project"}`)
	resp, err := http.Post(ts.URL+"/v1/tenants", "application/json", reqBody)
	if err != nil {
		t.Fatalf("POST /v1/tenants returned error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", resp.StatusCode)
	}
}

func TestCreateTenantFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}

	pool := setupTestDB(t)
	ts := setupTestServer(t, pool)

	hankoUserID := "test-http-flow-user"
	email := "httpflow@eurobase.app"
	tokenStr := generateTestJWT(hankoUserID, email)

	t.Cleanup(func() {
		ctx := context.Background()
		// Clean up projects created by this test.
		rows, err := pool.Query(ctx,
			`SELECT p.id FROM projects p
			 JOIN platform_users u ON p.owner_id = u.id
			 WHERE u.hanko_user_id = $1`,
			hankoUserID,
		)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var projectID string
				if err := rows.Scan(&projectID); err == nil {
					_, _ = pool.Exec(ctx, `SELECT deprovision_tenant($1)`, projectID)
					_, _ = pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, projectID)
				}
			}
		}
		_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE hanko_user_id = $1`, hankoUserID)
	})

	// POST /v1/tenants - create a project.
	reqBody := strings.NewReader(`{"name":"HTTP Flow Test","slug":"test-http-flow","region":"fr-par","plan":"free"}`)
	req, _ := http.NewRequest("POST", ts.URL+"/v1/tenants", reqBody)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tokenStr)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/tenants returned error: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected status 201, got %d; body: %s", resp.StatusCode, string(respBody))
	}

	var createResp map[string]interface{}
	if err := json.Unmarshal(respBody, &createResp); err != nil {
		t.Fatalf("failed to decode create response: %v", err)
	}

	if createResp["id"] == nil || createResp["id"] == "" {
		t.Error("expected non-empty id in response")
	}
	if createResp["slug"] != "test-http-flow" {
		t.Errorf("expected slug 'test-http-flow', got %v", createResp["slug"])
	}

	// GET /v1/tenants - list projects.
	req, _ = http.NewRequest("GET", ts.URL+"/v1/tenants", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)

	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/tenants returned error: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp2.StatusCode)
	}

	var listResp []map[string]interface{}
	if err := json.NewDecoder(resp2.Body).Decode(&listResp); err != nil {
		t.Fatalf("failed to decode list response: %v", err)
	}

	if len(listResp) == 0 {
		t.Fatal("expected at least 1 project in list, got 0")
	}

	found := false
	for _, p := range listResp {
		if p["slug"] == "test-http-flow" {
			found = true
			break
		}
	}
	if !found {
		t.Error("created project not found in list response")
	}
}
