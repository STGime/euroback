package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// #874: GET …/schema/objects lists the project schema's views,
// materialized views, sequences and types — and nothing from other
// schemas, row types of tables, array types or extension members.
//
//	CONVERGE_TEST_ADMIN_URL=postgres://postgres@localhost:5432/eurobase \
//	go test ./internal/query/ -run SchemaObjects
func TestSchemaObjects_DB(t *testing.T) {
	adminURL := os.Getenv("CONVERGE_TEST_ADMIN_URL")
	if adminURL == "" {
		t.Skip("CONVERGE_TEST_ADMIN_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	suffix := fmt.Sprintf("%d", os.Getpid())
	schema := "tenant_schemaobj_" + suffix
	other := "tenant_schemaobj_other_" + suffix
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		_, _ = pool.Exec(c, `DROP SCHEMA IF EXISTS `+other+` CASCADE`)
	})
	s := schema + "."
	for _, stmt := range []string{
		`CREATE SCHEMA ` + schema,
		`CREATE SCHEMA ` + other,
		`CREATE TABLE ` + s + `teams (id serial PRIMARY KEY, name text)`,
		`CREATE TABLE ` + s + `games (id bigint GENERATED ALWAYS AS IDENTITY, team_id int)`,
		// An internal table's sequence: hidden from a viewer.
		`CREATE TABLE ` + s + `storage_objects (n serial)`,
		`CREATE SEQUENCE ` + s + `invoice_no AS integer`,
		`CREATE VIEW ` + s + `team_names WITH (security_invoker = true) AS SELECT name FROM ` + s + `teams`,
		`CREATE VIEW ` + s + `owner_view AS SELECT id FROM ` + s + `teams`,
		`CREATE MATERIALIZED VIEW ` + s + `team_count AS SELECT count(*) FROM ` + s + `teams`,
		`CREATE TYPE ` + s + `position AS ENUM ('goalie', 'defender', 'forward')`,
		`CREATE TYPE ` + s + `score AS (home int, away int)`,
		`CREATE DOMAIN ` + s + `jersey AS smallint CHECK (VALUE BETWEEN 1 AND 99)`,
		`CREATE TYPE ` + s + `minutes AS RANGE (subtype = int4)`,
		`ALTER TYPE ` + s + `position ADD VALUE 'center' BEFORE 'forward'`,
		// A domain over the project's own type shows it unqualified
		// (quoted, since position is a keyword).
		`CREATE DOMAIN ` + s + `main_position AS ` + s + `position`,
		`CREATE TYPE ` + s + `later_type`,
		// Another schema: never listed.
		`CREATE VIEW ` + other + `.secret_view AS SELECT 1 AS x`,
		`CREATE SEQUENCE ` + other + `.secret_seq`,
		`CREATE TYPE ` + other + `.secret_type AS ENUM ('x')`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	objs, err := ListSchemaObjects(ctx, pool, schema, true, false)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(objs)
	if strings.Contains(string(body), "secret_") {
		t.Fatalf("another schema leaked: %s", body)
	}

	views := map[string]SchemaView{}
	for _, v := range objs.Views {
		views[v.Name] = v
	}
	if len(views) != 3 ||
		!views["team_names"].SecurityInvoker || views["team_names"].Materialized ||
		views["owner_view"].SecurityInvoker ||
		!views["team_count"].Materialized || views["team_count"].SecurityInvoker ||
		!strings.Contains(views["team_names"].Definition, "FROM") {
		t.Fatalf("views: %+v", objs.Views)
	}

	seqs := map[string]string{}
	for _, q := range objs.Sequences {
		seqs[q.Name] = q.DataType + " " + q.OwnedBy
	}
	want := map[string]string{
		"teams_id_seq":          "integer teams.id",
		"games_id_seq":          "bigint games.id",
		"storage_objects_n_seq": "integer storage_objects.n",
		"invoice_no":            "integer ",
	}
	if len(seqs) != len(want) {
		t.Fatalf("sequences: %+v", objs.Sequences)
	}
	for k, v := range want {
		if seqs[k] != v {
			t.Fatalf("sequence %s: got %q, want %q (%+v)", k, seqs[k], v, objs.Sequences)
		}
	}

	types := map[string]SchemaType{}
	for _, ty := range objs.Types {
		types[ty.Name] = ty
	}
	if len(types) != 6 ||
		types["later_type"].Kind != "shell" ||
		types["minutes"].Multirange != "minutes_multirange" ||
		types["position"].Kind != "enum" || strings.Join(types["position"].Labels, ",") != "goalie,defender,center,forward" ||
		types["main_position"].BaseType != `"position"` ||
		types["score"].Kind != "composite" ||
		types["jersey"].Kind != "domain" || types["jersey"].BaseType != "smallint" ||
		types["minutes"].Kind != "range" || types["minutes"].BaseType != "integer" {
		t.Fatalf("types (table row types, arrays and multiranges must not appear): %+v", objs.Types)
	}

	// Viewer: no definitions, no sequences of platform tables.
	objs, err = ListSchemaObjects(ctx, pool, schema, false, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range objs.Views {
		if v.Definition != "" {
			t.Fatalf("viewer got a definition: %+v", v)
		}
	}
	for _, q := range objs.Sequences {
		if q.Name == "storage_objects_n_seq" {
			t.Fatalf("viewer sees a platform table's sequence: %+v", objs.Sequences)
		}
	}

	// The handler applies that by role. Needs the platform's projects
	// table (the CI database has it).
	var hasProjects bool
	_ = pool.QueryRow(ctx, `SELECT to_regclass('public.projects') IS NOT NULL AND to_regclass('public.platform_users') IS NOT NULL`).Scan(&hasProjects)
	if !hasProjects {
		t.Log("no public.projects: handler checks skipped")
		return
	}
	email := "schemaobj-" + suffix + "@test.eurobase.local"
	var ownerID, projectID string
	if err := pool.QueryRow(ctx, `INSERT INTO platform_users (email) VALUES ($1) RETURNING id`, email).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		 VALUES ($1, 'SchemaObj', $2, $3, $4, 'fr-par', 'free', 'active') RETURNING id`,
		ownerID, "schemaobj-"+suffix, schema, "b-schemaobj-"+suffix).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(c, `DELETE FROM platform_users WHERE id = $1`, ownerID)
	})
	get := func(role string) (int, SchemaObjects) {
		r := httptest.NewRequest(http.MethodGet, "/schema/objects", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", projectID)
		c := context.WithValue(r.Context(), chi.RouteCtxKey, rctx)
		r = r.WithContext(WithPlatformRole(c, role))
		w := httptest.NewRecorder()
		HandleSchemaObjects(pool)(w, r)
		var o SchemaObjects
		_ = json.Unmarshal(w.Body.Bytes(), &o)
		return w.Code, o
	}
	code, o := get("developer")
	if code != http.StatusOK || len(o.Views) != 3 || o.Views[0].Definition == "" || len(o.Sequences) != 4 {
		t.Fatalf("developer: %d %+v", code, o)
	}
	// Viewer, and any role that isn't developer+ (fail closed): names only.
	for _, role := range []string{"viewer", "", "auditor"} {
		code, o = get(role)
		if code != http.StatusOK || len(o.Views) != 3 || len(o.Sequences) != 3 {
			t.Fatalf("role %q: %d %+v", role, code, o)
		}
		for _, v := range o.Views {
			if v.Definition != "" {
				t.Fatalf("role %q got a view definition: %+v", role, v)
			}
		}
	}
}

// A Team project without its dedicated pool is refused, never read from
// the shared cluster.
func TestListSchemaObjects_DedicatedUnavailable(t *testing.T) {
	ctx := WithDedicatedDB(context.Background())
	if _, err := ListSchemaObjects(ctx, nil, "tenant_x", true, false); !errors.Is(err, ErrDedicatedPoolUnavailable) {
		t.Fatalf("want ErrDedicatedPoolUnavailable, got %v", err)
	}
}
