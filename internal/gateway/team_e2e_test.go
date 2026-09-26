package gateway

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/dbprovider"
	"github.com/eurobase/euroback/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestTeamEndToEnd (#683) drives the real gateway router for a Team project
// whose tenant data lives on its own "dedicated" Postgres (a second
// server), and checks every feature reaches that database — and never the
// shared cluster, which has no tenant schema for Team projects.
//
// Run via scripts/test-team.sh. Known routing gaps are listed in
// teamKnownGaps: such a check is reported as SKIP with its issue while it
// still fails, and turns into a FAILURE once it passes ("gap fixed —
// remove it from teamKnownGaps"), so the list can't go stale.
func TestTeamEndToEnd(t *testing.T) {
	sharedAdmin, sharedGW, sharedDev := os.Getenv("TEAM_TEST_SHARED_ADMIN"), os.Getenv("TEAM_TEST_SHARED_GATEWAY"), os.Getenv("TEAM_TEST_SHARED_DEVELOPER")
	dedOwner, dedAdmin := os.Getenv("TEAM_TEST_DED_OWNER"), os.Getenv("TEAM_TEST_DED_ADMIN")
	if sharedAdmin == "" || dedOwner == "" {
		t.Skip("run via scripts/test-team.sh")
	}
	env := setupTeamProject(t, sharedAdmin, sharedGW, sharedDev, dedOwner, dedAdmin)

	sdk := func(method, path, body string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(method, "http://"+env.slug+".eurobase.test"+path, strings.NewReader(body))
		req.Host = env.slug + ".eurobase.test"
		req.Header.Set("apikey", env.secretKey)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	console := func(method, path, body string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(method, "http://api.eurobase.test/platform/projects/"+env.projectID+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+env.platformJWT)
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	check(t, "sdk_rest_insert_and_select", func() error {
		code, body := sdk("POST", "/v1/db/todos", `{"title":"from-sdk"}`)
		if code >= 300 {
			return fmt.Errorf("insert: %d %s", code, body)
		}
		if n := env.dedCount(t, "todos", "title = 'from-sdk'"); n != 1 {
			return fmt.Errorf("row not on the dedicated DB (count %d)", n)
		}
		code, body = sdk("GET", "/v1/db/todos", "")
		if code != 200 || !strings.Contains(body, "from-sdk") {
			return fmt.Errorf("select: %d %s", code, body)
		}
		return nil
	})

	check(t, "sdk_sql_endpoint", func() error {
		code, body := sdk("POST", "/v1/db/sql", `{"sql":"SELECT count(*) AS n FROM todos"}`)
		if code != 200 {
			return fmt.Errorf("%d %s", code, body)
		}
		return nil
	})

	check(t, "sdk_enduser_signup_and_signin", func() error {
		creds := `{"email":"enduser@team.test","password":"Correct-horse-9"}`
		code, body := sdk("POST", "/v1/auth/signup", creds)
		if code >= 300 {
			return fmt.Errorf("signup: %d %s", code, trunc(body))
		}
		if n := env.dedCount(t, "users", "email = 'enduser@team.test'"); n != 1 {
			return fmt.Errorf("user not on the dedicated DB (count %d)", n)
		}
		code, body = sdk("POST", "/v1/auth/signin", creds)
		if code != 200 || !strings.Contains(body, "access_token") {
			return fmt.Errorf("signin: %d %s", code, trunc(body))
		}
		return nil
	})

	check(t, "console_table_editor_insert", func() error { // #678
		code, body := console("POST", "/data/todos", `{"title":"from-console"}`)
		if code >= 300 {
			return fmt.Errorf("insert: %d %s", code, body)
		}
		if n := env.dedCount(t, "todos", "title = 'from-console'"); n != 1 {
			return fmt.Errorf("row not on the dedicated DB (count %d)", n)
		}
		return nil
	})

	check(t, "console_sql_editor", func() error { // #678
		code, body := console("POST", "/data/sql", `{"sql":"INSERT INTO todos (title) VALUES ('from-sql-editor')"}`)
		if code >= 300 {
			return fmt.Errorf("%d %s", code, body)
		}
		if n := env.dedCount(t, "todos", "title = 'from-sql-editor'"); n != 1 {
			return fmt.Errorf("row not on the dedicated DB (count %d)", n)
		}
		return nil
	})

	check(t, "console_schema_introspection", func() error { // #679
		code, body := console("GET", "/schema", "")
		if code != 200 || !strings.Contains(body, "todos") {
			return fmt.Errorf("%d %s", code, trunc(body))
		}
		return nil
	})

	check(t, "sdk_ddl_create_table", func() error { // #679
		code, body := sdk("POST", "/v1/db/schema/tables", `{"name":"sdk_made","columns":[{"name":"id","type":"uuid","primary_key":true,"default":"gen_random_uuid()"},{"name":"note","type":"text"}]}`)
		if code >= 300 {
			return fmt.Errorf("%d %s", code, trunc(body))
		}
		if !env.dedTableExists(t, "sdk_made") {
			return fmt.Errorf("table not created on the dedicated DB")
		}
		return nil
	})

	// Nothing for this project may have landed on the shared cluster.
	check(t, "shared_cluster_untouched", func() error {
		var n int
		if err := env.shared.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_namespace WHERE nspname = $1`, env.schema).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return fmt.Errorf("tenant schema %s exists on the shared cluster", env.schema)
		}
		return nil
	})
}

// teamKnownGaps: checks expected to fail until their issue is fixed.
var teamKnownGaps = map[string]string{
	"console_table_editor_insert":  "#678",
	"console_sql_editor":           "#678",
	"console_schema_introspection": "#679",
	"sdk_ddl_create_table":         "#679",
}

func check(t *testing.T, name string, fn func() error) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		err := fn()
		issue, known := teamKnownGaps[name]
		if err != nil && isAuthFailure(err) {
			// A harness auth problem would otherwise hide behind a known gap.
			t.Fatalf("request never reached the handler: %v", err)
		}
		switch {
		case err != nil && known:
			t.Skipf("known gap %s: %v", issue, err)
		case err != nil:
			t.Fatal(err)
		case known:
			t.Fatalf("gap %s appears fixed — remove %q from teamKnownGaps", issue, name)
		}
	})
}

func isAuthFailure(err error) bool {
	m := err.Error()
	for _, sig := range []string{"401 ", "insufficient role", `"project not found"`, "sso_required"} {
		if strings.Contains(m, sig) {
			return true
		}
	}
	return false
}

func trunc(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

type teamEnv struct {
	// router runs the production middleware chain (no devMode, which
	// swaps the SDK chain and skips console membership/role checks):
	// SDK requests carry the secret API key, console requests a real
	// platform JWT for the project owner.
	router      http.Handler
	platformJWT string
	projectID   string
	slug        string
	schema      string
	secretKey   string
	shared      *pgxpool.Pool
	ded         *pgxpool.Pool // admin view of the dedicated DB, for assertions
}

func (e *teamEnv) dedCount(t *testing.T, table, where string) int {
	t.Helper()
	var n int
	q := fmt.Sprintf("SELECT count(*) FROM %s WHERE %s", pgx.Identifier{e.schema, table}.Sanitize(), where)
	if err := e.ded.QueryRow(context.Background(), q).Scan(&n); err != nil {
		t.Logf("dedCount: %v", err)
		return -1
	}
	return n
}

func (e *teamEnv) dedTableExists(t *testing.T, table string) bool {
	t.Helper()
	var ok bool
	_ = e.ded.QueryRow(context.Background(), `SELECT to_regclass($1) IS NOT NULL`, e.schema+"."+table).Scan(&ok)
	return ok
}

func randHexT(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func setupTeamProject(t *testing.T, sharedAdmin, sharedGW, sharedDev, dedOwner, dedAdmin string) *teamEnv {
	t.Helper()
	ctx := context.Background()
	mustPool := func(u string) *pgxpool.Pool {
		p, err := pgxpool.New(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	admin, gw, dev, ded := mustPool(sharedAdmin), mustPool(sharedGW), mustPool(sharedDev), mustPool(dedAdmin)

	devUser := "7e4a0000-0000-4000-9000-" + randHexT(t, 6)
	projectID := "7e4a0000-0000-4000-8000-" + randHexT(t, 6)
	slug := "team-" + randHexT(t, 4)
	mustExecT(t, admin, `INSERT INTO platform_users (id, email) VALUES ($1, $2)`, devUser, "owner-"+devUser+"@team.test")
	mustExecT(t, admin, `INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		VALUES ($1, $2, 'team e2e', $3, $4, $5, 'fr-par', 'team', 'active')`,
		projectID, devUser, slug, "tenant_"+strings.ReplaceAll(projectID, "-", "_"), "b-"+slug)
	mustExecT(t, admin, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'owner')`, projectID, devUser)

	// API keys (the SDK authenticates with the secret key).
	pub, sec, pubHash, secHash, err := tenant.GenerateAPIKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	_ = pub
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := tenant.StoreAPIKeys(ctx, tx, projectID, pubHash, pub[:14], secHash, sec[:14]); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// The dedicated instance, bootstrapped exactly as the provision worker does.
	runtimePW, readonlyPW := randHexT(t, 16), randHexT(t, 16)
	creds, schema, err := dbprovider.BootstrapDedicated(ctx, dedOwner, projectID, "team e2e", runtimePW, readonlyPW, nil)
	if err != nil {
		t.Fatalf("BootstrapDedicated: %v", err)
	}

	// project_databases row with sealed credentials (owner, runtime, readonly).
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keyB64 := base64.StdEncoding.EncodeToString(key)
	cipher, err := dbprovider.NewCipher(keyB64, 1)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(dedOwner)
	ownerPW, _ := u.User.Password()
	port, _ := strconv.Atoi(u.Port())
	seal := func(s string) ([]byte, []byte, int16) {
		ct, nonce, ver, err := cipher.Seal(s)
		if err != nil {
			t.Fatal(err)
		}
		return ct, nonce, ver
	}
	repo := dbprovider.NewRepo(admin)
	ct, nonce, ver := seal(ownerPW)
	rec, err := repo.InsertProvisioning(ctx, projectID, &dbprovider.Instance{
		ProviderID: "e2e-" + slug, Host: u.Hostname(), Port: port, DBName: strings.TrimPrefix(u.Path, "/"),
		Username: u.User.Username(), State: dbprovider.StateActive, Region: "fr-par",
	}, "scaleway", ct, nonce, ver)
	if err != nil {
		t.Fatalf("InsertProvisioning: %v", err)
	}
	ct, nonce, ver = seal(creds.Runtime.Password)
	if _, err := repo.SetRuntimeCredentials(ctx, rec.ID, creds.Runtime.Username, ct, nonce, ver); err != nil {
		t.Fatalf("SetRuntimeCredentials: %v", err)
	}
	ct, nonce, ver = seal(creds.Readonly.Password)
	if _, err := repo.SetReadonlyCredentials(ctx, rec.ID, creds.Readonly.Username, ct, nonce, ver); err != nil {
		t.Fatalf("SetReadonlyCredentials: %v", err)
	}
	mustExecT(t, admin, `UPDATE projects SET schema_name = $2 WHERE id = $1`, projectID, schema)

	// The real router with Team routing on and platform auth wired as in
	// cmd/gateway.
	t.Setenv("VAULT_ENCRYPTION_KEY", keyB64)
	t.Setenv("TEAM_TIER_ROUTING", "1")
	platformSvc := auth.NewPlatformAuthService(gw, randHexT(t, 32))
	jwt, _, err := platformSvc.IssuePlatformJWT(devUser, "owner-"+devUser+"@team.test", false)
	if err != nil {
		t.Fatal(err)
	}
	subdomain := auth.NewSubdomainMiddleware(gw, "eurobase.test")
	router := NewRouter(gw, dev, nil, auth.NewPlatformAuthMiddleware(platformSvc), platformSvc, nil, nil, nil, nil, nil,
		subdomain, nil, nil, nil, nil, "", nil, "", nil, nil, nil, nil, SSOWiring{}, nil, nil)

	return &teamEnv{router: router, platformJWT: jwt, projectID: projectID, slug: slug, schema: schema, secretKey: sec, shared: admin, ded: ded}
}

func mustExecT(t *testing.T, p *pgxpool.Pool, q string, args ...any) {
	t.Helper()
	if _, err := p.Exec(context.Background(), q, args...); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
}
