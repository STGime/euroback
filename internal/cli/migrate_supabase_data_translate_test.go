package cli

import (
	"strings"
	"testing"
	"time"
)

// #270: pure translator tests. Row emission + SQL escaping is the
// bug-prone part — a hostile `raw_user_meta_data` payload that
// contains `'; DROP TABLE …` must not become an executable statement
// on the target Eurobase project. And a broken topology sort would
// cause silent FK-violation failures at apply time.

// ── sqlLiteral escaping ──────────────────────────────────────────────

func TestSQLLiteral_StringEscaping(t *testing.T) {
	cases := map[string]string{
		"hello":            "'hello'",
		"O'Brien":          "'O''Brien'",
		"'; DROP TABLE t;": "'''; DROP TABLE t;'",
		"":                 "''",
		"line1\nline2":     "'line1\nline2'", // newlines are literal — Postgres handles fine
	}
	for in, want := range cases {
		if got := sqlLiteral(in); got != want {
			t.Errorf("sqlLiteral(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSQLLiteral_NilHandled(t *testing.T) {
	if got := sqlLiteral(nil); got != "NULL" {
		t.Errorf("sqlLiteral(nil) = %q, want NULL", got)
	}
	var ns *string
	if got := sqlLiteral(ns); got != "NULL" {
		t.Errorf("sqlLiteral((*string)(nil)) = %q, want NULL", got)
	}
	var nt *time.Time
	if got := sqlLiteral(nt); got != "NULL" {
		t.Errorf("sqlLiteral((*time.Time)(nil)) = %q, want NULL", got)
	}
}

func TestSQLLiteral_TimeIsRFC3339(t *testing.T) {
	tm := time.Date(2026, 6, 1, 12, 34, 56, 0, time.UTC)
	got := sqlLiteral(tm)
	want := "'2026-06-01T12:34:56Z'::timestamptz"
	if got != want {
		t.Errorf("sqlLiteral(time) = %q, want %q", got, want)
	}
}

func TestSQLLiteral_JsonbLiteralGetsCast(t *testing.T) {
	got := sqlLiteral(jsonbLiteral(`{"name":"alice","role":"admin"}`))
	if !strings.Contains(got, "::jsonb") {
		t.Errorf("jsonbValue missing ::jsonb cast: %q", got)
	}
	if !strings.Contains(got, `"name":"alice"`) {
		t.Errorf("jsonbValue payload lost: %q", got)
	}
}

func TestSQLLiteral_JsonbNormalizesEmpty(t *testing.T) {
	for _, in := range []string{"", "null"} {
		got := sqlLiteral(jsonbLiteral(in))
		if !strings.Contains(got, `'{}'::jsonb`) {
			t.Errorf("jsonbLiteral(%q) not normalized to {}: %q", in, got)
		}
	}
}

func TestSQLLiteral_JsonbEscapesQuotes(t *testing.T) {
	// A hostile JSONB payload — a single quote must be doubled per
	// the SQL string standard. Otherwise the ::jsonb cast fails with
	// a syntax error at best, and executes injected SQL at worst.
	got := sqlLiteral(jsonbLiteral(`{"msg":"it's fine"}`))
	if !strings.Contains(got, `it''s fine`) {
		t.Errorf("jsonbLiteral didn't double quote inside JSON: %q", got)
	}
}

// ── FK topology sort ─────────────────────────────────────────────────

func TestSortTablesByFK_ParentBeforeChild(t *testing.T) {
	tables := []tableRef{
		{name: "orders"}, {name: "users"}, {name: "line_items"},
	}
	edges := []fkEdge{
		{from: "orders", to: "users"},
		{from: "line_items", to: "orders"},
	}
	got, err := sortTablesByFK(tables, edges)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	names := make([]string, len(got))
	for i, t := range got {
		names[i] = t.name
	}
	// users must come before orders; orders before line_items.
	idx := map[string]int{}
	for i, n := range names {
		idx[n] = i
	}
	if idx["users"] > idx["orders"] {
		t.Errorf("users must precede orders: %v", names)
	}
	if idx["orders"] > idx["line_items"] {
		t.Errorf("orders must precede line_items: %v", names)
	}
}

func TestSortTablesByFK_DeterministicWhenIndependent(t *testing.T) {
	tables := []tableRef{
		{name: "widgets"}, {name: "gadgets"}, {name: "sprockets"},
	}
	// No edges — order should be lexicographic.
	got, err := sortTablesByFK(tables, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"gadgets", "sprockets", "widgets"}
	for i, tr := range got {
		if tr.name != want[i] {
			t.Errorf("independent tables not sorted lexicographically: %v", got)
			break
		}
	}
}

func TestSortTablesByFK_SelfReferenceHandled(t *testing.T) {
	// A tree structure that self-references (employees.manager_id).
	// Self-reference must NOT prevent the table from being emitted.
	tables := []tableRef{{name: "employees"}}
	edges := []fkEdge{{from: "employees", to: "employees"}}
	got, err := sortTablesByFK(tables, edges)
	if err != nil {
		t.Fatalf("self-ref should not error: %v", err)
	}
	if len(got) != 1 || got[0].name != "employees" {
		t.Errorf("self-reference blocked topology sort: %v", got)
	}
}

// #275 review H #7: a real FK cycle must error loudly rather than
// silently emit a wrong order — otherwise the emitted INSERTs
// apply-time fail on FK violation with no path back except re-run.
func TestSortTablesByFK_CycleErrors(t *testing.T) {
	tables := []tableRef{{name: "a"}, {name: "b"}, {name: "c"}}
	edges := []fkEdge{
		{from: "a", to: "b"},
		{from: "b", to: "a"},
	}
	_, err := sortTablesByFK(tables, edges)
	if err == nil {
		t.Fatal("expected an error on FK cycle, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error should mention the cycle: %v", err)
	}
	// The cycle members should be named so the tenant knows what to
	// fix on Supabase.
	if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "b") {
		t.Errorf("error should name cycle members: %v", err)
	}
}

func TestSortTablesByFK_EdgesToUnknownTablesIgnored(t *testing.T) {
	// An FK to a table outside our input set (e.g. auth.users, or a
	// dropped table) must not crash the sort.
	tables := []tableRef{{name: "orders"}}
	edges := []fkEdge{
		{from: "orders", to: "auth_users"},
		{from: "orphan", to: "nowhere"},
	}
	got, err := sortTablesByFK(tables, edges)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].name != "orders" {
		t.Errorf("unknown-table edges corrupted sort: %v", got)
	}
}

// ── emitInsertBatch shape ────────────────────────────────────────────

func TestEmitInsertBatch_EmptyBatchIsNoOp(t *testing.T) {
	var buf strings.Builder
	emitInsertBatch(&buf, "orders", []string{"id", "name"}, nil)
	if buf.Len() != 0 {
		t.Errorf("empty batch wrote %q", buf.String())
	}
}

func TestEmitInsertBatch_MultiRowShape(t *testing.T) {
	var buf strings.Builder
	emitInsertBatch(&buf, "orders",
		[]string{"id", "email"},
		[][]interface{}{
			{"u1", "alice@example.com"},
			{"u2", "bob@example.com"},
		})
	out := buf.String()
	if !strings.Contains(out, `INSERT INTO "orders" ("id", "email") VALUES`) {
		t.Errorf("INSERT header shape wrong:\n%s", out)
	}
	// Two rows, one statement, terminated with a single `;`.
	if strings.Count(out, ";") != 1 {
		t.Errorf("expected exactly one semicolon per batch:\n%s", out)
	}
	if !strings.Contains(out, "'alice@example.com'") ||
		!strings.Contains(out, "'bob@example.com'") {
		t.Errorf("row values missing:\n%s", out)
	}
}

// ── auth translation ────────────────────────────────────────────────

func TestTranslateAuthUser_HappyPath(t *testing.T) {
	email := "alice@example.com"
	pw := "$2b$12$abcdefghijklmnopqrstuv" // bcrypt-shape
	confirmed := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	in := supabaseUser{
		ID:                "8e6a6b74-5a5f-45d2-9c2f-2d3a5b8e1a1c",
		Email:             &email,
		EncryptedPassword: &pw,
		EmailConfirmedAt:  &confirmed,
		RawUserMetaData:   `{"name":"alice"}`,
		CreatedAt:         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:         &updated,
	}
	got, notes := translateAuthUser(in)
	if len(notes) != 0 {
		t.Errorf("unexpected notes on happy path: %v", notes)
	}
	if got.ID != in.ID {
		t.Errorf("UUID not preserved: got %q want %q", got.ID, in.ID)
	}
	if got.PasswordHash == nil || *got.PasswordHash != pw {
		t.Errorf("password_hash lost")
	}
	if got.Metadata != `{"name":"alice"}` {
		t.Errorf("metadata not preserved: got %q", got.Metadata)
	}
	if got.UpdatedAt != updated {
		t.Errorf("updated_at not preserved: got %v", got.UpdatedAt)
	}
}

func TestTranslateAuthUser_NoPasswordEmitsNote(t *testing.T) {
	email := "oauth@example.com"
	in := supabaseUser{
		ID:              "8e6a6b74-5a5f-45d2-9c2f-2d3a5b8e1a1c",
		Email:           &email,
		RawUserMetaData: "{}",
		CreatedAt:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	_, notes := translateAuthUser(in)
	found := false
	for _, n := range notes {
		if strings.Contains(n, "no password") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'no password' note; got %v", notes)
	}
}

func TestTranslateAuthUser_NullMetadataNormalized(t *testing.T) {
	// Supabase sometimes stores JSONB `null` (not empty). Translator
	// normalizes to `{}` so the Eurobase INSERT doesn't set metadata
	// to the JSONB scalar-null (which most application code doesn't
	// handle).
	email := "n@x"
	for _, in := range []string{"", "null"} {
		u := supabaseUser{
			ID:              "8e6a6b74-5a5f-45d2-9c2f-2d3a5b8e1a1c",
			Email:           &email,
			RawUserMetaData: in,
			CreatedAt:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		}
		got, _ := translateAuthUser(u)
		if got.Metadata != "{}" {
			t.Errorf("metadata(%q) → %q, want {}", in, got.Metadata)
		}
	}
}

func TestTranslateAuthUser_BannedUntilMapsToBannedAt(t *testing.T) {
	email := "banned@example.com"
	banned := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	in := supabaseUser{
		ID:              "8e6a6b74-5a5f-45d2-9c2f-2d3a5b8e1a1c",
		Email:           &email,
		BannedUntil:     &banned,
		RawUserMetaData: "{}",
		CreatedAt:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	got, _ := translateAuthUser(in)
	if got.BannedAt == nil || !got.BannedAt.Equal(banned) {
		t.Errorf("banned_until → banned_at mapping broken: %v", got.BannedAt)
	}
}

func TestTranslateAuthIdentity_HappyPath(t *testing.T) {
	last := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	in := supabaseIdentity{
		ID:           "id-1",
		UserID:       "user-uuid-1",
		Provider:     "google",
		ProviderID:   "google-sub-123",
		IdentityData: `{"email":"a@x","name":"Alice"}`,
		LastSignInAt: &last,
		CreatedAt:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	got := translateAuthIdentity(in)
	if got.UserID != in.UserID {
		t.Errorf("user_id lost: %q", got.UserID)
	}
	if got.Provider != "google" {
		t.Errorf("provider lost: %q", got.Provider)
	}
	if got.ProviderUserID != "google-sub-123" {
		t.Errorf("provider_user_id lost: %q", got.ProviderUserID)
	}
	if got.IdentityData != in.IdentityData {
		t.Errorf("identity_data lost: %q", got.IdentityData)
	}
	// UpdatedAt defaults to CreatedAt when source is nil.
	if !got.UpdatedAt.Equal(in.CreatedAt) {
		t.Errorf("updated_at not defaulted to created_at: %v", got.UpdatedAt)
	}
}

func TestTranslateAuthIdentity_NullIdentityDataNormalized(t *testing.T) {
	in := supabaseIdentity{
		UserID:       "u1",
		Provider:     "github",
		ProviderID:   "gh-1",
		IdentityData: "null",
		CreatedAt:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	got := translateAuthIdentity(in)
	if got.IdentityData != "{}" {
		t.Errorf("null identity_data not normalized: %q", got.IdentityData)
	}
}

// ── end-to-end row emission shape ────────────────────────────────────

func TestUserRowAsValues_ColumnOrderMatchesAuthUserColumns(t *testing.T) {
	// The tuple order must match authUserColumns exactly, or the
	// INSERT writes values into the wrong columns silently.
	email := "a@x"
	u := eurobaseUserRow{
		ID:        "u1",
		Email:     &email,
		Metadata:  `{"k":"v"}`,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	}
	vals := userRowAsValues(u)
	if len(vals) != len(authUserColumns) {
		t.Fatalf("value count %d != column count %d", len(vals), len(authUserColumns))
	}
	if vals[0] != "u1" {
		t.Errorf("first value must be id, got %v", vals[0])
	}
	// Position of metadata in authUserColumns — must be a jsonbValue.
	metaIdx := -1
	for i, c := range authUserColumns {
		if c == "metadata" {
			metaIdx = i
		}
	}
	if _, ok := vals[metaIdx].(jsonbValue); !ok {
		t.Errorf("metadata slot must be jsonbValue, got %T", vals[metaIdx])
	}
}

// ── #275 review fixes ────────────────────────────────────────────────

// TestSQLLiteral_TypedLiteralEmitsCast pins ship-blocker #3/#4:
// numeric / interval / inet columns come across as strings (the CLI
// casts them to text at SELECT time) and must emit as
// `'value'::type` so Postgres re-parses on insert. Without this,
// silent apply-time failure ("column is of type numeric but
// expression is of type text").
func TestSQLLiteral_TypedLiteralEmitsCast(t *testing.T) {
	cases := []struct {
		v    typedLiteral
		want string
	}{
		{typedLiteral{value: "123.45", pgType: "numeric"}, `'123.45'::numeric`},
		{typedLiteral{value: "1 day", pgType: "interval"}, `'1 day'::interval`},
		{typedLiteral{value: "10.0.0.0/24", pgType: "cidr"}, `'10.0.0.0/24'::cidr`},
		{typedLiteral{value: "{1,2,3}", pgType: "integer[]"}, `'{1,2,3}'::integer[]`},
		// Hostile value with a `'` — must double-escape.
		{typedLiteral{value: "it's", pgType: "text"}, `'it''s'::text`},
	}
	for _, c := range cases {
		if got := sqlLiteral(c.v); got != c.want {
			t.Errorf("sqlLiteral(%+v) = %q, want %q", c.v, got, c.want)
		}
	}
}

// TestMergeUserAndAppMetadata_KeepsUserAtTopLevel pins H #6:
// raw_app_meta_data used to be silently discarded. Now it nests
// under `app_metadata` so provider / role / tenant hints survive.
func TestMergeUserAndAppMetadata_KeepsUserAtTopLevel(t *testing.T) {
	got := mergeUserAndAppMetadata(
		`{"display_name":"alice","avatar_url":"https://x/a.png"}`,
		`{"provider":"google","providers":["google"],"role":"admin"}`,
	)
	// User-meta stays at the top level (existing app code keeps working).
	if !strings.Contains(got, `"display_name":"alice"`) {
		t.Errorf("user_metadata display_name lost: %s", got)
	}
	// App-meta nests under app_metadata (no field silently dropped).
	if !strings.Contains(got, `"app_metadata":`) {
		t.Errorf("app_metadata key missing: %s", got)
	}
	if !strings.Contains(got, `"role":"admin"`) {
		t.Errorf("app_metadata contents lost: %s", got)
	}
}

func TestMergeUserAndAppMetadata_EmptyInputs(t *testing.T) {
	cases := []struct {
		user, app string
		want      string
	}{
		{"", "", "{}"},
		{"null", "null", "{}"},
		{"{}", "{}", "{}"},
		{`{"k":"v"}`, "", `{"k":"v"}`},
		{"", `{"provider":"google"}`, `{"app_metadata":{"provider":"google"}}`},
	}
	for _, c := range cases {
		got := mergeUserAndAppMetadata(c.user, c.app)
		if got != c.want {
			t.Errorf("merge(%q,%q) = %q, want %q", c.user, c.app, got, c.want)
		}
	}
}

func TestMergeUserAndAppMetadata_NonObjectPreserved(t *testing.T) {
	// If Supabase stored a scalar/array in metadata (odd but happens),
	// nest under `_user_metadata_source` so we don't silently drop it.
	got := mergeUserAndAppMetadata(`["a","b"]`, `{"role":"admin"}`)
	if !strings.Contains(got, `_user_metadata_source`) {
		t.Errorf("non-object user_metadata dropped: %s", got)
	}
	if !strings.Contains(got, `"role":"admin"`) {
		t.Errorf("app_metadata lost when user_metadata was non-object: %s", got)
	}
}

// TestTranslateAuthUser_MergesAppMetadata is the end-to-end pin —
// translate must produce metadata that includes both user + app
// meta (H #6).
func TestTranslateAuthUser_MergesAppMetadata(t *testing.T) {
	email := "a@x"
	in := supabaseUser{
		ID:              "u1",
		Email:           &email,
		RawUserMetaData: `{"display_name":"alice"}`,
		RawAppMetaData:  `{"provider":"google","providers":["google"]}`,
		CreatedAt:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	got, _ := translateAuthUser(in)
	if !strings.Contains(got.Metadata, `"display_name":"alice"`) {
		t.Errorf("user meta discarded: %s", got.Metadata)
	}
	if !strings.Contains(got.Metadata, `"app_metadata"`) {
		t.Errorf("app meta discarded (regression on H #6): %s", got.Metadata)
	}
	if !strings.Contains(got.Metadata, `"provider":"google"`) {
		t.Errorf("app_metadata contents lost: %s", got.Metadata)
	}
}

// TestQuoteIdent_EscapesEmbeddedDoubleQuotes pins the SQL-injection
// guard on table/column names. A hostile identifier `"; DROP TABLE t; --`
// would otherwise close the identifier quote early.
func TestQuoteIdent_EscapesEmbeddedDoubleQuotes(t *testing.T) {
	cases := map[string]string{
		`orders`:      `"orders"`,
		`weird"name`:  `"weird""name"`,
		`"; DROP; --`: `"""; DROP; --"`,
	}
	for in, want := range cases {
		if got := quoteIdent(in); got != want {
			t.Errorf("quoteIdent(%q) = %q, want %q", in, got, want)
		}
	}
}
