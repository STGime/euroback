package cli

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func findItem(t *testing.T, items []item, namePart string) item {
	t.Helper()
	for _, it := range items {
		if strings.Contains(it.name, namePart) {
			return it
		}
	}
	t.Fatalf("no item containing %q in %+v", namePart, items)
	return item{}
}

func TestGradeFunctions_SecurityDefinerIsBlocker(t *testing.T) {
	items := gradeFunctions([]sbFunction{
		{Schema: "public", Name: "handle_new_user", SecDef: true},
		{Schema: "public", Name: "touch_updated_at"},
		{Schema: "public", Name: "apply_delta"},
		{Schema: "private", Name: "is_admin", SecDef: true},
		{Schema: "private", Name: "helper"},
	})
	if it := findItem(t, items, "handle_new_user"); it.grade != gradeBlocker || !strings.Contains(it.note, "SECURITY INVOKER") {
		t.Errorf("definer function: %+v", it)
	}
	if it := findItem(t, items, "2 function(s) in public"); it.grade != gradeOK || !strings.Contains(it.note, "apply_delta, touch_updated_at") {
		t.Errorf("invoker summary: %+v", it)
	}
	if it := findItem(t, items, "private.is_admin"); it.grade != gradeBlocker || !strings.Contains(it.note, "doesn't copy") {
		t.Errorf("definer in own schema: %+v", it)
	}
	if it := findItem(t, items, "private.helper"); it.grade != gradeWarn {
		t.Errorf("invoker in own schema: %+v", it)
	}
}

func TestGradeTriggers(t *testing.T) {
	ins := []string{"INSERT"}
	items := gradeTriggers([]sbTrigger{
		{TableSchema: "auth", Table: "users", Name: "on_auth_user_created", FuncSchema: "public", FuncName: "handle_new_user", Events: ins},
		{TableSchema: "auth", Table: "users", Name: "notify_slack", FuncSchema: "supabase_functions", FuncName: "http_request", Events: ins},
		{TableSchema: "auth", Table: "users", Name: "user_changed", FuncSchema: "supabase_functions", FuncName: "http_request", Events: []string{"UPDATE", "DELETE"}},
		{TableSchema: "public", Table: "orders", Name: "audit_orders", FuncSchema: "supabase_functions", FuncName: "http_request", Events: []string{"INSERT", "UPDATE"}},
		{TableSchema: "storage", Table: "objects", Name: "file_hook", FuncSchema: "supabase_functions", FuncName: "http_request", Events: []string{"INSERT", "DELETE"}},
		{TableSchema: "storage", Table: "objects", Name: "update_objects_updated_at", FuncSchema: "storage", FuncName: "update_updated_at_column", Events: []string{"UPDATE"}},
		{TableSchema: "storage", Table: "objects", Name: "on_upload", FuncSchema: "public", FuncName: "index_file", Events: ins},
		{TableSchema: "public", Table: "posts", Name: "handle_updated_at", FuncSchema: "extensions", FuncName: "moddatetime", Events: []string{"UPDATE"}},
		{TableSchema: "public", Table: "posts", Name: "touch_posts", FuncSchema: "public", FuncName: "touch", Events: []string{"UPDATE"}},
	})
	all := ""
	for _, it := range items {
		all += it.note + "\n"
	}
	for _, bad := range []string{"auth.signup", "auth.signin", "function trigger"} {
		if strings.Contains(all, bad) {
			t.Errorf("report suggests %q, which Eurobase doesn't have", bad)
		}
	}
	if it := findItem(t, items, "on_auth_user_created"); it.grade != gradeBlocker || !strings.Contains(it.note, "auth.user.created") {
		t.Errorf("auth.users trigger: %+v", it)
	}
	if it := findItem(t, items, "webhook: notify_slack"); it.grade != gradeBlocker || !strings.Contains(it.note, "webhook on auth.user.created") {
		t.Errorf("signup webhook: %+v", it)
	}
	if it := findItem(t, items, "webhook: user_changed"); !strings.Contains(it.note, "auth.user.deleted") || !strings.Contains(it.note, "no event for update") {
		t.Errorf("update/delete webhook on users: %+v", it)
	}
	if it := findItem(t, items, "webhook: audit_orders"); !strings.Contains(it.note, "db.insert / db.update") {
		t.Errorf("table webhook: %+v", it)
	}
	if it := findItem(t, items, "webhook: file_hook"); !strings.Contains(it.note, "storage.upload / storage.delete") || strings.Contains(it.note, "db.") {
		t.Errorf("storage webhook: %+v", it)
	}
	if it := findItem(t, items, "on_upload"); it.grade != gradeWarn {
		t.Errorf("own storage trigger: %+v", it)
	}
	if it := findItem(t, items, "handle_updated_at"); it.grade != gradeBlocker || !strings.Contains(it.note, "moddatetime") {
		t.Errorf("trigger on non-public function: %+v", it)
	}
	for _, it := range items {
		if strings.Contains(it.name, "update_objects_updated_at") {
			t.Errorf("Supabase's own storage trigger should be skipped: %+v", it)
		}
	}
	if it := findItem(t, items, "1 trigger(s) on your tables"); it.grade != gradeOK {
		t.Errorf("own triggers: %+v", it)
	}
}

