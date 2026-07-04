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
	// `net.http_post` is Supabase's pg_net extension — no Eurobase
	// equivalent, so the translator should flag it (unlike
	// `extensions.uuid_generate_v4()`, which we rewrite outright).
	in := `SELECT net.http_post('https://example.com', '{}'::jsonb);`
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

// #274 review H4: `extensions.uuid_generate_v4()` gets rewritten to
// Eurobase's `public.uuid_generate_v4()` outright (not warned about).
// `extensions.gen_random_uuid()` → bare `gen_random_uuid()`.
func TestTranslate_ExtensionsUUIDRewrite(t *testing.T) {
	in := `CREATE TABLE orders (
    id uuid DEFAULT extensions.uuid_generate_v4() NOT NULL,
    ref uuid DEFAULT extensions.gen_random_uuid() NOT NULL
);`
	got := Translate(in).sql
	if !strings.Contains(got, "public.uuid_generate_v4()") {
		t.Errorf("extensions.uuid_generate_v4() not rewritten to public.uuid_generate_v4():\n%s", got)
	}
	if strings.Contains(got, "extensions.uuid_generate_v4") {
		t.Errorf("original extensions.uuid_generate_v4() survived:\n%s", got)
	}
	if !strings.Contains(got, "DEFAULT gen_random_uuid()") {
		t.Errorf("extensions.gen_random_uuid() not rewritten to gen_random_uuid():\n%s", got)
	}
	if strings.Contains(got, "extensions.gen_random_uuid") {
		t.Errorf("original extensions.gen_random_uuid() survived:\n%s", got)
	}
}

// #274 review B1: `public.<table>` must be stripped so the object lands
// in the tenant schema (the executor pins `search_path`). Function
// calls stay qualified — `public.uuid_generate_v4()` and
// `public.is_service_role()` are Eurobase helpers.
func TestTranslate_StripsPublicQualifier(t *testing.T) {
	in := `CREATE TABLE public.orders (id uuid PRIMARY KEY);
ALTER TABLE ONLY public.orders ADD CONSTRAINT orders_pkey PRIMARY KEY (id);
ALTER TABLE public.orders ENABLE ROW LEVEL SECURITY;
CREATE INDEX orders_user_idx ON public.orders (user_id);
CREATE POLICY p ON public.orders FOR SELECT USING (true);
ALTER TABLE ONLY public.line_items ADD CONSTRAINT fk FOREIGN KEY (order_id) REFERENCES public.orders(id);`
	got := Translate(in).sql
	if strings.Contains(got, "public.orders") {
		t.Errorf("public.orders qualifier survived:\n%s", got)
	}
	if strings.Contains(got, "public.line_items") {
		t.Errorf("public.line_items qualifier survived:\n%s", got)
	}
	// The bare-identifier forms must be present.
	for _, want := range []string{
		"CREATE TABLE orders",
		"ALTER TABLE ONLY orders",
		"ALTER TABLE orders ENABLE",
		"CREATE INDEX orders_user_idx ON orders",
		"CREATE POLICY p ON orders",
		"REFERENCES orders(id)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in output:\n%s", want, got)
		}
	}
}

// Function calls on public.* must NOT get their `public.` stripped —
// they refer to Eurobase's helper functions.
func TestTranslate_KeepsPublicOnFunctionCalls(t *testing.T) {
	in := `CREATE TABLE orders (
    id uuid DEFAULT public.uuid_generate_v4() NOT NULL
);
CREATE POLICY p ON orders FOR SELECT USING (public.is_service_role() OR (user_id = auth_uid()));`
	got := Translate(in).sql
	if !strings.Contains(got, "public.uuid_generate_v4()") {
		t.Errorf("public.uuid_generate_v4() (function call) got stripped:\n%s", got)
	}
	if !strings.Contains(got, "public.is_service_role()") {
		t.Errorf("public.is_service_role() (function call) got stripped:\n%s", got)
	}
}

