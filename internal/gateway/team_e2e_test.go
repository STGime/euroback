package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/eurobase/euroback/internal/vault"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestTeamEndToEnd (#683) drives the real gateway router for a Team project
// whose tenant data lives on its own "dedicated" Postgres, and checks every
// feature reaches that database — and never the shared cluster.
//
// Two scenarios, each on its own dedicated database:
//   - fresh:    a project created as Team — the shared cluster has no
//     tenant schema, so a misrouted request errors;
//   - upgraded: a Pro project upgraded to Team — the shared cluster still
//     holds a stale copy of the schema with a decoy row, so a misrouted
//     request *succeeds* against the wrong database (the realistic prod
//     failure). Assertions read the dedicated DB directly and the decoy
//     schema must stay exactly as it was.
//
// Run via scripts/test-team.sh. Known routing gaps are listed in
// teamKnownGaps with the error they fail with today: such a check SKIPs
// only while it fails *that way*; any other failure is a FAIL, and a pass
// is a FAIL too ("gap fixed — remove it from teamKnownGaps"), so the list
// can't go stale or mask a new bug. Requests that never reach the handler
// (401/403, membership 404, maintenance 503) always FAIL.
func TestTeamEndToEnd(t *testing.T) {
	cfg := teamTestConfig{
		sharedAdmin: os.Getenv("TEAM_TEST_SHARED_ADMIN"),
		sharedGW:    os.Getenv("TEAM_TEST_SHARED_GATEWAY"),
		sharedDev:   os.Getenv("TEAM_TEST_SHARED_DEVELOPER"),
		dedOwner:    os.Getenv("TEAM_TEST_DED_OWNER"),
		dedAdmin:    os.Getenv("TEAM_TEST_DED_ADMIN"),
	}
	if cfg.sharedAdmin == "" || cfg.dedOwner == "" {
		t.Skip("run via scripts/test-team.sh")
	}
	// One instance-wide secret, as in prod: the runtime roles are
	// cluster-wide, so both scenarios' bootstraps must set the same password.
	cfg.runtimePW, cfg.readonlyPW = randHexT(t, 16), randHexT(t, 16)

	for _, sc := range []struct {
		name     string
		db       string
		upgraded bool
	}{
		{"fresh", "eb_fresh", false},
		{"upgraded", "eb_upgraded", true},
	} {
		t.Run(sc.name, func(t *testing.T) {
			runTeamChecks(t, setupTeamProject(t, cfg, sc.name, sc.db, sc.upgraded))
		})
	}
}

