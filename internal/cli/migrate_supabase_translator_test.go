package cli

import (
	"strings"
	"testing"
)

// #269: exhaustive tests on the DDL translator. This is the piece where
// a subtle regex bug (an over-eager wrap clobbers a policy body) causes
// a broken migration that only shows up when the tenant applies it —
// so the tests are the safety net.
//
// Tests are grouped by rewrite rule + the wrapPolicyBody rules (which
// need their own paren-walker coverage) + the whole-input smoke tests.

// ── strip rules ──────────────────────────────────────────────────────

func TestTranslate_StripsSchemaMeta(t *testing.T) {
	in := `SET search_path = public;
CREATE SCHEMA public;
CREATE SCHEMA IF NOT EXISTS public;
ALTER SCHEMA public OWNER TO postgres;
COMMENT ON SCHEMA public IS 'standard public schema';
CREATE TABLE orders (id uuid primary key);`
	got := Translate(in).sql

	for _, banned := range []string{
		"SET search_path",
		"CREATE SCHEMA public",
		"CREATE SCHEMA IF NOT EXISTS public",
		"ALTER SCHEMA public OWNER",
		"COMMENT ON SCHEMA public",
	} {
		if strings.Contains(got, banned) {
			t.Errorf("output still contains %q:\n%s", banned, got)
		}
	}
	// Real DDL must survive.
	if !strings.Contains(got, "CREATE TABLE orders") {
		t.Errorf("stripper ate a real CREATE TABLE:\n%s", got)
	}
}

// ── atom rewrites ────────────────────────────────────────────────────

func TestTranslate_AuthUsersRefs(t *testing.T) {
	in := `CREATE TABLE profiles (
    user_id uuid REFERENCES auth.users(id) ON DELETE CASCADE
);`
	got := Translate(in).sql
	if !strings.Contains(got, "REFERENCES users(id)") {
		t.Errorf("auth.users → users failed:\n%s", got)
	}
	if strings.Contains(got, "auth.users") {
		t.Errorf("output still contains auth.users:\n%s", got)
	}
}

func TestTranslate_AuthUsersDoesntEatViewSuffix(t *testing.T) {
	// A table literally named `auth.users_view` mustn't get eaten.
	// The word-boundary regex should keep it intact.
	in := `SELECT * FROM auth.users_view;`
	got := Translate(in).sql
	if !strings.Contains(got, "auth.users_view") {
		t.Errorf("auth.users_view got mangled:\n%s", got)
	}
}

func TestTranslate_AuthUID(t *testing.T) {
	in := `CREATE POLICY sel ON orders FOR SELECT USING (user_id = auth.uid());`
	got := Translate(in).sql
	if !strings.Contains(got, "auth_uid()") {
		t.Errorf("auth.uid() → auth_uid() failed:\n%s", got)
	}
	if strings.Contains(got, "auth.uid()") {
		t.Errorf("output still contains auth.uid():\n%s", got)
	}
}

func TestTranslate_AuthUIDWithWhitespace(t *testing.T) {
	// Some SQL dialects prettify to `auth.uid ()` — the regex tolerates
	// whitespace between name and parens.
	in := `CREATE POLICY sel ON orders FOR SELECT USING (id = auth.uid ());`
	got := Translate(in).sql
	if !strings.Contains(got, "auth_uid()") {
		t.Errorf("auth.uid ( ) with whitespace not handled:\n%s", got)
	}
}

func TestTranslate_AuthRole(t *testing.T) {
	in := `CREATE POLICY svc ON orders FOR ALL USING (auth.role() = 'service_role');`
	got := Translate(in).sql
	if !strings.Contains(got, "CASE WHEN public.is_service_role()") {
		t.Errorf("auth.role() → CASE failed:\n%s", got)
	}
	if strings.Contains(got, "auth.role()") {
		t.Errorf("output still contains auth.role():\n%s", got)
	}
}

func TestTranslate_AuthEmail(t *testing.T) {
	in := `CREATE POLICY email_only ON orders FOR ALL USING (auth.email() = email);`
	got := Translate(in).sql
	if !strings.Contains(got, "auth_email()") {
		t.Errorf("auth.email() → auth_email() failed:\n%s", got)
	}
}

