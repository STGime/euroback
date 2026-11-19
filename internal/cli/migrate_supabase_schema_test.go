package cli

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/eurobase/euroback/internal/query"
)

func hasWarning(res translationResult, part string) bool {
	for _, w := range res.warnings {
		if strings.Contains(w.note, part) {
			return true
		}
	}
	return false
}

// pg_dump puts a comment header in front of every object; the policy
// wrap used to look for CREATE POLICY at the very start of the
// statement and so never fired on a real dump.
func TestTranslate_WrapsPolicyAfterPgDumpHeader(t *testing.T) {
	in := `--
-- Name: posts owner_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY owner_select ON public.posts FOR SELECT USING ((user_id = auth.uid()));
`
	got := Translate(in).sql
	if !strings.Contains(got, "USING (public.is_service_role() OR ((user_id = auth_uid())))") {
		t.Errorf("policy after a pg_dump header not wrapped:\n%s", got)
	}
}

func TestTranslate_PolicyRoles(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"authenticated",
			`CREATE POLICY p ON t FOR SELECT TO authenticated USING (true);`,
			`CREATE POLICY p ON t FOR SELECT USING (public.is_service_role() OR ((auth_uid() IS NOT NULL) AND (true)));`},
		{"anon",
			`CREATE POLICY p ON t FOR INSERT TO anon WITH CHECK (true);`,
			`CREATE POLICY p ON t FOR INSERT WITH CHECK (public.is_service_role() OR ((auth_uid() IS NULL) AND (true)));`},
		{"anon and authenticated = everyone",
			`CREATE POLICY p ON t FOR SELECT TO anon, authenticated USING (true);`,
			`CREATE POLICY p ON t FOR SELECT USING (public.is_service_role() OR (true));`},
		{"public = everyone",
			`CREATE POLICY p ON t FOR SELECT TO public USING (true);`,
			`CREATE POLICY p ON t FOR SELECT USING (public.is_service_role() OR (true));`},
		{"service_role only",
			`CREATE POLICY p ON t TO service_role USING (true) WITH CHECK (true);`,
			`CREATE POLICY p ON t USING (public.is_service_role() OR ((false) AND (true))) WITH CHECK (public.is_service_role() OR ((false) AND (true)));`},
		{"quoted name containing TO and USING",
			`CREATE POLICY "Users TO see USING (x)" ON t FOR SELECT TO authenticated USING ((id = auth.uid()));`,
			`CREATE POLICY "Users TO see USING (x)" ON t FOR SELECT USING (public.is_service_role() OR ((auth_uid() IS NOT NULL) AND ((id = auth_uid()))));`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strings.Join(strings.Fields(Translate(c.in).sql), " ")
			want := strings.Join(strings.Fields(c.want), " ")
			if got != want {
				t.Errorf("\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

func TestTranslate_PolicyUnknownRoleWarns(t *testing.T) {
	res := Translate(`CREATE POLICY p ON t FOR SELECT TO reporting USING (true);`)
	if !hasWarning(res, `role "reporting"`) {
		t.Errorf("no warning for unknown role: %+v", res.warnings)
	}
	if !strings.Contains(res.sql, "TO reporting") {
		t.Errorf("unknown role clause should be left for the apply to name:\n%s", res.sql)
	}
}

// auth.role() must say 'anon' without a signed-in end user — otherwise
// `auth.role() = 'authenticated'` admits anonymous callers.
func TestTranslate_AuthRoleDistinguishesAnon(t *testing.T) {
	got := Translate(`CREATE POLICY p ON t FOR SELECT USING ((auth.role() = 'authenticated'::text));`).sql
	if !strings.Contains(got, "WHEN auth_uid() IS NULL THEN 'anon'") {
		t.Errorf("auth.role() rewrite doesn't distinguish anon:\n%s", got)
	}
}

func TestTranslate_StripsPublicEverywhereButHelpers(t *testing.T) {
	in := `CREATE TYPE public.user_role AS ENUM ('a', 'b');
CREATE TABLE public."MixedCase" (id integer, role public.user_role);
CREATE FUNCTION public.touch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$;
CREATE TRIGGER t BEFORE UPDATE ON public.posts FOR EACH ROW EXECUTE FUNCTION public.touch();
ALTER TABLE ONLY public.posts ALTER COLUMN id SET DEFAULT nextval('public.posts_id_seq'::regclass);
ALTER SEQUENCE public.posts_id_seq OWNED BY public.posts.id;
CREATE POLICY p ON public.posts USING (public.is_service_role() OR (public.current_end_user_id() = user_id));
`
	got := Translate(in).sql
	for _, bad := range []string{"public.user_role", `public."MixedCase"`, "public.touch", "public.posts"} {
		if strings.Contains(got, bad) {
			t.Errorf("%s survived:\n%s", bad, got)
		}
	}
	for _, want := range []string{
		"CREATE TYPE user_role", `CREATE TABLE "MixedCase"`, "role user_role",
		"EXECUTE FUNCTION touch()", "nextval('posts_id_seq'::regclass)",
		"OWNED BY posts.id", "public.is_service_role()", "public.current_end_user_id()",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

func TestTranslate_StripsPgDump18Preamble(t *testing.T) {
	in := `\restrict AbC123
SET statement_timeout = 0;
SELECT pg_catalog.set_config('search_path', '', false);
CREATE TABLE public.t (key text COLLATE pg_catalog."C" NOT NULL);
\unrestrict AbC123
`
	got := Translate(in).sql
	for _, bad := range []string{`\restrict`, `\unrestrict`, "set_config", "pg_catalog"} {
		if strings.Contains(got, bad) {
			t.Errorf("%s survived:\n%s", bad, got)
		}
	}
	if !strings.Contains(got, `key text COLLATE "C" NOT NULL`) {
		t.Errorf("collation lost:\n%s", got)
	}
}

func TestTranslate_SecurityDefinerBecomesInvoker(t *testing.T) {
	in := `CREATE FUNCTION public.handle_new_user() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO ''
    AS $$
BEGIN
  INSERT INTO public.user_profiles (id, email) VALUES (NEW.id, NEW.email);
  -- SECURITY DEFINER in a comment stays
  RETURN NEW;
END;
$$;
`
	res := Translate(in)
	if strings.Contains(strings.ReplaceAll(res.sql, "-- SECURITY DEFINER in a comment stays", ""), "SECURITY DEFINER\n") ||
		strings.Contains(res.sql, "plpgsql SECURITY DEFINER") {
		t.Errorf("SECURITY DEFINER survived:\n%s", res.sql)
	}
	if !strings.Contains(res.sql, "-- SECURITY DEFINER in a comment stays") {
		t.Errorf("body comment changed:\n%s", res.sql)
	}
	if !strings.Contains(res.sql, "INSERT INTO user_profiles (id, email)") {
		t.Errorf("body not translated:\n%s", res.sql)
	}
	if strings.Contains(res.sql, "search_path") {
		t.Errorf("function search_path setting survived (migrations refuse it):\n%s", res.sql)
	}
	if !hasWarning(res, "handle_new_user was SECURITY DEFINER") {
		t.Errorf("no warning: %+v", res.warnings)
	}
	if err := query.ValidateTenantMigrationSQL(res.sql); err != nil {
		t.Errorf("platform would refuse: %v\n%s", err, res.sql)
	}
}

func TestTranslate_ViewsBecomeSecurityInvoker(t *testing.T) {
	cases := map[string]string{
		`CREATE VIEW public.v AS SELECT 1;`:                                        `CREATE VIEW v WITH (security_invoker='true') AS SELECT 1;`,
		`CREATE VIEW public.v WITH (check_option='local') AS SELECT 1;`:            `CREATE VIEW v WITH (security_invoker='true', check_option='local') AS SELECT 1;`,
		`CREATE VIEW public.v WITH (security_invoker='false') AS SELECT 1;`:        `CREATE VIEW v WITH (security_invoker='true') AS SELECT 1;`,
		`CREATE VIEW public.v WITH (security_invoker='true') AS SELECT 'AS' AS x;`: `CREATE VIEW v WITH (security_invoker='true') AS SELECT 'AS' AS x;`,
	}
	for in, want := range cases {
		got := strings.Join(strings.Fields(Translate(in).sql), " ")
		if got != want {
			t.Errorf("\n  in: %s\n got: %s\nwant: %s", in, got, want)
		}
	}
}

func TestTranslate_Moddatetime(t *testing.T) {
	in := `CREATE TRIGGER handle_updated_at BEFORE UPDATE ON public.posts FOR EACH ROW EXECUTE FUNCTION extensions.moddatetime('updated_at');`
	res := Translate(in)
	if !strings.Contains(res.sql, "EXECUTE FUNCTION eurobase_moddatetime('updated_at')") || !strings.Contains(res.sql, "CREATE FUNCTION eurobase_moddatetime()") {
		t.Errorf("moddatetime not replaced:\n%s", res.sql)
	}
	if strings.Count(Translate(in+"\n"+in).sql, "CREATE FUNCTION eurobase_moddatetime()") != 1 {
		t.Error("moddatetime() added more than once")
	}
}

func TestTranslate_WarnsOnOtherExtensionsRefs(t *testing.T) {
	res := Translate(`CREATE INDEX i ON public.posts USING gin (title extensions.gin_trgm_ops);`)
	if !hasWarning(res, "`extensions` schema") {
		t.Errorf("no warning: %+v", res.warnings)
	}
}

func TestTranslate_CommentsOutUnsupportedDumpObjects(t *testing.T) {
	in := `--
-- Name: pg_stat_statements; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pg_stat_statements WITH SCHEMA public;


--
-- Name: EXTENSION pg_stat_statements; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON EXTENSION pg_stat_statements IS 'track statistics';


--
-- Name: stats; Type: MATERIALIZED VIEW; Schema: public; Owner: -
--

CREATE MATERIALIZED VIEW public.stats AS
 SELECT count(*) AS n
   FROM public.posts
  WITH NO DATA;


--
-- Name: stats_n_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX stats_n_idx ON public.stats USING btree (n);


--
-- Name: posts_n_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX posts_n_idx ON public.posts USING btree (n);


--
-- Name: buckets; Type: TABLE; Schema: storage; Owner: -
--

CREATE TABLE storage.buckets (id text);


--
-- Name: supabase_realtime posts; Type: PUBLICATION TABLE; Schema: public; Owner: -
--

ALTER PUBLICATION supabase_realtime ADD TABLE ONLY public.posts;
`
	res := Translate(in)
	for _, line := range strings.Split(res.sql, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		if trimmed != "CREATE INDEX posts_n_idx ON posts USING btree (n);" {
			t.Errorf("not commented out: %q", line)
		}
	}
	for _, want := range []string{"extensions are managed", "materialized views", "Realtime publication", "only the public schema"} {
		if !hasWarning(res, want) {
			t.Errorf("missing warning %q: %+v", want, res.warnings)
		}
	}
	if err := query.ValidateTenantMigrationSQL(res.sql); err != nil {
		t.Errorf("platform would refuse: %v\n%s", err, res.sql)
	}
}

// A pg_dump 18 dump shaped like a typical Supabase app (headers, \restrict,
// enums, serial, auth FKs, TO authenticated policies, a definer trigger
// function, an updated_at trigger, a view) must come out as a migration
// the platform accepts.
func TestTranslate_PgDump18SamplePassesPlatformValidation(t *testing.T) {
	in := `--
-- PostgreSQL database dump
--

\restrict X1y2

-- Dumped from database version 17.6
-- Dumped by pg_dump version 18.1

SET statement_timeout = 0;
SET lock_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET row_security = off;

--
-- Name: public; Type: SCHEMA; Schema: -; Owner: -
--

CREATE SCHEMA public;


--
-- Name: SCHEMA public; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON SCHEMA public IS 'standard public schema';


--
-- Name: user_role; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.user_role AS ENUM (
    'user',
    'admin'
);


--
-- Name: handle_new_user(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.handle_new_user() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO ''
    AS $$
BEGIN
  INSERT INTO public.profiles (id, email) VALUES (NEW.id, NEW.email);
  RETURN NEW;
END;
$$;


--
-- Name: is_admin(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.is_admin() RETURNS boolean
    LANGUAGE sql STABLE
    AS $$
  SELECT EXISTS (SELECT 1 FROM public.profiles WHERE id = auth.uid() AND role = 'admin'::public.user_role);
$$;


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: profiles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.profiles (
    id uuid NOT NULL,
    email text,
    role public.user_role DEFAULT 'user'::public.user_role NOT NULL,
    updated_at timestamp with time zone DEFAULT now()
);


--
-- Name: posts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.posts (
    id bigint NOT NULL,
    user_id uuid DEFAULT auth.uid(),
    slug text COLLATE pg_catalog."C",
    body text
);


--
-- Name: posts_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.posts_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: posts_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.posts_id_seq OWNED BY public.posts.id;


--
-- Name: my_posts; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.my_posts AS
 SELECT id, body FROM public.posts WHERE (user_id = auth.uid());


--
-- Name: posts id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.posts ALTER COLUMN id SET DEFAULT nextval('public.posts_id_seq'::regclass);


--
-- Name: profiles profiles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.profiles
    ADD CONSTRAINT profiles_pkey PRIMARY KEY (id);


--
-- Name: profiles handle_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER handle_updated_at BEFORE UPDATE ON public.profiles FOR EACH ROW EXECUTE FUNCTION extensions.moddatetime('updated_at');


--
-- Name: profiles profiles_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.profiles
    ADD CONSTRAINT profiles_id_fkey FOREIGN KEY (id) REFERENCES auth.users(id) ON DELETE CASCADE;


--
-- Name: posts Admins read all; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins read all" ON public.posts FOR SELECT TO authenticated USING (public.is_admin());


--
-- Name: posts Public read; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Public read" ON public.posts FOR SELECT USING (true);


--
-- Name: profiles Own profile; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Own profile" ON public.profiles TO authenticated USING ((( SELECT auth.uid() AS uid) = id)) WITH CHECK ((( SELECT auth.uid() AS uid) = id));


--
-- Name: posts; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.posts ENABLE ROW LEVEL SECURITY;

--
-- Name: profiles; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.profiles ENABLE ROW LEVEL SECURITY;

--
-- PostgreSQL database dump complete
--

\unrestrict X1y2
`
	res := Translate(in)
	if err := query.ValidateTenantMigrationSQL(res.sql); err != nil {
		t.Fatalf("platform would refuse the translated dump: %v\n%s", err, res.sql)
	}
	if n := strings.Count(res.sql, "public.is_service_role() OR ("); n != 4 {
		t.Errorf("policy wraps = %d, want 4:\n%s", n, res.sql)
	}
	for _, want := range []string{
		"SELECT EXISTS (SELECT 1 FROM profiles WHERE id = auth_uid() AND role = 'admin'::user_role);",
		"WITH (security_invoker='true')",
		"EXECUTE FUNCTION eurobase_moddatetime('updated_at')",
		"REFERENCES users(id)",
		"user_id uuid DEFAULT auth_uid()",
	} {
		if !strings.Contains(res.sql, want) {
			t.Errorf("missing %q:\n%s", want, res.sql)
		}
	}
}

// TestSchemaImport_Fixture runs the whole command path — pg_dump of a
// Supabase-shaped database, translation, the platform's validator, and
// an apply inside a project-like schema — against ASSESS_SCRATCH_DB_URL
// (a throwaway local database; see TestReadSupabaseCode_Fixture). The
// apply runs in a transaction that is rolled back.
func TestSchemaImport_Fixture(t *testing.T) {
	dsn := os.Getenv("ASSESS_SCRATCH_DB_URL")
	if dsn == "" {
		t.Skip("ASSESS_SCRATCH_DB_URL not set")
	}
	if _, err := exec.LookPath("pg_dump"); err != nil {
		t.Skip("pg_dump not on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conn := openSupabaseFixture(ctx, t, dsn)
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, schemaImportFixtureSQL); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	dump, err := exec.CommandContext(ctx, "pg_dump", "--schema-only", "--no-owner", "--no-privileges",
		"--no-tablespaces", "--schema=public", dsn).Output()
	if err != nil {
		t.Fatalf("pg_dump: %v", err)
	}
	res := Translate(string(dump))
	if err := query.ValidateTenantMigrationSQL(res.sql); err != nil {
		t.Fatalf("platform would refuse: %v\n%s", err, res.sql)
	}
	if !hasWarning(res, "handle_new_user was SECURITY DEFINER") {
		t.Errorf("definer not reported: %+v", res.warnings)
	}

	// GRANT / REVOKE restrictions are read from the source.
	grants, err := readNarrowedGrants(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	var grantLines []string
	for _, g := range grants {
		grantLines = append(grantLines, g.String())
	}
	wantGrant := "table profiles: anon has no SELECT; column grants: SELECT (id, role)\n" +
		"function is_admin(): anon has no EXECUTE"
	if strings.Join(grantLines, "\n") != wantGrant {
		t.Errorf("narrowed grants:\n%s\nwant:\n%s", strings.Join(grantLines, "\n"), wantGrant)
	}

	// The command refuses to write while access changes are unaccepted,
	// and writes them into the file's header once accepted.
	t.Setenv("SUPABASE_DB_URL", dsn)
	out := t.TempDir() + "/m.sql"
	cmd := importSupabaseSchemaCmd()
	cmd.SetArgs([]string{"--output", out})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.ExecuteContext(ctx); err == nil || !strings.Contains(err.Error(), "--accept-access-changes") {
		t.Errorf("expected a refusal, got %v", err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("file written despite the refusal")
	}
	cmd = importSupabaseSchemaCmd()
	cmd.SetArgs([]string{"--output", out, "--accept-access-changes"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("with --accept-access-changes: %v", err)
	}
	written, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "-- ACCESS CHANGES") || !strings.Contains(string(written), "GRANT/REVOKE not carried over: table profiles") {
		t.Errorf("header doesn't list the access changes:\n%.1500s", written)
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// What a project schema provides: the platform helpers in public,
	// and users / auth_uid() / auth_email() in the project schema.
	if _, err := tx.Exec(ctx, `
		CREATE FUNCTION public.current_end_user_id() RETURNS uuid LANGUAGE sql STABLE
		  AS $$ SELECT nullif(current_setting('app.end_user_id', true), '')::uuid $$;
		CREATE FUNCTION public.is_service_role() RETURNS boolean LANGUAGE sql STABLE
		  AS $$ SELECT current_setting('app.end_user_role', true) = 'service' $$;
		CREATE SCHEMA tenant_schemaimport;
		SET LOCAL search_path TO tenant_schemaimport;
		CREATE TABLE users (id uuid PRIMARY KEY, email text);
		CREATE FUNCTION auth_uid() RETURNS uuid LANGUAGE sql STABLE AS $$ SELECT public.current_end_user_id() $$;
		CREATE FUNCTION auth_email() RETURNS text LANGUAGE sql STABLE AS $$ SELECT email FROM users WHERE id = public.current_end_user_id() $$;
	`); err != nil {
		t.Fatalf("project stubs: %v", err)
	}
	if _, err := tx.Exec(ctx, res.sql); err != nil {
		t.Fatalf("apply: %v\n%s", err, res.sql)
	}
	// The translated objects work at runtime: the updated_at trigger
	// (moddatetime replacement) and a function body that used auth.uid().
	if _, err := tx.Exec(ctx, `
		SELECT set_config('app.end_user_id', 'a0000000-0000-0000-0000-000000000001', true);
		INSERT INTO users VALUES ('a0000000-0000-0000-0000-000000000001', 'x@example.invalid');
		INSERT INTO profiles (id, updated_at) VALUES ('a0000000-0000-0000-0000-000000000001', '2000-01-01');
		UPDATE profiles SET role = 'admin';
	`); err != nil {
		t.Fatalf("runtime: %v", err)
	}
	var admin, fresh bool
	if err := tx.QueryRow(ctx, `SELECT is_admin(), updated_at > now() - interval '1 minute' FROM profiles`).Scan(&admin, &fresh); err != nil {
		t.Fatal(err)
	}
	if !admin || !fresh {
		t.Errorf("is_admin() = %v, updated_at refreshed = %v", admin, fresh)
	}

	// The translated policies, as a non-owner role (like SDK traffic):
	//   - profiles "Own profile" (TO authenticated): the signed-in owner
	//     sees the row, anonymous callers and other users don't;
	//   - notes "Read all" + a RESTRICTIVE own-only policy TO
	//     authenticated: anonymous callers see every row (the
	//     restriction isn't theirs), signed-in users only their own;
	//   - members_only (auth.role() = 'authenticated'): not anonymous;
	//   - posts "Anyone reads posts" (TO anon, authenticated): everyone.
	// The service key sees everything.
	if _, err := tx.Exec(ctx, `
		INSERT INTO notes VALUES (1, 'a0000000-0000-0000-0000-000000000001'), (2, 'b0000000-0000-0000-0000-000000000002');
		INSERT INTO members_only VALUES (1);
		INSERT INTO server_only VALUES (1);
		INSERT INTO posts (user_id) VALUES ('a0000000-0000-0000-0000-000000000001');
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		CREATE ROLE eb_schemaimport_sdk NOLOGIN;
		GRANT USAGE ON SCHEMA tenant_schemaimport TO eb_schemaimport_sdk;
		GRANT SELECT ON ALL TABLES IN SCHEMA tenant_schemaimport TO eb_schemaimport_sdk;
		SET LOCAL ROLE eb_schemaimport_sdk;
	`); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, uid, role, table string
		want                   int
	}{
		{"anonymous", "", "anon", "profiles", 0},
		{"owner", "a0000000-0000-0000-0000-000000000001", "", "profiles", 1},
		{"other user", "b0000000-0000-0000-0000-000000000002", "", "profiles", 0},
		{"service key", "", "service", "profiles", 1},
		{"anonymous", "", "anon", "notes", 2},
		{"owner", "a0000000-0000-0000-0000-000000000001", "", "notes", 1},
		{"other user", "b0000000-0000-0000-0000-000000000002", "", "notes", 1},
		{"service key", "", "service", "notes", 2},
		{"anonymous", "", "anon", "members_only", 0},
		{"owner", "a0000000-0000-0000-0000-000000000001", "", "members_only", 1},
		{"service key", "", "service", "members_only", 1},
		{"anonymous", "", "anon", "server_only", 0},
		{"owner", "a0000000-0000-0000-0000-000000000001", "", "server_only", 0},
		{"service key", "", "service", "server_only", 1},
		{"anonymous", "", "anon", "posts", 1},
		{"other user", "b0000000-0000-0000-0000-000000000002", "", "posts", 1},
	} {
		var n int
		if err := tx.QueryRow(ctx, `
			SELECT set_config('app.end_user_id', $1, true), set_config('app.end_user_role', $2, true)`,
			c.uid, c.role).Scan(new(string), new(string)); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+c.table).Scan(&n); err != nil {
			t.Fatalf("%s on %s: %v", c.name, c.table, err)
		}
		if n != c.want {
			t.Errorf("%s sees %d rows of %s, want %d", c.name, n, c.table, c.want)
		}
	}
}

// openSupabaseFixture connects to the scratch database and refuses
// anything that looks like a real Supabase project, then builds the
// shared fixture.
func openSupabaseFixture(ctx context.Context, t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var looksReal bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname IN ('realtime', '_realtime', 'supabase_migrations'))
		    OR EXISTS (SELECT 1 FROM pg_roles WHERE rolname IN ('supabase_admin', 'authenticator'))`).Scan(&looksReal); err != nil {
		t.Fatal(err)
	}
	if !looksReal {
		var n int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM auth.users`).Scan(&n); err == nil && n > 1 {
			looksReal = true
		}
	}
	if looksReal {
		conn.Close(ctx)
		t.Fatal("ASSESS_SCRATCH_DB_URL points at what looks like a real Supabase project; refusing to drop its schemas")
	}
	if _, err := conn.Exec(ctx, schemaImportCleanupSQL+supabaseFixtureSQL); err != nil {
		conn.Close(ctx)
		t.Fatalf("fixture: %v", err)
	}
	return conn
}

// schemaImportCleanupSQL removes what schemaImportFixtureSQL adds, so
// the shared fixture is the same whichever test ran before.
const schemaImportCleanupSQL = `
DROP MATERIALIZED VIEW IF EXISTS public.post_counts;
DROP TABLE IF EXISTS public.profiles CASCADE;
DROP FUNCTION IF EXISTS public.is_admin() CASCADE;
DROP TYPE IF EXISTS public.user_role;
DROP TABLE IF EXISTS public.notes, public.members_only, public.server_only CASCADE;
`

// schemaImportFixtureSQL adds what a typical app has on top of
// supabaseFixtureSQL (which has the definer signup function): policies
// for anon / authenticated, a helper used by policies, an updated_at trigger, a
// materialized view. Idempotent.
const schemaImportFixtureSQL = `
CREATE OR REPLACE FUNCTION auth.uid() RETURNS uuid LANGUAGE sql STABLE
  AS $f$ SELECT nullif(current_setting('request.jwt.claim.sub', true), '')::uuid $f$;
CREATE OR REPLACE FUNCTION extensions.moddatetime() RETURNS trigger LANGUAGE plpgsql AS $f$ BEGIN RETURN NEW; END $f$;

CREATE TYPE public.user_role AS ENUM ('user', 'admin');
CREATE TABLE public.profiles (
  id uuid PRIMARY KEY REFERENCES auth.users(id) ON DELETE CASCADE,
  role public.user_role NOT NULL DEFAULT 'user',
  updated_at timestamptz DEFAULT now()
);
ALTER TABLE public.profiles ENABLE ROW LEVEL SECURITY;
CREATE FUNCTION public.is_admin() RETURNS boolean LANGUAGE sql STABLE
  AS $f$ SELECT EXISTS (SELECT 1 FROM public.profiles WHERE id = auth.uid() AND role = 'admin') $f$;
CREATE TRIGGER handle_updated_at BEFORE UPDATE ON public.profiles
  FOR EACH ROW EXECUTE FUNCTION extensions.moddatetime('updated_at');
CREATE POLICY "Own profile" ON public.profiles TO authenticated
  USING ((SELECT auth.uid()) = id) WITH CHECK ((SELECT auth.uid()) = id);
CREATE POLICY "Anyone reads posts" ON public.posts FOR SELECT TO anon, authenticated USING (true);
CREATE POLICY "Admins write posts" ON public.posts FOR ALL TO authenticated USING (public.is_admin());
CREATE TABLE public.notes (id integer PRIMARY KEY, owner uuid);
ALTER TABLE public.notes ENABLE ROW LEVEL SECURITY;
CREATE POLICY "Read all" ON public.notes FOR SELECT USING (true);
CREATE POLICY "Signed-in users: own only" ON public.notes AS RESTRICTIVE FOR SELECT TO authenticated
  USING (owner = auth.uid());
CREATE TABLE public.members_only (id integer PRIMARY KEY);
ALTER TABLE public.members_only ENABLE ROW LEVEL SECURITY;
CREATE OR REPLACE FUNCTION auth.role() RETURNS text LANGUAGE sql STABLE AS $f$ SELECT 'anon' $f$;
CREATE POLICY "Members" ON public.members_only FOR SELECT USING (auth.role() = 'authenticated');
-- EXECUTE revoked: a trigger function (not callable directly anyway —
-- not reported) and a helper closed to anon (reported).
REVOKE EXECUTE ON FUNCTION public.handle_new_user() FROM PUBLIC, anon, authenticated;
REVOKE EXECUTE ON FUNCTION public.is_admin() FROM PUBLIC, anon;
GRANT EXECUTE ON FUNCTION public.is_admin() TO authenticated;
-- Column-level security: anon may read only id and role.
REVOKE SELECT ON public.profiles FROM anon;
GRANT SELECT (id, role) ON public.profiles TO anon;
-- Server-only table: RLS on, no policies, closed to anon / authenticated.
CREATE TABLE public.server_only (id integer PRIMARY KEY);
ALTER TABLE public.server_only ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON public.server_only FROM anon, authenticated;
CREATE MATERIALIZED VIEW public.post_counts AS SELECT user_id, count(*) AS n FROM public.posts GROUP BY user_id;
CREATE INDEX post_counts_user_idx ON public.post_counts (user_id);
`

// ── review fixes ─────────────────────────────────────────────────────

// pg_dump writes SECURITY DEFINER between other attributes on one line;
// removing it must not glue its neighbours together.
func TestTranslate_SecurityDefinerBetweenAttributes(t *testing.T) {
	for _, in := range []string{
		"CREATE FUNCTION public.f() RETURNS boolean\n    LANGUAGE sql STABLE SECURITY DEFINER PARALLEL SAFE\n    AS $$ SELECT true $$;",
		"CREATE FUNCTION public.f() RETURNS boolean LANGUAGE sql SECURITY DEFINER AS $$ SELECT true $$;",
		"CREATE FUNCTION public.f() RETURNS boolean\n    LANGUAGE sql SECURITY DEFINER LEAKPROOF COST 10\n    AS $$ SELECT true $$;",
	} {
		var code []string // the output without the explanatory comment
		for _, ln := range strings.Split(Translate(in).sql, "\n") {
			if !strings.HasPrefix(ln, "--") {
				code = append(code, ln)
			}
		}
		got := strings.Join(code, "\n")
		for _, glued := range []string{"STABLEPARALLEL", "sqlAS", "sqlLEAKPROOF", "DEFINER"} {
			if strings.Contains(got, glued) {
				t.Errorf("%q in output:\n%s", glued, got)
			}
		}
	}
	got := strings.Join(strings.Fields(Translate("CREATE FUNCTION public.f() RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER PARALLEL SAFE AS $$ SELECT true $$;").sql), " ")
	if !strings.Contains(got, "LANGUAGE sql STABLE PARALLEL SAFE AS $$ SELECT true $$;") {
		t.Errorf("attributes mangled: %s", got)
	}
}

// A restrictive policy only restricts the roles it names; callers
// outside its TO list pass it.
func TestTranslate_RestrictivePolicyRoles(t *testing.T) {
	got := strings.Join(strings.Fields(Translate(
		`CREATE POLICY r ON t AS RESTRICTIVE FOR SELECT TO authenticated USING ((owner = auth.uid()));`).sql), " ")
	want := `CREATE POLICY r ON t AS RESTRICTIVE FOR SELECT USING (public.is_service_role() OR ((NOT (auth_uid() IS NOT NULL)) OR ((owner = auth_uid()))));`
	if got != want {
		t.Errorf("\n got: %s\nwant: %s", got, want)
	}
}

// Role / JWT-setting checks have no Eurobase meaning and can fail open.
func TestTranslate_WarnsOnRoleChecks(t *testing.T) {
	for _, in := range []string{
		`CREATE POLICY p ON t USING ((CURRENT_USER <> 'anon'::name));`,
		`CREATE POLICY p ON t USING ((COALESCE(current_setting('request.jwt.claim.role', true), '') <> 'anon'));`,
		"CREATE FUNCTION public.f() RETURNS boolean LANGUAGE sql AS $$ SELECT session_user <> 'anon' $$;",
	} {
		if res := Translate(in); !hasWarning(res, "lets anonymous callers through") {
			t.Errorf("no warning for %s: %+v", in, res.warnings)
		}
	}
	if res := Translate(`CREATE POLICY p ON t USING ((owner = auth.uid()));`); hasWarning(res, "lets anonymous callers through") {
		t.Errorf("false positive: %+v", res.warnings)
	}
}

func TestTranslate_WarnsOnPublicAsName(t *testing.T) {
	res := Translate(`CREATE POLICY p ON t USING ((EXISTS ( SELECT 1 FROM public.groups public WHERE (public.id = t.group_id))));`)
	if !hasWarning(res, "uses `public` as a name") {
		t.Errorf("no warning: %+v", res.warnings)
	}
	if res := Translate(`CREATE POLICY p ON public.t USING (public.is_service_role());`); hasWarning(res, "uses `public` as a name") {
		t.Errorf("false positive: %+v", res.warnings)
	}
}

// Only the function body (the dollar quote after AS) is translated; a
// dollar-quoted parameter default is data.
func TestTranslate_DollarQuotedDefaultIsData(t *testing.T) {
	got := Translate("CREATE FUNCTION public.f(p text DEFAULT $$public.secret$$) RETURNS text LANGUAGE sql AS $$ SELECT p FROM public.t $$;").sql
	if !strings.Contains(got, "DEFAULT $$public.secret$$") {
		t.Errorf("default changed:\n%s", got)
	}
	if !strings.Contains(got, "AS $$ SELECT p FROM t $$") {
		t.Errorf("body not translated:\n%s", got)
	}
}

// Comments, indexes and views that depend on a skipped materialized
// view are skipped with it (otherwise the apply fails on the missing
// relation); unrelated objects aren't.
func TestTranslate_SkipsMatviewDependents(t *testing.T) {
	in := `--
-- Name: mv; Type: MATERIALIZED VIEW; Schema: public; Owner: -
--

CREATE MATERIALIZED VIEW public.mv AS SELECT 1 AS a WITH NO DATA;


--
-- Name: COLUMN mv.a; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.mv.a IS 'x';


--
-- Name: on_mv; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.on_mv AS SELECT a FROM public.mv;


--
-- Name: on_on_mv; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.on_on_mv AS SELECT a FROM public.on_mv;


--
-- Name: mvx; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.mvx AS SELECT 1 AS mv;
`
	res := Translate(in)
	var live []string
	for _, line := range strings.Split(res.sql, "\n") {
		if tr := strings.TrimSpace(line); tr != "" && !strings.HasPrefix(tr, "--") {
			live = append(live, tr)
		}
	}
	if len(live) != 1 || !strings.HasPrefix(live[0], "CREATE VIEW mvx") {
		t.Errorf("live statements = %q", live)
	}
}

// A line inside a function body that looks like a pg_dump header must
// not split the dump there.
func TestTranslate_HeaderLookalikeInBody(t *testing.T) {
	in := `--
-- Name: f(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.f() RETURNS integer LANGUAGE plpgsql AS $$
BEGIN
-- Name: x; Type: EXTENSION; Schema: -;
  RETURN 1;
END $$;
`
	got := Translate(in).sql
	if !strings.Contains(got, "\n  RETURN 1;\nEND $$;") || strings.Contains(got, "not imported") {
		t.Errorf("body commented out:\n%s", got)
	}
}

// A literal that happens to contain the wrap text doesn't stop the
// service-key wrap.
func TestTranslate_WrapIdempotenceIsPerClause(t *testing.T) {
	got := Translate(`CREATE POLICY p ON t USING ((note <> 'public.is_service_role() OR ('::text));`).sql
	if !strings.Contains(got, "USING (public.is_service_role() OR ((note <>") {
		t.Errorf("not wrapped:\n%s", got)
	}
	twice := Translate(got).sql
	// One wrap plus the copy inside the literal.
	if twice != got || strings.Count(twice, "public.is_service_role() OR (") != 2 {
		t.Errorf("wrapped twice on rerun:\n%s", twice)
	}
}

// A function's own settings stay; search_path / role settings are
// removed (the platform refuses them).
func TestTranslate_FunctionSettings(t *testing.T) {
	in := "CREATE FUNCTION public.f() RETURNS integer\n    LANGUAGE sql\n    SET statement_timeout TO '5s'\n    SET search_path TO ''\n    AS $$ SELECT 1 $$;"
	got := Translate(in).sql
	if !strings.Contains(got, "SET statement_timeout TO '5s'") {
		t.Errorf("function setting removed:\n%s", got)
	}
	if strings.Contains(got, "search_path") {
		t.Errorf("search_path setting kept:\n%s", got)
	}
}

// ── second review ────────────────────────────────────────────────────

func accessNotes(res translationResult) []string {
	var out []string
	for _, w := range res.warnings {
		if w.access {
			out = append(out, w.note)
		}
	}
	return out
}

func TestTranslate_RoleSettingChecksAreAccessChanges(t *testing.T) {
	for _, in := range []string{
		"CREATE FUNCTION public.guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF current_setting('role') = 'authenticated' THEN RAISE EXCEPTION 'no'; END IF; RETURN NEW; END $$;",
		`CREATE POLICY p ON t USING ((current_setting('role'::text) <> 'anon'::text));`,
		`CREATE POLICY p ON t USING (pg_has_role('authenticated', 'member'));`,
		`CREATE POLICY p ON t USING ((CURRENT_USER <> 'anon'::name));`,
	} {
		if len(accessNotes(Translate(in))) == 0 {
			t.Errorf("not an access change: %s", in)
		}
	}
}

// A former definer function used in a policy or trigger is an access
// change: as invoker it can't see what it checked for.
func TestTranslate_DefinerUsedInPolicyIsAccessChange(t *testing.T) {
	in := `CREATE FUNCTION public.is_banned() RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER AS $$ SELECT EXISTS (SELECT 1 FROM public.banned_users WHERE id = auth.uid()) $$;
CREATE POLICY "Insert unless banned" ON public.posts FOR INSERT TO authenticated WITH CHECK (((auth.uid() = user_id) AND (NOT public.is_banned())));
CREATE TRIGGER t BEFORE INSERT ON public.posts FOR EACH ROW EXECUTE FUNCTION public.is_banned();
CREATE POLICY "Other" ON public.posts FOR SELECT USING (true);`
	notes := accessNotes(Translate(in))
	if len(notes) != 2 || !strings.Contains(notes[0], `policy "Insert unless banned" calls is_banned`) || !strings.Contains(notes[1], "trigger t calls is_banned") {
		t.Errorf("access notes = %q", notes)
	}
}

// Leaving out a policy, an RLS switch or a trigger is an access change;
// leaving out an index isn't.
func TestTranslate_SkippedPolicyIsAccessChange(t *testing.T) {
	in := `--
-- Name: active_subs; Type: MATERIALIZED VIEW; Schema: public; Owner: -
--

CREATE MATERIALIZED VIEW public.active_subs AS SELECT 1 AS user_id WITH NO DATA;


--
-- Name: active_subs_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX active_subs_idx ON public.active_subs USING btree (user_id);


--
-- Name: posts Paid only; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Paid only" ON public.posts AS RESTRICTIVE FOR SELECT USING ((EXISTS ( SELECT 1 FROM public.active_subs WHERE (active_subs.user_id = auth.uid()))));
`
	notes := accessNotes(Translate(in))
	if len(notes) != 1 || !strings.Contains(notes[0], "posts Paid only is not imported") {
		t.Errorf("access notes = %q", notes)
	}
}

// A quoted mixed-case materialized view doesn't drag a lowercase table
// of the same spelling along.
func TestTranslate_SkippedMatviewNameIsCaseSensitive(t *testing.T) {
	in := `--
-- Name: Orders; Type: MATERIALIZED VIEW; Schema: public; Owner: -
--

CREATE MATERIALIZED VIEW public."Orders" AS SELECT 1 AS n WITH NO DATA;


--
-- Name: orders; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.orders ENABLE ROW LEVEL SECURITY;
`
	if got := Translate(in).sql; !strings.Contains(got, "\nALTER TABLE orders ENABLE ROW LEVEL SECURITY;") {
		t.Errorf("RLS switch of orders was skipped:\n%s", got)
	}
}

func TestTranslate_ModdatetimeChecksColumn(t *testing.T) {
	got := Translate(`CREATE TRIGGER t BEFORE UPDATE ON public.p FOR EACH ROW EXECUTE FUNCTION extensions.moddatetime('updated_at');`).sql
	if !strings.Contains(got, "to_jsonb(NEW) ? TG_ARGV[0]") || !strings.Contains(got, "RAISE EXCEPTION") {
		t.Errorf("moddatetime() doesn't check its column:\n%s", got)
	}
}

func TestTranslate_WarnsOnPlatformTableNameAndPrivateSchema(t *testing.T) {
	res := Translate(`CREATE TABLE public.users (id uuid);
CREATE POLICY p ON public.posts USING (private.is_org_member(org_id));`)
	if !hasWarning(res, "table users has the name of a table every Eurobase project already has") {
		t.Errorf("no platform-table warning: %+v", res.warnings)
	}
	if !hasWarning(res, "schema other than") {
		t.Errorf("no private-schema warning: %+v", res.warnings)
	}
}

// Every table with RLS gets the service-role policy: on Supabase the
// service key bypasses RLS for every command. A table whose policies
// cover only SELECT (posts) must still be writable by server code.
func TestTranslate_ServicePolicyForEveryRLSTable(t *testing.T) {
	in := `CREATE TABLE public.tokens (id integer);
CREATE TABLE public.posts (id integer);
CREATE TABLE public."Mixed" (id integer);
ALTER TABLE public.tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.posts ENABLE ROW LEVEL SECURITY;
ALTER TABLE public."Mixed" ENABLE ROW LEVEL SECURITY;
CREATE POLICY "Read" ON public.posts FOR SELECT USING (true);
-- ALTER TABLE public.commented ENABLE ROW LEVEL SECURITY;
`
	got := Translate(in).sql
	for _, want := range []string{
		"CREATE POLICY eurobase_service_role ON tokens USING (public.is_service_role()) WITH CHECK (public.is_service_role());",
		"CREATE POLICY eurobase_service_role ON posts USING (public.is_service_role()) WITH CHECK (public.is_service_role());",
		`CREATE POLICY eurobase_service_role ON "Mixed" USING`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "CREATE POLICY eurobase_service_role") != 3 {
		t.Errorf("want one service policy per table with RLS, none for a commented-out statement:\n%s", got)
	}
	if err := query.ValidateTenantMigrationSQL(got); err != nil {
		t.Errorf("platform would refuse: %v", err)
	}
}

// ── third review ─────────────────────────────────────────────────────

// A function that calls a former definer function is as affected as
// the definer itself; so are views, column defaults and CHECK
// constraints that call either.
func TestTranslate_DefinerUseIsTransitive(t *testing.T) {
	in := `CREATE FUNCTION public.can_post() RETURNS boolean LANGUAGE sql AS $$ SELECT NOT public.is_banned() $$;
CREATE FUNCTION public.is_banned() RETURNS boolean LANGUAGE sql SECURITY DEFINER AS $$ SELECT EXISTS (SELECT 1 FROM public.banned WHERE id = auth.uid()) $$;
CREATE FUNCTION public.unrelated() RETURNS boolean LANGUAGE sql AS $$ SELECT true $$;
CREATE TABLE public.docs (id integer, ok boolean DEFAULT public.can_post(), CONSTRAINT c CHECK (public.unrelated()));
CREATE VIEW public.v AS SELECT id FROM public.docs WHERE public.is_banned();
CREATE POLICY p ON public.docs FOR INSERT WITH CHECK (public.can_post());
CREATE POLICY q ON public.docs FOR SELECT USING (public.unrelated());`
	notes := accessNotes(Translate(in))
	want := []string{"table docs calls can_post", "view v calls is_banned", "policy p calls can_post"}
	if len(notes) != len(want) {
		t.Fatalf("access notes = %q", notes)
	}
	for i, w := range want {
		if !strings.Contains(notes[i], w) {
			t.Errorf("note %d = %q, want it to contain %q", i, notes[i], w)
		}
	}
}

// A SQL-standard body (BEGIN ATOMIC … END) stays one statement: its
// calls count for the definer check, and SECURITY DEFINER is removed.
func TestTranslate_BeginAtomicIsOneStatement(t *testing.T) {
	in := "CREATE FUNCTION public.is_banned() RETURNS boolean LANGUAGE sql SECURITY DEFINER AS $$ SELECT true $$;\n" +
		"CREATE FUNCTION public.gate() RETURNS boolean\n    LANGUAGE sql\n    BEGIN ATOMIC\n SELECT 1;\n SELECT (NOT public.is_banned());\nEND;\n" +
		"CREATE POLICY p ON public.t FOR INSERT WITH CHECK (public.gate());"
	res := Translate(in)
	if notes := accessNotes(res); len(notes) != 1 || !strings.Contains(notes[0], "policy p calls gate") {
		t.Errorf("access notes = %q", notes)
	}
	if !strings.Contains(res.sql, " SELECT (NOT is_banned());\nEND;") {
		t.Errorf("atomic body changed:\n%s", res.sql)
	}
}

// Only column-0 SET lines (pg_dump's preamble) are stripped; the SET
// of a multi-line UPDATE stays, whatever the column is called.
func TestTranslate_IndentedSetLinesStay(t *testing.T) {
	in := "CREATE RULE r AS\n    ON UPDATE TO public.v DO INSTEAD  UPDATE public.t\n  SET role = new.role\n  WHERE (t.id = old.id);"
	got := Translate(in).sql
	if !strings.Contains(got, "\n  SET role = new.role\n") {
		t.Errorf("UPDATE's SET line removed:\n%s", got)
	}
}

// ENABLE ROW LEVEL SECURITY text inside a function body isn't a
// statement of the migration.
func TestTranslate_ServiceOnlyPolicyIgnoresFunctionBodies(t *testing.T) {
	in := "CREATE FUNCTION public.mk() RETURNS void LANGUAGE plpgsql AS $$\nBEGIN\nALTER TABLE secrets ENABLE ROW LEVEL SECURITY;\nEND $$;"
	if got := Translate(in).sql; strings.Contains(got, "eurobase_service_role") {
		t.Errorf("policy added for text in a function body:\n%s", got)
	}
}

// The view check reads the statement, not pg_dump's comment header.
func TestTranslate_ViewCheckIgnoresComments(t *testing.T) {
	in := "--\n-- Name: security_invoker=on; Type: VIEW; Schema: public; Owner: -\n--\n\nCREATE VIEW public.\"security_invoker=on\" AS\n SELECT 1;"
	if got := Translate(in).sql; !strings.Contains(got, "WITH (security_invoker='true')") {
		t.Errorf("view left to run with its owner's rights:\n%s", got)
	}
}

// An app's own moddatetime() isn't what the extension's triggers get.
func TestTranslate_OwnModdatetimeIsUntouched(t *testing.T) {
	in := `CREATE FUNCTION public.moddatetime() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$;
CREATE TRIGGER t BEFORE UPDATE ON public.p FOR EACH ROW EXECUTE FUNCTION extensions.moddatetime('updated_at');`
	got := Translate(in).sql
	if !strings.Contains(got, "EXECUTE FUNCTION eurobase_moddatetime('updated_at')") || !strings.Contains(got, "CREATE FUNCTION eurobase_moddatetime()") {
		t.Errorf("trigger not pointed at the replacement:\n%s", got)
	}
}