// #274 review B2: pg_dump pretty-prints long policy bodies across
// multiple lines. Line-by-line wrap missed those; statement-level
// wrap must handle them.
func TestTranslate_WrapsMultiLinePolicyBody(t *testing.T) {
	in := `CREATE POLICY owner_select ON orders
    FOR SELECT
    TO authenticated
    USING (
        (user_id = auth.uid())
        AND (status = 'open')
    );`
	got := Translate(in).sql
	if !strings.Contains(got, "public.is_service_role() OR (") {
		t.Errorf("multi-line policy body not wrapped:\n%s", got)
	}
	// Both original body clauses must be inside the wrap.
	if !strings.Contains(got, "user_id = auth_uid()") {
		t.Errorf("policy body content lost:\n%s", got)
	}
	if !strings.Contains(got, "status = 'open'") {
		t.Errorf("policy body content lost:\n%s", got)
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
	// Any preamble `SET <ident>` (statement_timeout, lock_timeout, …)
	// must be stripped — #274 review M6.
	if strings.Contains(got, "SET statement_timeout") {
		t.Errorf("SET statement_timeout survived (M6):\n%s", got)
	}
	if strings.Contains(got, "SET client_encoding") {
		t.Errorf("SET client_encoding survived (M6):\n%s", got)
	}
	if strings.Contains(got, "auth.users") {
		t.Errorf("auth.users survived:\n%s", got)
	}
	if strings.Contains(got, "auth.uid()") {
		t.Errorf("auth.uid() survived:\n%s", got)
	}
	// Real DDL survives — but WITHOUT the `public.` qualifier
	// (#274 review B1).
	if !strings.Contains(got, "CREATE TABLE orders") {
		t.Errorf("CREATE TABLE dropped:\n%s", got)
	}
	if strings.Contains(got, "public.orders") {
		t.Errorf("public.orders qualifier survived (B1):\n%s", got)
	}
	if !strings.Contains(got, "ADD CONSTRAINT orders_pkey") {
		t.Errorf("PK constraint dropped:\n%s", got)
	}
	// Every policy wrapped: 3 USING (select/update/delete) + 2 WITH CHECK
	// (insert + update) = 5 total.
	got5 := strings.Count(got, "public.is_service_role() OR")
	if got5 != 5 {
		t.Errorf("expected 5 policy wraps, got %d:\n%s", got5, got)
	}
}

// ── #274 re-review fixes: tokenizer safety, JOIN/IF EXISTS, dollar-quotes ─

// TestTranslate_LeavesStringLiteralsAlone pins re-review S2/H1: atom
// rewrites and public-qualifier stripping MUST NOT touch content inside
// single-quoted string literals. A COMMENT/RAISE with `public.orders`
// as text must survive unchanged.
func TestTranslate_LeavesStringLiteralsAlone(t *testing.T) {
	in := `COMMENT ON TABLE orders IS 'legacy of public.orders in the supabase project — auth.uid() gated';
INSERT INTO events (msg) VALUES ('table public.orders was updated by auth.uid()');`
	got := Translate(in).sql
	if !strings.Contains(got, "'legacy of public.orders in the supabase project — auth.uid() gated'") {
		t.Errorf("COMMENT body was rewritten inside a string literal:\n%s", got)
	}
	if !strings.Contains(got, "'table public.orders was updated by auth.uid()'") {
		t.Errorf("INSERT literal was rewritten inside a string literal:\n%s", got)
	}
}

// TestTranslate_LeavesDollarQuotedBodiesAlone pins re-review S1/S2:
// content inside a `$$…$$` or `$tag$…$tag$` block MUST pass through
// unchanged. Statement splitting must not stop at a `;` inside the body.
func TestTranslate_LeavesDollarQuotedBodiesAlone(t *testing.T) {
	in := `CREATE FUNCTION notify_change() RETURNS trigger AS $$
BEGIN
    PERFORM 1;
    RAISE NOTICE 'auth.uid() = %', auth.uid();
    PERFORM 2;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TABLE orders (id uuid PRIMARY KEY);`
	got := Translate(in).sql

	// Body content must be untouched — including the `auth.uid()` inside
	// the RAISE NOTICE string literal.
	if !strings.Contains(got, "'auth.uid() = %'") {
		t.Errorf("string literal inside dollar-quote body was rewritten:\n%s", got)
	}
	// The dollar-quote body's `;` must NOT split the statement — the
	// CREATE TABLE that follows must land intact.
	if !strings.Contains(got, "CREATE TABLE orders (id uuid PRIMARY KEY);") {
		t.Errorf("dollar-quote body's `;` split the statement:\n%s", got)
	}
	// The full function-body sequence should still be present in one
	// piece (all four PERFORM/RAISE lines).
	for _, want := range []string{
		"PERFORM 1;",
		"PERFORM 2;",
		"RETURN NEW;",
		"$$ LANGUAGE plpgsql;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("dollar-quote body content lost (%q):\n%s", want, got)
		}
	}
	// The dollar-quoted function-call auth.uid() outside the string
	// literal — is that inside the body? Yes: `PERFORM auth.uid()`? No,
	// the input has `auth.uid()` bare in `RAISE NOTICE 'auth.uid() = %', auth.uid()`.
	// The bare one is inside the dollar-quote body → also untouched.
	// So the rewritten output must still contain `auth.uid()` verbatim.
	if !strings.Contains(got, "auth.uid()") {
		t.Errorf("bare auth.uid() inside dollar-quote body was rewritten:\n%s", got)
	}
}