func TestGradeViews(t *testing.T) {
	items := gradeViews([]sbView{
		{Name: "latest", AnonCanRead: true},
		{Name: "safe", SecurityInvoker: true},
		{Name: "stats", Materialized: true},
	}, 7, 1)
	if it := findItem(t, items, "view: public.latest"); it.grade != gradeBlocker || !strings.Contains(it.note, "security_invoker = true") || !strings.Contains(it.note, "anon key") {
		t.Errorf("owner-run view: %+v", it)
	}
	if it := findItem(t, items, "view: public.safe"); it.grade != gradeOK {
		t.Errorf("invoker view: %+v", it)
	}
	if it := findItem(t, items, "materialized view"); it.grade != gradeBlocker {
		t.Errorf("matview: %+v", it)
	}
	findItem(t, items, "7 enum type(s), 1 sequence(s)")
}

func TestHasSecurityInvoker(t *testing.T) {
	for opts, want := range map[string]bool{
		"":                      false,
		"security_invoker=true": true,
		"security_barrier=true,security_invoker=on": true,
		"security_invoker=false":                    false,
		"check_option=local":                        false,
	} {
		if got := hasSecurityInvoker(opts); got != want {
			t.Errorf("%q: got %v want %v", opts, got, want)
		}
	}
}

// Supabase installs pg_net, supabase_vault, pgsodium in every project:
// installed-but-unused must not be a blocker.
func TestGradeExtensions_DefaultsGradedByUse(t *testing.T) {
	exts := []string{"pg_net", "supabase_vault", "pgsodium", "pg_stat_statements", "pg_graphql", "pg_trgm"}

	unused := gradeExtensions(exts, &sbCode{vaultSecrets: 0})
	for _, name := range []string{"pg_net", "supabase_vault", "pgsodium", "pg_stat_statements"} {
		if it := findItem(t, unused, name); it.grade != gradeOK {
			t.Errorf("unused %s: %+v", name, it)
		}
	}
	if it := findItem(t, unused, "pg_graphql"); it.grade != gradeWarn {
		t.Errorf("pg_graphql: %+v", it)
	}
	if it := findItem(t, unused, "pg_trgm"); it.grade != gradeWarn {
		t.Errorf("pg_trgm (not installed on Eurobase): %+v", it)
	}

	used := gradeExtensions(exts, &sbCode{
		vaultSecrets: 2,
		functions: []sbFunction{
			{Schema: "public", Name: "call_api", Body: "select net.http_post(url := 'x')"},
			{Schema: "public", Name: "get_key", Body: "select decrypted_secret from vault.decrypted_secrets"},
			{Schema: "public", Name: "enc", Body: "select pgsodium.crypto_aead_det_encrypt(...)"},
		},
		triggers: []sbTrigger{{TableSchema: "auth", Table: "users", Name: "hook", FuncSchema: "supabase_functions", FuncName: "http_request"}},
	})
	if it := findItem(t, used, "pg_net"); it.grade != gradeBlocker || !strings.Contains(it.note, "call_api") || strings.Contains(it.note, "database webhook") {
		t.Errorf("used pg_net (webhooks are listed on their own): %+v", it)
	}
	webhookOnly := gradeExtensions([]string{"pg_net"}, &sbCode{vaultSecrets: 0,
		triggers: []sbTrigger{{TableSchema: "auth", Table: "users", Name: "hook", FuncSchema: "supabase_functions", FuncName: "http_request"}}})
	if it := findItem(t, webhookOnly, "pg_net"); it.grade != gradeOK || !strings.Contains(it.note, "database webhooks listed above") {
		t.Errorf("pg_net used only by webhooks: %+v", it)
	}
	if it := findItem(t, used, "supabase_vault"); it.grade != gradeBlocker || !strings.Contains(it.note, "get_key") {
		t.Errorf("used vault: %+v", it)
	}
	if it := findItem(t, used, "pgsodium"); it.grade != gradeBlocker {
		t.Errorf("used pgsodium: %+v", it)
	}

	secretsOnly := gradeExtensions([]string{"supabase_vault"}, &sbCode{vaultSecrets: 3})
	if it := findItem(t, secretsOnly, "supabase_vault"); it.grade != gradeWarn || !strings.Contains(it.note, "3 secret(s)") {
		t.Errorf("vault with secrets: %+v", it)
	}
}

