package cli

import (
	"github.com/google/uuid"
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

// TestSortTablesByFK_CycleReportsOnlyCycleMembers pins #275 round-2
// H #2 — the SCC finder must report ONLY the tables in the cycle,
// not every downstream table that's stuck waiting on the cycle to
// resolve. In `a↔b, c→b`, `c` is stuck but not IN the cycle.
func TestSortTablesByFK_CycleReportsOnlyCycleMembers(t *testing.T) {
	tables := []tableRef{
		{name: "a"}, {name: "b"}, {name: "c"}, {name: "d"},
	}
	edges := []fkEdge{
		{from: "a", to: "b"},
		{from: "b", to: "a"},
		{from: "c", to: "b"}, // c waits on b but is not IN the cycle
		{from: "d", to: "c"}, // d waits on c, still not in the cycle
	}
	_, err := sortTablesByFK(tables, edges)
	if err == nil {
		t.Fatal("expected an error on FK cycle, got nil")
	}
	msg := err.Error()
	// The two-node cycle must be named.
	if !strings.Contains(msg, "a") || !strings.Contains(msg, "b") {
		t.Errorf("error should name cycle members a + b: %v", msg)
	}
	// Downstream `c` and `d` must NOT be reported as cycle members —
	// they're victims, not perpetrators. Reporting them would
	// mislead the tenant into "fixing" something that isn't broken.
	if strings.Contains(msg, "[a b c") || strings.Contains(msg, "c d") {
		t.Errorf("error mislabels downstream victims as cycle members: %v", msg)
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

var testNow = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

// A bcrypt hash in the shape Supabase stores (cost 10).
const testBcryptHash = "$2a$10$7EqJtq98hPqEX7fNZaFWoOhi5BWX4Z1Z3MZ5uG1h1uG1h1uG1h1uG"

func TestTranslateAuthUser_HappyPath(t *testing.T) {
	email := "alice@example.com"
	pw := testBcryptHash
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
	got, flags := translateAuthUser(in, testNow)
	if flags != (userFlags{}) {
		t.Errorf("unexpected flags on happy path: %+v", flags)
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

func TestTranslateAuthUser_Passwords(t *testing.T) {
	email := "u@example.com"
	high := "$2a$14$7EqJtq98hPqEX7fNZaFWoOhi5BWX4Z1Z3MZ5uG1h1uG1h1uG1h1uG"
	notBcrypt := "$argon2id$v=19$m=65536,t=3,p=4$c29tZXNhbHQ$RdescudvJCsgt3ub+b+dWRWJTmaaJObG"
	empty := ""
	ok := testBcryptHash
	for _, c := range []struct {
		name        string
		pw          *string
		kept        bool
		noPw, dropd bool
	}{
		{"bcrypt cost 10", &ok, true, false, false},
		{"no password", nil, false, true, false},
		{"empty string", &empty, false, true, false},
		{"bcrypt cost 14 (sign-in would refuse it)", &high, false, false, true},
		{"not bcrypt", &notBcrypt, false, false, true},
	} {
		got, flags := translateAuthUser(supabaseUser{ID: "u1", Email: &email, EncryptedPassword: c.pw, RawUserMetaData: "{}"}, testNow)
		if (got.PasswordHash != nil) != c.kept || flags.noPassword != c.noPw || flags.passwordDropped != c.dropd {
			t.Errorf("%s: hash kept=%v flags=%+v", c.name, got.PasswordHash != nil, flags)
		}
	}
}

func TestTranslateAuthUser_EmailPhoneAndProfile(t *testing.T) {
	email, phone := "  Alice@Example.ORG ", "4915112345678"
	got, flags := translateAuthUser(supabaseUser{
		ID: "u1", Email: &email, Phone: &phone,
		RawUserMetaData: `{"full_name":"Alice A","picture":"https://x.example/a.png"}`,
	}, testNow)
	if got.Email == nil || *got.Email != "alice@example.org" {
		t.Errorf("email = %v, want lower case and trimmed", got.Email)
	}
	if got.Phone == nil || *got.Phone != "+4915112345678" {
		t.Errorf("phone = %v, want E.164", got.Phone)
	}
	if got.DisplayName == nil || *got.DisplayName != "Alice A" || got.AvatarURL == nil || *got.AvatarURL != "https://x.example/a.png" {
		t.Errorf("display_name / avatar_url not taken from the metadata: %v %v", got.DisplayName, got.AvatarURL)
	}
	if flags.noLogin {
		t.Error("flagged as having no login")
	}
	plus := "+33612345678"
	if got, _ := translateAuthUser(supabaseUser{ID: "u2", Phone: &plus, RawUserMetaData: "{}"}, testNow); *got.Phone != plus {
		t.Errorf("phone with + changed: %s", *got.Phone)
	}
	if _, flags := translateAuthUser(supabaseUser{ID: "u3", RawUserMetaData: "{}"}, testNow); !flags.noLogin {
		t.Error("a user without email and phone must be flagged")
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
		got, _ := translateAuthUser(u, testNow)
		if got.Metadata != "{}" {
			t.Errorf("metadata(%q) → %q, want {}", in, got.Metadata)
		}
	}
}

func TestTranslateAuthUser_Bans(t *testing.T) {
	email := "banned@example.com"
	future, past := testNow.Add(24*time.Hour), testNow.Add(-24*time.Hour)
	got, flags := translateAuthUser(supabaseUser{ID: "u1", Email: &email, BannedUntil: &future, RawUserMetaData: "{}"}, testNow)
	if got.BannedAt == nil || !flags.temporaryBan {
		t.Errorf("a running ban must carry over: banned_at=%v flags=%+v", got.BannedAt, flags)
	}
	// A ban that has run out must not lock the user out for good.
	got, flags = translateAuthUser(supabaseUser{ID: "u1", Email: &email, BannedUntil: &past, RawUserMetaData: "{}"}, testNow)
	if got.BannedAt != nil || flags.temporaryBan {
		t.Errorf("an expired ban was imported as a ban: banned_at=%v", got.BannedAt)
	}
}

func TestPlanAuthImport(t *testing.T) {
	s := func(v string) *string { return &v }
	deleted := testNow.Add(-time.Hour)
	users := []supabaseUser{
		{ID: "alice", Email: s("alice@example.org"), EncryptedPassword: s(testBcryptHash), RawUserMetaData: "{}"},
		{ID: "oauth", Email: s("bob@example.org"), RawUserMetaData: "{}"},
		{ID: "anon", IsAnonymous: true, RawUserMetaData: "{}"},
		{ID: "gone", Email: s("gone@example.org"), DeletedAt: &deleted, RawUserMetaData: "{}"},
		{ID: "sso", Email: s("alice@example.org"), IsSSOUser: true, RawUserMetaData: "{}"},
	}
	identities := []supabaseIdentity{
		{UserID: "alice", Provider: "email", ProviderID: "alice"},
		{UserID: "oauth", Provider: "google", ProviderID: "sub-1"},
		{UserID: "gone", Provider: "github", ProviderID: "gh-1"},
		{UserID: "oauth", Provider: "azure", ProviderID: "ms-1", ID: "0b0e7c5e-7a3c-4c57-9a52-2f1b1d0f5a11"},
		{UserID: "oauth", Provider: "linkedin_oidc", ProviderID: "li-1"},
		{UserID: "oauth", Provider: "discord", ProviderID: "d-1"},
	}
	plan, err := planAuthImport(users, identities, testNow)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, u := range plan.users {
		ids = append(ids, u.ID)
	}
	// The anonymous user is kept (app rows refer to it); the deleted
	// user and the SSO user (same email as alice) are not.
	if strings.Join(ids, ",") != "alice,oauth,anon" {
		t.Errorf("imported users = %v", ids)
	}
	// Only the real OAuth link of an imported user.
	// Supabase's provider names become Eurobase's; a provider Eurobase
	// doesn't offer is left out and named in a note.
	var got []string
	for _, l := range plan.identities {
		if _, err := uuid.Parse(l.ID); err != nil {
			t.Errorf("link %s has no usable id: %q", l.Provider, l.ID)
		}
		got = append(got, l.Provider+":"+l.ProviderUserID)
	}
	if strings.Join(got, ",") != "google:sub-1,microsoft:ms-1,linkedin:li-1" {
		t.Errorf("imported links = %v", got)
	}
	if plan.identities[1].ID != "0b0e7c5e-7a3c-4c57-9a52-2f1b1d0f5a11" {
		t.Errorf("a uuid id from Supabase should be kept, got %s", plan.identities[1].ID)
	}
	if n := strings.Join(plan.notes, "\n"); !strings.Contains(n, "discord (1)") || !strings.Contains(n, "3 OAuth link(s) imported") {
		t.Errorf("notes lack the provider notes:\n%s", n)
	}
	notes := strings.Join(plan.notes, "\n")
	for _, want := range []string{"1 user(s) deleted", "1 SSO user(s)", "1 user(s) have neither email nor phone", "1 user(s) have no password", "1 OAuth link(s) belong to users that are not imported"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, notes)
		}
	}
}

func TestPlanAuthImport_DuplicateEmailIsAnError(t *testing.T) {
	a, b := "Same@example.org", "same@example.org"
	_, err := planAuthImport([]supabaseUser{
		{ID: "u1", Email: &a, RawUserMetaData: "{}"},
		{ID: "u2", Email: &b, RawUserMetaData: "{}"},
	}, nil, testNow)
	if err == nil || !strings.Contains(err.Error(), "u1") || !strings.Contains(err.Error(), "u2") {
		t.Errorf("want an error naming both users, got %v", err)
	}
}

func TestEmitInsert_OverridingSystemValue(t *testing.T) {
	var buf strings.Builder
	emitInsert(&buf, "categories", []string{"id", "name"}, [][]interface{}{{"1", "root"}, {"2", nil}}, true)
	want := "INSERT INTO \"categories\" (\"id\", \"name\") OVERRIDING SYSTEM VALUE VALUES\n  ('1', 'root'),\n  ('2', NULL);\n"
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestSetvalStatement(t *testing.T) {
	if got := setvalStatement(sequenceState{name: "orders_id_seq", lastValue: 42, isCalled: true}); got != "SELECT setval('\"orders_id_seq\"', 42, true);\n" {
		t.Errorf("got %q", got)
	}
	// A sequence that was never used hands out its start value next.
	if got := setvalStatement(sequenceState{name: `odd"name's`, lastValue: 1, isCalled: false}); got != "SELECT setval('\"odd\"\"name''s\"', 1, false);\n" {
		t.Errorf("got %q", got)
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

// TestMergeUserAndAppMetadata_KeepsUserAtTopLevel pins H #6:
// raw_app_meta_data used to be silently discarded. Now it nests
// under `_app_metadata` so provider / role / tenant hints survive.
// (Underscored to avoid colliding with a user-supplied `app_metadata`
// key — #275 round-2 L #7.)
func TestMergeUserAndAppMetadata_KeepsUserAtTopLevel(t *testing.T) {
	got := mergeUserAndAppMetadata(
		`{"display_name":"alice","avatar_url":"https://x/a.png"}`,
		`{"provider":"google","providers":["google"],"role":"admin"}`,
	)
	// User-meta stays at the top level (existing app code keeps working).
	if !strings.Contains(got, `"display_name":"alice"`) {
		t.Errorf("user_metadata display_name lost: %s", got)
	}
	// App-meta nests under _app_metadata (no field silently dropped).
	if !strings.Contains(got, `"_app_metadata":`) {
		t.Errorf("_app_metadata key missing: %s", got)
	}
	if !strings.Contains(got, `"role":"admin"`) {
		t.Errorf("_app_metadata contents lost: %s", got)
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
		{"", `{"provider":"google"}`, `{"_app_metadata":{"provider":"google"}}`},
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
		t.Errorf("_app_metadata lost when user_metadata was non-object: %s", got)
	}
}

// TestMergeUserAndAppMetadata_InvalidUserJSONDoesNotDropAppMeta pins
// #275 round-2 M #3: previously, invalid JSON in user_metadata
// wrapped as json.RawMessage failed the outer json.Marshal, silently
// dropping BOTH user AND app metadata. Now invalid JSON falls back to
// a string wrap so the outer marshal always succeeds.
func TestMergeUserAndAppMetadata_InvalidUserJSONDoesNotDropAppMeta(t *testing.T) {
	// `{{{` is not valid JSON — the old code would json.RawMessage it
	// and blow up on the final Marshal, returning "{}".
	got := mergeUserAndAppMetadata(`{{{ not json`, `{"provider":"google"}`)
	// The invalid user meta must be preserved somewhere (as a string).
	if !strings.Contains(got, `_user_metadata_source`) {
		t.Errorf("invalid user_metadata dropped: %s", got)
	}
	// And crucially, app-meta MUST survive.
	if !strings.Contains(got, `"provider":"google"`) {
		t.Errorf("app_metadata dropped by invalid user_metadata regression: %s", got)
	}
}

// TestMergeUserAndAppMetadata_UserAppMetadataKeyDoesNotClobber pins
// #275 round-2 L #7: if user_metadata legitimately has an
// `app_metadata` top-level key, our injected merge target must not
// silently overwrite it. Using `_app_metadata` (underscored) as our
// key protects that.
func TestMergeUserAndAppMetadata_UserAppMetadataKeyDoesNotClobber(t *testing.T) {
	got := mergeUserAndAppMetadata(
		`{"app_metadata":"legacy-user-value"}`,
		`{"provider":"google"}`,
	)
	if !strings.Contains(got, `"app_metadata":"legacy-user-value"`) {
		t.Errorf("user's legit app_metadata key clobbered by merge: %s", got)
	}
	if !strings.Contains(got, `"_app_metadata":`) {
		t.Errorf("injected _app_metadata key missing: %s", got)
	}
}

// TestTranslateAuthUser_MergesAppMetadata is the end-to-end pin —
// translate must produce metadata that includes both user + app
// meta (H #6). App-meta lands under `_app_metadata` (underscored to
// avoid collision, L #7).
func TestTranslateAuthUser_MergesAppMetadata(t *testing.T) {
	email := "a@x"
	in := supabaseUser{
		ID:              "u1",
		Email:           &email,
		RawUserMetaData: `{"display_name":"alice"}`,
		RawAppMetaData:  `{"provider":"google","providers":["google"]}`,
		CreatedAt:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	got, _ := translateAuthUser(in, testNow)
	if !strings.Contains(got.Metadata, `"display_name":"alice"`) {
		t.Errorf("user meta discarded: %s", got.Metadata)
	}
	if !strings.Contains(got.Metadata, `"_app_metadata"`) {
		t.Errorf("app meta discarded (regression on H #6): %s", got.Metadata)
	}
	if !strings.Contains(got.Metadata, `"provider":"google"`) {
		t.Errorf("_app_metadata contents lost: %s", got.Metadata)
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