// TestTranslate_LeavesLineCommentsAlone pins re-review S2: `--` line
// comments are literals; content inside must survive.
func TestTranslate_LeavesLineCommentsAlone(t *testing.T) {
	in := `-- migrate public.orders from supabase; uses auth.uid() heavily
CREATE TABLE orders (id uuid PRIMARY KEY);`
	got := Translate(in).sql
	if !strings.Contains(got, "-- migrate public.orders from supabase; uses auth.uid() heavily") {
		t.Errorf("line comment body was rewritten:\n%s", got)
	}
}

// TestTranslate_LeavesBlockCommentsAlone pins re-review S2: `/* */`
// block comments are literals. Also covers the split-across-lines case.
func TestTranslate_LeavesBlockCommentsAlone(t *testing.T) {
	in := `/* pinned: public.orders reference is intentional
   until the RLS rewrite for auth.uid() ships */
CREATE TABLE orders (id uuid PRIMARY KEY);`
	got := Translate(in).sql
	if !strings.Contains(got, "public.orders reference is intentional") {
		t.Errorf("block comment body was rewritten:\n%s", got)
	}
	if !strings.Contains(got, "auth.uid() ships") {
		t.Errorf("block comment continuation was rewritten:\n%s", got)
	}
}

// TestTranslate_StripsJoinAndIfExists pins re-review H2 and H3.
// pg_dump emits `CREATE VIEW public.v AS SELECT … JOIN public.other …`
// and, with --clean, `DROP TABLE IF EXISTS public.orders`. Both must
// have their `public.` stripped.
func TestTranslate_StripsJoinAndIfExists(t *testing.T) {
	in := `CREATE VIEW v AS SELECT o.id FROM orders o JOIN public.line_items li ON li.order_id = o.id;
DROP TABLE IF EXISTS public.orders;
ALTER TABLE IF EXISTS public.orders ADD COLUMN foo text;`
	got := Translate(in).sql
	if strings.Contains(got, "public.line_items") {
		t.Errorf("JOIN public.line_items not stripped (H2):\n%s", got)
	}
	if !strings.Contains(got, "JOIN line_items") {
		t.Errorf("JOIN <name> missing after strip:\n%s", got)
	}
	if strings.Contains(got, "DROP TABLE IF EXISTS public.orders") {
		t.Errorf("DROP TABLE IF EXISTS public.orders not stripped (H3):\n%s", got)
	}
	if strings.Contains(got, "ALTER TABLE IF EXISTS public.orders") {
		t.Errorf("ALTER TABLE IF EXISTS public.orders not stripped (H3):\n%s", got)
	}
}

// TestTranslate_PrettyPrintedMultiLinePolicy pins re-review M3 —
// pg_dump's real multi-line policy shape (USING on its own line,
// body indented, closing paren on its own line).
func TestTranslate_PrettyPrintedMultiLinePolicy(t *testing.T) {
	in := `CREATE POLICY owner_select ON public.orders
    AS PERMISSIVE
    FOR SELECT
    TO authenticated
    USING (
        (user_id = auth.uid())
        AND (org_id = current_setting('app.org_id')::uuid)
    );`
	got := Translate(in).sql
	// Body wrapped despite spanning six lines.
	if !strings.Contains(got, "public.is_service_role() OR (") {
		t.Errorf("multi-line pretty-printed policy body not wrapped:\n%s", got)
	}
	// public. qualifier stripped from the table name.
	if strings.Contains(got, "public.orders") {
		t.Errorf("public.orders survived on multi-line policy:\n%s", got)
	}
	// Body content preserved.
	for _, want := range []string{
		"user_id = auth_uid()",
		"current_setting('app.org_id')::uuid",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("policy body content lost (%q):\n%s", want, got)
		}
	}
}

// TestTranslate_StripsSetWithStringArg confirms `SET client_encoding =
// 'UTF8';` — where the line has both a keyword-prefix in code AND a
// tail literal — still strips cleanly. Regression pin for the
// `headIsCode` gate in translatePass1.
func TestTranslate_StripsSetWithStringArg(t *testing.T) {
	in := `SET client_encoding = 'UTF8';
CREATE TABLE orders (id uuid PRIMARY KEY);`
	got := Translate(in).sql
	if strings.Contains(got, "'UTF8'") {
		t.Errorf("SET line's trailing literal orphaned:\n%s", got)
	}
	if strings.Contains(got, "SET client_encoding") {
		t.Errorf("SET client_encoding not stripped:\n%s", got)
	}
	if !strings.Contains(got, "CREATE TABLE orders") {
		t.Errorf("CREATE TABLE dropped:\n%s", got)
	}
}
