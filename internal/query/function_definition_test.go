package query

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// #820: GET …/schema/functions/{name} returns a function's definition —
// only for the project's own schema: the functions the list shows plus
// trigger functions and the platform's RLS helpers (marked platform) —
// and the list carries arguments + security so overloads can be told
// apart.
//
//	CONVERGE_TEST_ADMIN_URL=postgres://postgres@localhost:5432/eurobase \
//	go test ./internal/query/ -run GetFunctions
func TestGetFunctions_DB(t *testing.T) {
	adminURL := os.Getenv("CONVERGE_TEST_ADMIN_URL")
	if adminURL == "" {
		t.Skip("CONVERGE_TEST_ADMIN_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close) // first, so it runs after the cleanup below

	suffix := fmt.Sprintf("%d", os.Getpid())
	email := "fndef-" + suffix + "@test.eurobase.local"
	var ownerID, projectID, schema string
	if err := pool.QueryRow(ctx, `INSERT INTO platform_users (email) VALUES ($1) RETURNING id`, email).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		 VALUES ($1, 'FnDef', $2, 'tenant_placeholder', $3, 'fr-par', 'free', 'provisioning') RETURNING id`,
		ownerID, "fndef-"+suffix, "b-fndef-"+suffix).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	other := "tenant_fndef_other_" + suffix
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `SELECT deprovision_tenant($1)`, projectID)
		_, _ = pool.Exec(c, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(c, `DELETE FROM platform_users WHERE id = $1`, ownerID)
		_, _ = pool.Exec(c, `DROP SCHEMA IF EXISTS `+other+` CASCADE`)
		_, _ = pool.Exec(c, `DROP FUNCTION IF EXISTS public.fndef_public_`+suffix+`()`)
	})
	if _, err := pool.Exec(ctx, `SELECT provision_tenant($1, 'FnDef', 'free')`, projectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT schema_name FROM projects WHERE id = $1`, projectID).Scan(&schema); err != nil {
		t.Fatal(err)
	}

	q := `"` + schema + `"`
	for _, stmt := range []string{
		`CREATE FUNCTION ` + q + `.is_current_user_head_coach() RETURNS boolean LANGUAGE plpgsql AS $$ BEGIN RETURN true; END $$`,
		`CREATE FUNCTION ` + q + `.score(team_id uuid) RETURNS integer LANGUAGE sql AS $$ SELECT 1 $$`,
		`CREATE FUNCTION ` + q + `.score(team_id uuid, season integer) RETURNS integer LANGUAGE sql SECURITY DEFINER AS $$ SELECT 2 $$`,
		`CREATE FUNCTION ` + q + `.on_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`,
		// Same name in another schema and in public: never visible here.
		`CREATE SCHEMA ` + other,
		`CREATE FUNCTION ` + other + `.secret_fn() RETURNS integer LANGUAGE sql AS $$ SELECT 42 $$`,
		`CREATE FUNCTION public.fndef_public_` + suffix + `() RETURNS integer LANGUAGE sql AS $$ SELECT 7 $$`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	get := func(name, rawQuery string) (int, []byte) {
		target := "/" + name
		if rawQuery != "" {
			target += "?" + rawQuery
		}
		r := httptest.NewRequest(http.MethodGet, target, nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", projectID)
		rctx.URLParams.Add("funcName", name)
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()
		handleGetFunction(pool)(w, r)
		return w.Code, w.Body.Bytes()
	}

	// One function: the full definition.
	code, body := get("is_current_user_head_coach", "")
	var f DBFunction
	_ = json.Unmarshal(body, &f)
	if code != http.StatusOK || f.Language != "plpgsql" || f.ReturnType != "boolean" || f.Security != "invoker" ||
		!strings.Contains(f.Definition, "CREATE OR REPLACE FUNCTION") || !strings.Contains(f.Definition, "RETURN true") {
		t.Fatalf("get is_current_user_head_coach: %d %s", code, body)
	}

	// Overloads: 409 with candidates, ?args= picks one (whitespace-insensitive).
	code, body = get("score", "")
	if code != http.StatusConflict || !strings.Contains(string(body), `"team_id uuid, season integer"`) {
		t.Fatalf("overloads without args: %d %s", code, body)
	}
	code, body = get("score", "args=team_id%20uuid,season%20%20integer")
	_ = json.Unmarshal(body, &f)
	if code != http.StatusOK || f.Arguments != "team_id uuid, season integer" || f.Security != "definer" || !strings.Contains(f.Definition, "SELECT 2") {
		t.Fatalf("overload by args: %d %s", code, body)
	}
	if code, _ := get("score", "args=nope"); code != http.StatusNotFound {
		t.Fatalf("unknown args: %d", code)
	}

	// Trigger functions and the platform's RLS helpers aren't in the list
	// but are readable by name, so a project can see what its triggers
	// and policies call. Helpers are marked platform.
	code, body = get("on_insert", "")
	f = DBFunction{}
	_ = json.Unmarshal(body, &f)
	if code != http.StatusOK || f.ReturnType != "trigger" || f.Platform || !strings.Contains(f.Definition, "RETURN NEW") {
		t.Fatalf("get trigger function: %d %s", code, body)
	}
	for _, name := range []string{"auth_uid", "auth_role", "auth_email"} {
		code, body = get(name, "")
		f = DBFunction{}
		_ = json.Unmarshal(body, &f)
		if code != http.StatusOK || !f.Platform || f.Security != "invoker" || f.Definition == "" {
			t.Fatalf("get %s: %d %s", name, code, body)
		}
	}
	if code, body = get("auth_uid", ""); !strings.Contains(string(body), "current_end_user_id") {
		t.Fatalf("auth_uid definition: %d %s", code, body)
	}

	// Never readable: other schemas, public, extension members.
	for _, name := range []string{"secret_fn", "fndef_public_" + suffix, "uuid_generate_v4"} {
		if code, body := get(name, ""); code != http.StatusNotFound {
			t.Fatalf("%s must be 404: %d %s", name, code, body)
		}
	}

	// The list: arguments + security, no definitions, overloads apart.
	funcs, err := ListFunctions(ctx, pool, schema)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, fn := range funcs {
		if fn.Definition != "" {
			t.Fatalf("list must not carry definitions: %+v", fn)
		}
		got = append(got, fn.Name+"("+fn.Arguments+") "+fn.Security)
	}
	want := []string{"is_current_user_head_coach() invoker", "score(team_id uuid) invoker", "score(team_id uuid, season integer) definer"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("list = %v, want %v", got, want)
	}
}