func TestTranslate_StorageObjects(t *testing.T) {
	in := `CREATE POLICY p ON storage.objects FOR SELECT USING (bucket_id = 'avatars');`
	got := Translate(in).sql
	if !strings.Contains(got, "storage_objects") {
		t.Errorf("storage.objects → storage_objects failed:\n%s", got)
	}
	if strings.Contains(got, "storage.objects") {
		t.Errorf("output still contains storage.objects:\n%s", got)
	}
}

// ── policy wrap ──────────────────────────────────────────────────────

func TestTranslate_WrapsPolicyUSING(t *testing.T) {
	in := `CREATE POLICY owner_select ON orders FOR SELECT USING (user_id = auth.uid());`
	got := Translate(in).sql
	// Both the atom rewrite AND the wrap fire; the final form is:
	//   USING (public.is_service_role() OR (user_id = auth_uid()))
	if !strings.Contains(got, "public.is_service_role() OR (user_id = auth_uid())") {
		t.Errorf("USING clause not wrapped correctly:\n%s", got)
	}
}

func TestTranslate_WrapsPolicyWithCheck(t *testing.T) {
	in := `CREATE POLICY owner_insert ON orders FOR INSERT WITH CHECK (user_id = auth.uid());`
	got := Translate(in).sql
	if !strings.Contains(got, "public.is_service_role() OR (user_id = auth_uid())") {
		t.Errorf("WITH CHECK clause not wrapped correctly:\n%s", got)
	}
}

func TestTranslate_WrapsBothUSINGAndWITHCHECK(t *testing.T) {
	// UPDATE policies use both.
	in := `CREATE POLICY owner_update ON orders FOR UPDATE USING (user_id = auth.uid()) WITH CHECK (user_id = auth.uid());`
	got := Translate(in).sql
	// Both must be wrapped.
	if strings.Count(got, "public.is_service_role() OR") != 2 {
		t.Errorf("expected both USING and WITH CHECK to wrap (2 total):\n%s", got)
	}
}

func TestTranslate_DoesNotDoubleWrap(t *testing.T) {
	// Running the translator over already-translated output must be
	// idempotent — a body already containing public.is_service_role()
	// stays as-is.
	in := `CREATE POLICY p ON orders FOR SELECT USING (public.is_service_role() OR (user_id = auth_uid()));`
	got := Translate(in).sql
	// Wrap count should stay at 1.
	if strings.Count(got, "public.is_service_role() OR") != 1 {
		t.Errorf("wrap fired twice on already-wrapped input:\n%s", got)
	}
}

func TestTranslate_PolicyBodyWithNestedParens(t *testing.T) {
	// The paren walker must handle nested parens (common in real
	// policies with function calls).
	in := `CREATE POLICY p ON orders FOR SELECT USING ((user_id = auth.uid()) AND (status IN ('open', 'pending')));`
	got := Translate(in).sql
	// Full body should be preserved inside the OR-wrap.
	if !strings.Contains(got, "public.is_service_role() OR ((user_id = auth_uid()) AND (status IN ('open', 'pending')))") {
		t.Errorf("nested parens broke the paren walker:\n%s", got)
	}
}

func TestTranslate_PolicyBodyWithParenInStringLiteral(t *testing.T) {
	// `foo (bar)` inside a string literal must not confuse the paren
	// walker.
	in := `CREATE POLICY p ON orders FOR SELECT USING (label = 'foo(bar)baz');`
	got := Translate(in).sql
	if !strings.Contains(got, "public.is_service_role() OR (label = 'foo(bar)baz')") {
		t.Errorf("string literal parens confused walker:\n%s", got)
	}
}

// ── warnings ─────────────────────────────────────────────────────────

func TestTranslate_WarnsOnAuthJWT(t *testing.T) {
	in := `CREATE POLICY p ON orders FOR SELECT USING (auth.jwt() ->> 'sub' = user_id::text);`
	res := Translate(in)
	found := false
	for _, w := range res.warnings {
		if strings.Contains(w.note, "auth.jwt()") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected auth.jwt() warning; got %+v", res.warnings)
	}
}