func TestGradeTable_RLSOff(t *testing.T) {
	it := gradeTable("api_tokens", 32, 80*1024, false, []string{"SELECT", "INSERT"})
	if it.grade != gradeWarn || !strings.Contains(it.note, "RLS is disabled") || !strings.Contains(it.note, "SELECT / INSERT") {
		t.Errorf("rls off: %+v", it)
	}
	if it := gradeTable("posts", 1, 1, true, []string{"SELECT"}); it.grade != gradeOK {
		t.Errorf("rls on: %+v", it)
	}
	if it := gradeTable("MyTable", 1, 1, false, []string{"SELECT"}); !strings.Contains(it.note, `ALTER TABLE public."MyTable"`) {
		t.Errorf("mixed-case name must be quoted: %+v", it)
	}
	if it := gradeTable("a`b", 1, 1, false, []string{"SELECT"}); strings.Contains(it.note, "ALTER TABLE") {
		t.Errorf("name with a backtick gets no SQL suggestion: %+v", it)
	}
}

func TestMdNote_KeepsCodeSpans(t *testing.T) {
	got := mdNote("use `eurobase vault set` for my_table*")
	if got != `use `+"`eurobase vault set`"+` for my\_table\*` {
		t.Errorf("got %q", got)
	}
	// An unbalanced backtick falls back to full escaping.
	if got := mdNote("a ` b"); got != "a \\` b" {
		t.Errorf("unbalanced: %q", got)
	}
}

func TestReportHasNoInternalIssueNumbers(t *testing.T) {
	notes := []string{storagePolicyNote}
	for _, u := range []string{"auth.uid() = id", "auth.email() = x", "auth.jwt() ->> 'email' = x", "true", ""} {
		u := u
		n, _ := gradePolicy(&u, nil)
		notes = append(notes, n)
	}
	for _, n := range notes {
		if strings.Contains(n, "#2") {
			t.Errorf("internal issue number in customer-facing note: %q", n)
		}
	}
}

func TestGradePolicy_AuthJWTIsWarning(t *testing.T) {
	u := "(auth.jwt() ->> 'email') = owner_email"
	if note, grade := gradePolicy(&u, nil); grade != gradeWarn || !strings.Contains(note, "auth.jwt()") {
		t.Errorf("got %s %q", grade, note)
	}
}