func runTeamChecks(t *testing.T, env *teamEnv) {
	sdk := func(method, path, body string) *httpResult {
		t.Helper()
		req := httptest.NewRequest(method, "http://"+env.slug+".eurobase.test"+path, strings.NewReader(body))
		req.Header.Set("apikey", env.secretKey)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		return &httpResult{req: method + " " + path, code: rec.Code, body: rec.Body.String()}
	}
	console := func(method, path, body string) *httpResult {
		t.Helper()
		req := httptest.NewRequest(method, "http://api.eurobase.test/platform/projects/"+env.projectID+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+env.platformJWT)
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		return &httpResult{req: "console " + method + " " + path, code: rec.Code, body: rec.Body.String()}
	}

	check(t, env, "sdk_rest_insert_and_select", func() error {
		if r := sdk("POST", "/v1/db/todos", `{"title":"from-sdk"}`); r.code >= 300 {
			return r
		}
		if n := env.dedCount(t, "todos", "title = 'from-sdk'"); n != 1 {
			return fmt.Errorf("row not on the dedicated DB (count %d)", n)
		}
		r := sdk("GET", "/v1/db/todos", "")
		if r.code != 200 || !strings.Contains(r.body, "from-sdk") || strings.Contains(r.body, decoyTitle) {
			return fmt.Errorf("select read the wrong rows: %w", r)
		}
		return nil
	})

	// Proves the SDK runs on the dedicated instance as the non-owner
	// runtime role — an owner connection would bypass RLS.
	check(t, env, "sdk_sql_runtime_role_on_dedicated", func() error {
		r := sdk("POST", "/v1/db/sql", `{"sql":"SELECT current_user AS who, current_database() AS db, (SELECT count(*) FROM todos WHERE title = 'from-sdk') AS n"}`)
		if r.code != 200 {
			return r
		}
		for _, want := range []string{`"who":"eurobase_gateway"`, `"db":"` + env.dedDB + `"`, `"n":1`} {
			if !strings.Contains(r.body, want) {
				return fmt.Errorf("missing %s: %w", want, r)
			}
		}
		return nil
	})

	check(t, env, "sdk_enduser_signup_and_signin", func() error {
		creds := `{"email":"enduser@team.test","password":"Correct-horse-9"}`
		if r := sdk("POST", "/v1/auth/signup", creds); r.code >= 300 {
			return r
		}
		if n := env.dedCount(t, "users", "email = 'enduser@team.test'"); n != 1 {
			return fmt.Errorf("user not on the dedicated DB (count %d)", n)
		}
		if r := sdk("POST", "/v1/auth/signin", creds); r.code != 200 || !strings.Contains(r.body, "access_token") {
			return r
		}
		return nil
	})

	check(t, env, "console_table_editor_insert", func() error {
		if r := console("POST", "/data/todos", `{"title":"from-console"}`); r.code >= 300 {
			return r
		}
		if n := env.dedCount(t, "todos", "title = 'from-console'"); n != 1 {
			return fmt.Errorf("row not on the dedicated DB (count %d)", n)
		}
		return nil
	})

	check(t, env, "console_sql_editor", func() error {
		if r := console("POST", "/data/sql", `{"sql":"INSERT INTO todos (title) VALUES ('from-sql-editor')"}`); r.code >= 300 {
			return r
		}
		if n := env.dedCount(t, "todos", "title = 'from-sql-editor'"); n != 1 {
			return fmt.Errorf("row not on the dedicated DB (count %d)", n)
		}
		return nil
	})

	check(t, env, "console_table_editor_read_update", func() error {
		r := console("GET", "/data/todos", "")
		if r.code != 200 || !strings.Contains(r.body, "from-console") || strings.Contains(r.body, decoyTitle) {
			return fmt.Errorf("select read the wrong rows: %w", r)
		}
		id := env.dedScalar(t, "SELECT id::text FROM %s WHERE title = 'from-console'", "todos")
		if r := console("PATCH", "/data/todos/"+id, `{"title":"console-updated"}`); r.code >= 300 {
			return r
		}
		if n := env.dedCount(t, "todos", "title = 'console-updated'"); n != 1 {
			return fmt.Errorf("update not on the dedicated DB (count %d)", n)
		}
		return nil
	})

	// MCP runSQLTransaction uses this route too.
	check(t, env, "console_sql_transaction", func() error {
		if r := console("POST", "/data/sql/transaction", `{"statements":["INSERT INTO todos (title) VALUES ('from-tx-1')","INSERT INTO todos (title) VALUES ('from-tx-2')"]}`); r.code >= 300 {
			return r
		}
		if n := env.dedCount(t, "todos", "title LIKE 'from-tx-%'"); n != 2 {
			return fmt.Errorf("rows not on the dedicated DB (count %d)", n)
		}
		return nil
	})

	// The console runs as the dedicated instance's owner (console traffic
	// is service-role by design); the point is that it is *that* instance.
	check(t, env, "console_sql_on_dedicated", func() error {
		r := console("POST", "/data/sql", `{"sql":"SELECT current_database() AS db"}`)
		if r.code != 200 || !strings.Contains(r.body, `"db":"`+env.dedDB+`"`) {
			return fmt.Errorf("not on the dedicated DB: %w", r)
		}
		return nil
	})

	// A Team project whose dedicated pool can't be opened is refused —
	// never served from the shared cluster.
	check(t, env, "console_fails_closed_without_dedicated_pool", func() error {
		reached := false
		mw := tenant.PlatformTenantContext(env.gw, env.dev, func(context.Context, string) *pgxpool.Pool { return nil })
		h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
		cr := chi.NewRouter()
		cr.Handle("/p/{id}/data", h)
		req := httptest.NewRequest("GET", "/p/"+env.projectID+"/data", nil)
		req = req.WithContext(auth.ContextWithClaims(req.Context(), &auth.Claims{Subject: env.ownerUser, Email: env.ownerEmail}))
		rec := httptest.NewRecorder()
		cr.ServeHTTP(rec, req)
		if reached || rec.Code != http.StatusServiceUnavailable {
			return fmt.Errorf("handler reached=%v, status %d %s (want 503, not reached)", reached, rec.Code, rec.Body.String())
		}
		return nil
	})

	check(t, env, "console_vault_set", func() error {
		if r := console("POST", "/vault", `{"name":"E2E_CONSOLE_SECRET","value":"v1"}`); r.code >= 300 {
			return r
		}
		if n := env.dedCount(t, "vault_secrets", "name = 'E2E_CONSOLE_SECRET'"); n != 1 {
			return fmt.Errorf("secret not in the dedicated DB's vault (count %d)", n)
		}
		return nil
	})

	check(t, env, "sdk_vault_set", func() error {
		if r := sdk("POST", "/v1/vault", `{"name":"E2E_SDK_SECRET","value":"v1"}`); r.code >= 300 {
			return r
		}
		if n := env.dedCount(t, "vault_secrets", "name = 'E2E_SDK_SECRET'"); n != 1 {
			return fmt.Errorf("secret not in the dedicated DB's vault (count %d)", n)
		}
		return nil
	})

	// Through the real router: a Team project whose dedicated database is
	// still provisioning (no host yet — its pool can't be opened) gets
	// 503 on tenant-data routes, never the shared cluster, while its
	// platform-table settings stay editable.
	check(t, env, "console_fails_closed_while_provisioning", func() error {
		stuck := env.addProvisioningTeamProject(t)
		for _, path := range []string{"/data/todos", "/users"} {
			r := env.consoleFor(stuck, "GET", path, "")
			if r.code != http.StatusServiceUnavailable {
				return fmt.Errorf("want 503: %w", r)
			}
		}
		body, err := json.Marshal(map[string]any{"auth_config": tenant.DefaultAuthConfig()})
		if err != nil {
			return err
		}
		req := httptest.NewRequest("PATCH", "http://api.eurobase.test/v1/tenants/"+stuck, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+env.platformJWT)
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		if rec.Code >= 300 {
			return fmt.Errorf("settings locked out: PATCH /v1/tenants/{id}: %d %s", rec.Code, rec.Body.String())
		}
		// Saving an OAuth client secret needs the dedicated vault: refused,
		// not written to the shared one.
		cfg := tenant.DefaultAuthConfig()
		cfg.OAuthProviders = map[string]tenant.OAuthProviderConfig{
			"github": {Enabled: true, ClientID: "e2e-client", ClientSecret: "e2e-secret"},
		}
		body, err = json.Marshal(map[string]any{"auth_config": cfg})
		if err != nil {
			return err
		}
		req = httptest.NewRequest("PATCH", "http://api.eurobase.test/v1/tenants/"+stuck, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+env.platformJWT)
		rec = httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			return fmt.Errorf("OAuth secret save while provisioning: want 503, got %d %s", rec.Code, rec.Body.String())
		}
		return nil
	})

	// ded_only exists only on the dedicated DB, so a listing read from the
	// stale shared copy can't pass.
	check(t, env, "console_schema_introspection", func() error {
		r := console("GET", "/schema", "")
		if r.code != 200 {
			return r
		}
		if !strings.Contains(r.body, "ded_only") {
			return fmt.Errorf("schema listing lacks the dedicated-only table: %w", r)
		}
		return nil
	})

	// Console Table Editor schema editing (/schema/tables): the DDL itself
	// is routed, but its prechecks and listings read the shared cluster
	// (#679). ded_only exists only on the dedicated DB.
	check(t, env, "console_ddl_create_table", func() error {
		if r := console("POST", "/schema/tables", `{"name":"console_made","columns":[{"name":"id","type":"uuid","primary_key":true,"default":"gen_random_uuid()"},{"name":"note","type":"text"}]}`); r.code >= 300 {
			return r
		}
		if !env.dedTableExists(t, "console_made") {
			return errors.New("table not created on the dedicated DB")
		}
		return nil
	})

	check(t, env, "console_ddl_add_column", func() error {
		if r := console("POST", "/schema/tables/ded_only/columns", `{"name":"note","type":"text","nullable":true}`); r.code >= 300 {
			return r
		}
		if env.dedScalar(t, "SELECT count(*)::text FROM pg_attribute WHERE attrelid = '%s'::regclass AND attname = 'note'", "ded_only") != "1" {
			return errors.New("column not added on the dedicated DB")
		}
		return nil
	})

	check(t, env, "console_ddl_list_indexes", func() error {
		r := console("GET", "/schema/tables/ded_only/indexes", "")
		if r.code != 200 {
			return r
		}
		if !strings.Contains(r.body, "ded_only_marker_idx") {
			return fmt.Errorf("index listing lacks the dedicated-only index: %w", r)
		}
		return nil
	})

	check(t, env, "console_ddl_alter_and_drop_column", func() error {
		if r := console("PATCH", "/schema/tables/ded_only/columns/note", `{"new_name":"note2"}`); r.code >= 300 {
			return r
		}
		if r := console("DELETE", "/schema/tables/ded_only/columns/note2", ""); r.code >= 300 {
			return r
		}
		if env.dedScalar(t, "SELECT count(*)::text FROM pg_attribute WHERE attrelid = '%s'::regclass AND attname IN ('note', 'note2') AND NOT attisdropped", "ded_only") != "0" {
			return errors.New("column not renamed/dropped on the dedicated DB")
		}
		return nil
	})

	check(t, env, "console_ddl_list_functions_and_policies", func() error {
		r := console("GET", "/schema/functions", "")
		if r.code != 200 || !strings.Contains(r.body, "ded_only_fn") {
			return fmt.Errorf("function listing lacks the dedicated-only function: %w", r)
		}
		if strings.Contains(r.body, "auth_uid") {
			return fmt.Errorf("function listing shows platform helpers: %w", r)
		}
		r = console("GET", "/schema/tables/ded_only/policies", "")
		if r.code != 200 || !strings.Contains(r.body, "ded_only_policy") {
			return fmt.Errorf("policy listing lacks the dedicated-only policy: %w", r)
		}
		return nil
	})

	check(t, env, "console_rls_audit", func() error {
		r := console("GET", "/schema/rls-audit", "")
		if r.code != 200 || !strings.Contains(r.body, "ded_only") {
			return fmt.Errorf("RLS audit lacks the dedicated-only table: %w", r)
		}
		return nil
	})

	check(t, env, "console_schema_changes_backfill", func() error {
		r := console("GET", "/schema/changes", "")
		if r.code != 200 || !strings.Contains(r.body, "ded_only") {
			return fmt.Errorf("schema history lacks the dedicated-only table: %w", r)
		}
		return nil
	})

	check(t, env, "sdk_ddl_create_table", func() error {
		if r := sdk("POST", "/v1/db/schema/tables", `{"name":"sdk_made","columns":[{"name":"id","type":"uuid","primary_key":true,"default":"gen_random_uuid()"},{"name":"note","type":"text"}]}`); r.code >= 300 {
			return r
		}
		if !env.dedTableExists(t, "sdk_made") {
			return errors.New("table not created on the dedicated DB")
		}
		return nil
	})

	// Runs last: nothing above may have touched the shared cluster.
	check(t, env, "shared_cluster_untouched", func() error {
		ctx := context.Background()
		var exists bool
		if err := env.shared.QueryRow(ctx, `SELECT to_regnamespace($1) IS NOT NULL`, env.schema).Scan(&exists); err != nil {
			return err
		}
		if !env.upgraded {
			if exists {
				return fmt.Errorf("tenant schema %s was created on the shared cluster", env.schema)
			}
			return nil
		}
		var fp string
		q := fmt.Sprintf(sharedFingerprintSQL, pgx.Identifier{env.schema, "todos"}.Sanitize(), pgx.Identifier{env.schema, "vault_secrets"}.Sanitize())
		if err := env.shared.QueryRow(ctx, q, env.schema).Scan(&fp); err != nil {
			return err
		}
		if fp != env.sharedFingerprint {
			return fmt.Errorf("stale shared copy changed:\nbefore %s\nafter  %s", env.sharedFingerprint, fp)
		}
		return nil
	})
}