func TestTranslate_WarnsOnCustomSchemas(t *testing.T) {
	in := `CREATE FUNCTION my_call() RETURNS void AS $$ SELECT extensions.uuid_generate_v4(); $$ LANGUAGE sql;`
	res := Translate(in)
	found := false
	for _, w := range res.warnings {
		if strings.Contains(w.note, "custom schemas") || strings.Contains(w.note, "may not exist") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected custom-schema warning; got %+v", res.warnings)
	}
}

// ── rewrite bookkeeping ─────────────────────────────────────────────

func TestTranslate_RewriteCounts(t *testing.T) {
	in := `CREATE POLICY owner_select ON orders FOR SELECT USING (user_id = auth.uid());
CREATE POLICY owner_insert ON orders FOR INSERT WITH CHECK (user_id = auth.uid());
CREATE POLICY svc_only ON orders FOR ALL USING (auth.role() = 'service_role');`
	res := Translate(in)
	if res.rewrites["rewrite:auth.uid() → auth_uid()"] != 2 {
		t.Errorf("expected 2 auth.uid() rewrites, got %d", res.rewrites["rewrite:auth.uid() → auth_uid()"])
	}
	if res.rewrites["rewrite:auth.role() → CASE"] != 1 {
		t.Errorf("expected 1 auth.role() rewrite, got %d", res.rewrites["rewrite:auth.role() → CASE"])
	}
	// USING + WITH CHECK + USING = 3 policy wraps.
	if res.rewrites["rewrite:policy body OR'd with is_service_role()"] != 3 {
		t.Errorf("expected 3 policy wraps, got %d", res.rewrites["rewrite:policy body OR'd with is_service_role()"])
	}
}

// ── smoke test on realistic Supabase pg_dump output ─────────────────

func TestTranslate_RealisticPgDump(t *testing.T) {
	in := `--
-- PostgreSQL database dump
--

SET statement_timeout = 0;
SET lock_timeout = 0;
SET client_encoding = 'UTF8';
SET search_path = public;

--
-- Name: orders; Type: TABLE; Schema: public; Owner: postgres
--
CREATE TABLE public.orders (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.orders
    ADD CONSTRAINT orders_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.orders
    ADD CONSTRAINT orders_user_id_fkey FOREIGN KEY (user_id) REFERENCES auth.users(id) ON DELETE CASCADE;

ALTER TABLE public.orders ENABLE ROW LEVEL SECURITY;

CREATE POLICY owner_select ON public.orders FOR SELECT USING ((user_id = auth.uid()));
CREATE POLICY owner_insert ON public.orders FOR INSERT WITH CHECK ((user_id = auth.uid()));
CREATE POLICY owner_update ON public.orders FOR UPDATE USING ((user_id = auth.uid())) WITH CHECK ((user_id = auth.uid()));
CREATE POLICY owner_delete ON public.orders FOR DELETE USING ((user_id = auth.uid()));
`
	res := Translate(in)
	got := res.sql

	// Core assertions.
	if strings.Contains(got, "SET search_path") {
		t.Errorf("SET search_path survived:\n%s", got)
	}
	if strings.Contains(got, "auth.users") {
		t.Errorf("auth.users survived:\n%s", got)
	}
	if strings.Contains(got, "auth.uid()") {
		t.Errorf("auth.uid() survived:\n%s", got)
	}
	// Real DDL survives.
	if !strings.Contains(got, "CREATE TABLE public.orders") {
		t.Errorf("CREATE TABLE dropped:\n%s", got)
	}
	if !strings.Contains(got, "ADD CONSTRAINT orders_pkey") {
		t.Errorf("PK constraint dropped:\n%s", got)
	}
	// Every policy wrapped (5 USING + 1 WITH CHECK on the UPDATE line
	// wait — 3 USING (select/update/delete) + 2 WITH CHECK (insert +
	// update) = 5 total).
	got5 := strings.Count(got, "public.is_service_role() OR")
	if got5 != 5 {
		t.Errorf("expected 5 policy wraps, got %d:\n%s", got5, got)
	}
}