func TestSupabaseConnectHint(t *testing.T) {
	for msg, want := range map[string]string{
		"FATAL: (ENOTFOUND) tenant/user postgres.abc not found":   "Session pooler",
		"failed SASL auth: FATAL: password authentication failed": "wrong database password",
		"dial tcp: lookup db.abc.supabase.co: no such host":       "IPv6-only",
		"connection refused": "",
	} {
		got := supabaseConnectHint(errString(msg))
		if (want == "" && got != "") || (want != "" && !strings.Contains(got, want)) {
			t.Errorf("%q: got %q, want hint containing %q", msg, got, want)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// TestReadSupabaseCode_Fixture runs the enumerators against a Postgres
// database shaped like a Supabase project (auth / storage / vault
// schemas, anon role, a database webhook, definer functions, an
// owner-run view). Set ASSESS_SCRATCH_DB_URL to a throwaway local
// database: the test DROPs and rebuilds auth / storage / vault schemas.
// It refuses to run on anything that looks like a real Supabase project.
func TestReadSupabaseCode_Fixture(t *testing.T) {
	dsn := os.Getenv("ASSESS_SCRATCH_DB_URL")
	if dsn == "" {
		t.Skip("ASSESS_SCRATCH_DB_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn := openSupabaseFixture(ctx, t, dsn)
	defer conn.Close(ctx)

	code, err := readSupabaseCode(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	dbCode := append(gradeFunctions(code.functions), gradeTriggers(code.triggers)...)
	if it := findItem(t, dbCode, "function: public.handle_new_user"); it.grade != gradeBlocker {
		t.Errorf("definer: %+v", it)
	}
	if it := findItem(t, dbCode, "trigger: on_auth_user_created"); it.grade != gradeBlocker {
		t.Errorf("auth trigger: %+v", it)
	}
	if it := findItem(t, dbCode, "webhook: signup_hook"); it.grade != gradeBlocker || !strings.Contains(it.note, "auth.user.created") {
		t.Errorf("webhook: %+v", it)
	}
	if it := findItem(t, dbCode, "private.is_admin"); it.grade != gradeBlocker {
		t.Errorf("definer in own schema: %+v", it)
	}
	if it := findItem(t, dbCode, "trigger: set_updated_at"); it.grade != gradeBlocker {
		t.Errorf("trigger on extensions function: %+v", it)
	}
	for _, it := range dbCode {
		if strings.Contains(it.note, "hooks.example") || strings.Contains(it.name, "hooks.example") {
			t.Errorf("trigger arguments leaked into the report: %+v", it)
		}
	}
	views := gradeViews(code.views, code.enumCount, code.sequenceCount)
	if it := findItem(t, views, "view: public.latest_posts"); it.grade != gradeBlocker || !strings.Contains(it.note, "anon key") {
		t.Errorf("owner-run view: %+v", it)
	}
	findItem(t, views, "view: public.safe_posts")
	findItem(t, views, "1 enum type(s)")
	for _, it := range views {
		if strings.Contains(it.name, "pg_stat_statements") {
			t.Errorf("extension-owned view graded: %+v", it)
		}
	}
	if code.vaultSecrets != 0 {
		t.Errorf("vault secrets = %d, want 0", code.vaultSecrets)
	}
	if len(code.realtimeTabs) != 1 || code.realtimeTabs[0] != "public.posts" {
		t.Errorf("realtime tables = %v", code.realtimeTabs)
	}

	exts := gradeExtensions([]string{"pg_net", "supabase_vault"}, code)
	if it := findItem(t, exts, "pg_net"); it.grade != gradeOK {
		t.Errorf("pg_net used only by the webhook: %+v", it)
	}
	if it := findItem(t, exts, "supabase_vault"); it.grade != gradeOK {
		t.Errorf("empty vault: %+v", it)
	}

	// Tables: RLS off + anon privileges, through the real query.
	r := &report{}
	if err := assessTables(ctx, conn, r); err != nil {
		t.Fatal(err)
	}
	if it := findItem(t, r.tables, "public.api_tokens"); it.grade != gradeWarn || !strings.Contains(it.note, "SELECT / INSERT / UPDATE / DELETE") {
		t.Errorf("api_tokens: %+v", it)
	}
	if it := findItem(t, r.tables, "public.posts"); it.grade != gradeOK {
		t.Errorf("posts: %+v", it)
	}
}

// supabaseFixtureSQL builds a small Supabase-shaped project. Idempotent.
const supabaseFixtureSQL = `
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'anon') THEN CREATE ROLE anon NOLOGIN; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN CREATE ROLE authenticated NOLOGIN; END IF;
END $$;
-- Like Supabase: new public tables are open to anon / authenticated.
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO anon, authenticated;
DROP EXTENSION IF EXISTS pg_stat_statements;
DROP SCHEMA IF EXISTS auth, storage, vault, supabase_functions, private, extensions CASCADE;
DROP VIEW IF EXISTS public.latest_posts, public.safe_posts;
DROP TABLE IF EXISTS public.posts, public.api_tokens CASCADE;
DROP FUNCTION IF EXISTS public.handle_new_user(), public.touch() CASCADE;
DROP TYPE IF EXISTS public.post_status;
DROP PUBLICATION IF EXISTS supabase_realtime;

CREATE SCHEMA auth;
CREATE TABLE auth.users (id uuid PRIMARY KEY, email text);
CREATE TABLE auth.identities (id uuid PRIMARY KEY, provider text);
CREATE SCHEMA storage;
CREATE TABLE storage.buckets (id text PRIMARY KEY, name text, public boolean);
CREATE TABLE storage.objects (id uuid PRIMARY KEY, bucket_id text, metadata jsonb);
CREATE SCHEMA vault;
CREATE TABLE vault.secrets (id uuid PRIMARY KEY, secret text);
CREATE SCHEMA supabase_functions;
CREATE FUNCTION supabase_functions.http_request() RETURNS trigger LANGUAGE plpgsql AS $f$ BEGIN RETURN NEW; END $f$;

CREATE TYPE public.post_status AS ENUM ('draft', 'live');
CREATE TABLE public.posts (id serial PRIMARY KEY, user_id uuid, status public.post_status);
ALTER TABLE public.posts ENABLE ROW LEVEL SECURITY;
CREATE TABLE public.api_tokens (id uuid PRIMARY KEY, user_id uuid, token_hash text);
GRANT SELECT, INSERT, UPDATE, DELETE ON public.api_tokens, public.posts TO anon;

CREATE FUNCTION public.handle_new_user() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
  AS $f$ BEGIN INSERT INTO public.posts (user_id) VALUES (NEW.id); RETURN NEW; END $f$;
CREATE FUNCTION public.touch() RETURNS trigger LANGUAGE plpgsql AS $f$ BEGIN RETURN NEW; END $f$;
CREATE TRIGGER on_auth_user_created AFTER INSERT ON auth.users FOR EACH ROW EXECUTE FUNCTION public.handle_new_user();
CREATE TRIGGER signup_hook AFTER INSERT ON auth.users FOR EACH ROW
  EXECUTE FUNCTION supabase_functions.http_request('https://hooks.example/secret-path', 'POST', '{}', '{}', '5000');
CREATE TRIGGER touch_posts BEFORE UPDATE ON public.posts FOR EACH ROW EXECUTE FUNCTION public.touch();

CREATE SCHEMA private;
CREATE FUNCTION private.is_admin() RETURNS boolean LANGUAGE sql SECURITY DEFINER AS $f$ SELECT true $f$;
CREATE SCHEMA extensions;
CREATE FUNCTION extensions.moddatetime() RETURNS trigger LANGUAGE plpgsql AS $f$ BEGIN RETURN NEW; END $f$;
CREATE TRIGGER set_updated_at BEFORE UPDATE ON public.posts FOR EACH ROW EXECUTE FUNCTION extensions.moddatetime();

-- An extension that puts views in public (like PostGIS does).
CREATE EXTENSION pg_stat_statements SCHEMA public;

CREATE VIEW public.latest_posts AS SELECT * FROM public.posts;
CREATE VIEW public.safe_posts WITH (security_invoker = true) AS SELECT * FROM public.posts;
GRANT SELECT ON public.latest_posts TO anon;

CREATE PUBLICATION supabase_realtime FOR TABLE public.posts;
`