// knownGap: a check expected to fail until issue is fixed, and the error
// text it fails with today (any of sigs). Keyed by check name, or by
// "<scenario>/<name>" for a gap in one scenario only. The sigs cover both
// scenarios: fresh fails on the missing shared schema, upgraded lands on
// the stale shared copy.
type knownGap struct {
	issue string
	sigs  []string
}

var teamKnownGaps = map[string]knownGap{}

func check(t *testing.T, env *teamEnv, name string, fn func() error) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		err := fn()
		var hr *httpResult
		if errors.As(err, &hr) && hr.neverReachedHandler() {
			// A harness auth problem must not hide behind a known gap.
			t.Fatalf("request never reached the handler: %v", err)
		}
		gap, known := teamKnownGaps[name]
		if !known {
			gap, known = teamKnownGaps[env.scenario+"/"+name]
		}
		switch {
		case err == nil && known:
			t.Fatalf("gap %s appears fixed — remove %q from teamKnownGaps", gap.issue, name)
		case err == nil:
		case known && gap.matches(err):
			t.Skipf("known gap %s: %v", gap.issue, err)
		case known:
			t.Fatalf("known gap %s failed differently than expected: %v", gap.issue, err)
		default:
			t.Fatal(err)
		}
	})
}

func (g knownGap) matches(err error) bool {
	for _, s := range g.sigs {
		if strings.Contains(err.Error(), s) {
			return true
		}
	}
	return false
}

// httpResult is a response that failed a check; it carries the status so
// check can tell "reached the handler and misrouted" from "never got in".
type httpResult struct {
	req  string
	code int
	body string
}

func (r *httpResult) Error() string {
	b := r.body
	if len(b) > 300 {
		b = b[:300] + "…"
	}
	return fmt.Sprintf("%s: %d %s", r.req, r.code, strings.TrimSpace(b))
}

func (r *httpResult) neverReachedHandler() bool {
	switch r.code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable:
		return true
	case http.StatusNotFound:
		return strings.Contains(r.body, "project not found")
	}
	return false
}

const decoyTitle = "decoy-on-shared"

// sharedFingerprintSQL summarises the stale shared copy: its tables and
// the todos rows and the vault secret names (%[1]s / %[2]s = the quoted
// todos / vault_secrets tables).
const sharedFingerprintSQL = `
SELECT (SELECT string_agg(relname, ',' ORDER BY relname) FROM pg_class
         WHERE relnamespace = to_regnamespace($1) AND relkind = 'r')
    || ' | ' ||
       (SELECT coalesce(string_agg(title, ',' ORDER BY title), '') FROM ` + "%[1]s" + `)
    || ' | ' ||
       (SELECT coalesce(string_agg(name, ',' ORDER BY name), '') FROM ` + "%[2]s" + `)`

type teamTestConfig struct {
	sharedAdmin, sharedGW, sharedDev string
	dedOwner, dedAdmin               string // URLs; the database is replaced per scenario
	runtimePW, readonlyPW            string
}

type teamEnv struct {
	// router runs the production middleware chain (no devMode, which
	// swaps the SDK chain and skips console membership/role checks):
	// SDK requests carry the secret API key, console requests a real
	// platform JWT for the project owner.
	router            http.Handler
	platformJWT       string
	projectID         string
	slug              string
	schema            string
	dedDB             string
	secretKey         string
	scenario          string
	upgraded          bool
	sharedFingerprint string
	ownerUser         string
	ownerEmail        string
	gw, dev           *pgxpool.Pool // shared-cluster runtime + developer pools
	shared            *pgxpool.Pool
	ded               *pgxpool.Pool // admin view of the dedicated DB, for assertions
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

func (e *teamEnv) dedScalar(t *testing.T, format, table string) string {
	t.Helper()
	var v string
	q := fmt.Sprintf(format, pgx.Identifier{e.schema, table}.Sanitize())
	if err := e.ded.QueryRow(context.Background(), q).Scan(&v); err != nil {
		t.Fatalf("dedScalar: %v", err)
	}
	return v
}

// consoleFor issues a console request as the owner for another project.
func (e *teamEnv) consoleFor(projectID, method, path, body string) *httpResult {
	req := httptest.NewRequest(method, "http://api.eurobase.test/platform/projects/"+projectID+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.platformJWT)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return &httpResult{req: "console " + method + " " + path, code: rec.Code, body: rec.Body.String()}
}

// addProvisioningTeamProject creates a Team project owned by the same user
// whose project_databases row is still provisioning (no host), as between
// project creation and the provision worker's MarkActive.
func (e *teamEnv) addProvisioningTeamProject(t *testing.T) string {
	t.Helper()
	id := "7e4a0000-0000-4000-a000-" + randHexT(t, 6)
	slug := "stuck-" + randHexT(t, 4)
	mustExecT(t, e.shared, `INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		VALUES ($1, $2, 'stuck', $3, $4, $5, 'fr-par', 'team', 'active')`,
		id, e.ownerUser, slug, "tenant_"+strings.ReplaceAll(id, "-", "_"), "b-"+slug)
	mustExecT(t, e.shared, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'owner') ON CONFLICT DO NOTHING`, id, e.ownerUser)
	if _, err := dbprovider.NewRepo(e.shared).InsertProvisioning(context.Background(), id, &dbprovider.Instance{
		ProviderID: "e2e-" + slug, DBName: "rdb", Username: "eurobase_owner", State: dbprovider.StateProvisioning, Region: "fr-par",
	}, "scaleway", []byte("x"), []byte("x"), 1); err != nil {
		t.Fatalf("InsertProvisioning: %v", err)
	}
	return id
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

func withDB(t *testing.T, raw, db string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + db
	return u
}

func setupTeamProject(t *testing.T, cfg teamTestConfig, scenario, dedDB string, upgraded bool) *teamEnv {
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
	admin, gw, dev := mustPool(cfg.sharedAdmin), mustPool(cfg.sharedGW), mustPool(cfg.sharedDev)
	dedAdmin := mustPool(withDB(t, cfg.dedAdmin, dedDB).String())

	ownerUser := "7e4a0000-0000-4000-9000-" + randHexT(t, 6)
	ownerEmail := "owner-" + ownerUser + "@team.test"
	projectID := "7e4a0000-0000-4000-8000-" + randHexT(t, 6)
	slug := "team-" + randHexT(t, 4)
	schema := "tenant_" + strings.ReplaceAll(projectID, "-", "_")
	mustExecT(t, admin, `INSERT INTO platform_users (id, email) VALUES ($1, $2)`, ownerUser, ownerEmail)

	var sharedFP string
	if upgraded {
		// As a Pro project: provision_tenant builds the shared schema (the
		// copy an upgrade leaves behind), then a decoy row goes in.
		mustExecT(t, admin, `INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
			VALUES ($1, $2, 'team e2e', $3, $4, $5, 'fr-par', 'pro', 'active')`, projectID, ownerUser, slug, schema, "b-"+slug)
		mustExecT(t, admin, `SELECT provision_tenant($1, 'team e2e', 'pro')`, projectID)
		mustExecT(t, admin, fmt.Sprintf(`INSERT INTO %s (title) VALUES ($1)`, pgx.Identifier{schema, "todos"}.Sanitize()), decoyTitle)
		mustExecT(t, admin, `UPDATE projects SET plan = 'team' WHERE id = $1`, projectID)
		q := fmt.Sprintf(sharedFingerprintSQL, pgx.Identifier{schema, "todos"}.Sanitize(), pgx.Identifier{schema, "vault_secrets"}.Sanitize())
		if err := admin.QueryRow(ctx, q, schema).Scan(&sharedFP); err != nil {
			t.Fatal(err)
		}
	} else {
		mustExecT(t, admin, `INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
			VALUES ($1, $2, 'team e2e', $3, $4, $5, 'fr-par', 'team', 'active')`, projectID, ownerUser, slug, schema, "b-"+slug)
	}
	mustExecT(t, admin, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'owner')
		ON CONFLICT DO NOTHING`, projectID, ownerUser)

	// API keys (the SDK authenticates with the secret key).
	pub, sec, pubHash, secHash, err := tenant.GenerateAPIKeyPair()
	if err != nil {
		t.Fatal(err)
	}
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

	// The dedicated instance, bootstrapped as ProvisionTeamDatabaseWorker
	// does: owner DSN via BuildOwnerDSN (sslmode=require), display name =
	// project ID, then the provider's SetPrivilege (stand-in: CONNECT
	// granted by the instance superuser — the database is not the owner's
	// and PUBLIC has no CONNECT, as on Scaleway), then the readonly lockdown.
	ou := withDB(t, cfg.dedOwner, dedDB)
	ownerPW, _ := ou.User.Password()
	port, _ := strconv.Atoi(ou.Port())
	ownerDSN := dbprovider.BuildOwnerDSN(ou.User.Username(), ownerPW, ou.Hostname(), port, dedDB)
	creds, gotSchema, err := dbprovider.BootstrapDedicated(ctx, ownerDSN, projectID, projectID, cfg.runtimePW, cfg.readonlyPW, nil)
	if err != nil {
		t.Fatalf("BootstrapDedicated: %v", err)
	}
	if gotSchema != schema {
		t.Fatalf("bootstrap schema %q, projects.schema_name %q", gotSchema, schema)
	}
	for _, role := range []string{creds.Runtime.Username, creds.Readonly.Username} {
		mustExecT(t, dedAdmin, fmt.Sprintf(`GRANT CONNECT ON DATABASE %s TO %s`,
			pgx.Identifier{dedDB}.Sanitize(), pgx.Identifier{role}.Sanitize()))
	}
	if err := dbprovider.LockdownReadonlyGrants(ctx, ownerDSN, schema, nil); err != nil {
		t.Fatalf("LockdownReadonlyGrants: %v", err)
	}
	// Exists only here, so reads from the stale shared copy are detectable.
	// Created as the owner, like any customer table there.
	ownerPool := mustPool(ownerDSN)
	mustExecT(t, ownerPool, fmt.Sprintf(`CREATE TABLE %s (id int)`, pgx.Identifier{schema, "ded_only"}.Sanitize()))
	mustExecT(t, ownerPool, fmt.Sprintf(`CREATE INDEX ded_only_marker_idx ON %s (id)`, pgx.Identifier{schema, "ded_only"}.Sanitize()))
	mustExecT(t, ownerPool, fmt.Sprintf(`CREATE POLICY ded_only_policy ON %s FOR SELECT USING (true)`, pgx.Identifier{schema, "ded_only"}.Sanitize()))
	mustExecT(t, ownerPool, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS int LANGUAGE sql AS 'SELECT 1'`, pgx.Identifier{schema, "ded_only_fn"}.Sanitize()))

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
		ProviderID: "e2e-" + slug, Host: ou.Hostname(), Port: port, DBName: dedDB,
		Username: ou.User.Username(), State: dbprovider.StateActive, Region: "fr-par",
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

	// The real router with Team routing on and platform auth wired as in
	// cmd/gateway.
	t.Setenv("VAULT_ENCRYPTION_KEY", keyB64)
	t.Setenv("TEAM_TIER_ROUTING", "1")
	platformSvc := auth.NewPlatformAuthService(gw, randHexT(t, 32))
	jwt, _, err := platformSvc.IssuePlatformJWT(ownerUser, ownerEmail, false)
	if err != nil {
		t.Fatal(err)
	}
	subdomain := auth.NewSubdomainMiddleware(gw, "eurobase.test")
	vaultSvc, err := vault.NewVaultService(gw, keyB64)
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(gw, dev, nil, auth.NewPlatformAuthMiddleware(platformSvc), platformSvc, nil, nil, nil, nil, nil,
		subdomain, nil, nil, nil, vaultSvc, "", nil, "", nil, nil, nil, nil, SSOWiring{}, nil, nil)

	return &teamEnv{
		router: router, platformJWT: jwt, projectID: projectID, slug: slug, schema: schema, dedDB: dedDB,
		secretKey: sec, scenario: scenario, upgraded: upgraded, sharedFingerprint: sharedFP, shared: admin,
		ownerUser: ownerUser, ownerEmail: ownerEmail, gw: gw, dev: dev, ded: dedAdmin,
	}
}

func mustExecT(t *testing.T, p *pgxpool.Pool, q string, args ...any) {
	t.Helper()
	if _, err := p.Exec(context.Background(), q, args...); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
}
