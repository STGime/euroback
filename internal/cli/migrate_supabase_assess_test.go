package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// #268: pure-logic helpers get pinned so future changes to the grader
// or URL-redactor don't silently drift. The DB-touching enumerators
// (assessTables, assessPolicies, ...) are exercised by manual runs
// against real Supabase projects — no reasonable unit test for those.

func TestRedactURL_StripsPassword(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{
			"postgres://user:supersecret@db.example.com:5432/postgres",
			"postgres://user:***@db.example.com:5432/postgres",
		},
		{
			// `postgresql://` scheme variant (both aliases accepted
			// by the redactor). Password uses percent-encoded `@`
			// as real postgres URLs must — a literal `@` in the
			// password would be a URL-parsing footgun regardless
			// of what our redactor does.
			"postgresql://user:p%40ss@host/db",
			"postgresql://user:***@host/db",
		},
		{
			// No password → returned as-is (nothing to redact).
			"postgres://user@host/db",
			"postgres://user@host/db",
		},
		{
			// Non-postgres URL → returned as-is (grader accepts
			// custom sources in a future release).
			"mysql://user:pw@host/db",
			"mysql://user:pw@host/db",
		},
		{
			// Empty → returned as-is.
			"",
			"",
		},
	}
	for _, c := range cases {
		if got := redactURL(c.in); got != c.want {
			t.Errorf("redactURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGradePolicy_CommonCases(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := []struct {
		name       string
		using      *string
		withCheck  *string
		wantGrade  string
		wantSubstr string // must appear in the returned note
	}{
		{
			"standard auth.uid()",
			str("(auth.uid() = user_id)"),
			nil,
			gradeOK,
			"auth.uid()",
		},
		{
			"auth.role() service check",
			str("(auth.role() = 'service_role')"),
			nil,
			gradeOK,
			"auth.role()",
		},
		{
			"jwt app_metadata read",
			str("((auth.jwt() -> 'app_metadata' ->> 'org_id')::uuid = org_id)"),
			nil,
			gradeWarn,
			"app_metadata",
		},
		{
			"jwt user_metadata read",
			str("((auth.jwt() -> 'user_metadata' ->> 'tenant') = tenant)"),
			nil,
			gradeWarn,
			"user_metadata",
		},
		{
			"auth.email() usage",
			str("(auth.email() = email)"),
			nil,
			gradeOK,
			"auth.email()",
		},
		{
			"empty policy body",
			nil,
			nil,
			gradeWarn,
			"empty",
		},
		{
			"unrelated body (no auth refs)",
			str("(tenant_id = current_setting('app.tenant_id'))"),
			nil,
			gradeOK,
			"as-is",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			note, grade := gradePolicy(c.using, c.withCheck)
			if grade != c.wantGrade {
				t.Errorf("grade = %q, want %q (note: %q)", grade, c.wantGrade, note)
			}
			if !strings.Contains(strings.ToLower(note), strings.ToLower(c.wantSubstr)) {
				t.Errorf("note %q missing substring %q", note, c.wantSubstr)
			}
		})
	}
}

func TestFormatCount(t *testing.T) {
	cases := map[int64]string{
		0:         "0",
		1:         "1",
		999:       "999",
		1000:      "1.0k",
		1500:      "1.5k",
		999_999:   "1000.0k", // just under the M threshold
		1_000_000: "1.0M",
		2_500_000: "2.5M",
	}
	for n, want := range cases {
		if got := formatCount(n); got != want {
			t.Errorf("formatCount(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	cases := map[int64]string{
		0:                    "0 B",
		512:                  "512 B",
		1024:                 "1.0 KB",
		1_500_000:            "1.4 MB",
		1_073_741_824:        "1.00 GB",
	}
	for n, want := range cases {
		if got := formatBytes(n); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestWriteReport_ShapeAndOrdering confirms the report shape: header
// with source + timestamp, blocker summary if any, then the fixed
// section order (Tables → Policies → Auth users → Storage → Functions
// → Extensions), then the next-step footer.
func TestWriteReport_ShapeAndOrdering(t *testing.T) {
	r := &report{
		sourceURL:   "postgres://user:***@db.example.com/postgres",
		targetHint:  "run `eurobase migrate supabase schema` next",
		generatedAt: time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC),
		tables:      []item{{name: "public.orders", grade: gradeOK, note: "1.2M rows"}},
		policies:    []item{{name: "public.orders :: owner_select", grade: gradeOK, note: "standard auth.uid()"}},
		authUsers:   []item{{name: "auth.users", grade: gradeOK, note: "5.4k users"}},
		storage:     []item{{name: "avatars", grade: gradeWarn, note: "manual review"}},
		functions:   []item{{name: "webhook-stripe", grade: gradeOK, note: "clean port"}},
		extensions:  []item{{name: "pg_graphql", grade: gradeBlocker, note: "not supported"}},
		blockers:    []item{{name: "pg_graphql", grade: gradeBlocker, note: "not supported"}},
	}

	var buf bytes.Buffer
	if err := writeReport(&buf, r); err != nil {
		t.Fatalf("writeReport: %v", err)
	}
	out := buf.String()

	// Header
	if !strings.Contains(out, "Supabase → Eurobase migration report") {
		t.Errorf("missing report title")
	}
	if !strings.Contains(out, "postgres://user:***@") {
		t.Errorf("source URL missing from report — should be the redacted one")
	}
	// Blocker summary is present + counts right
	if !strings.Contains(out, "Blockers — 1 found") {
		t.Errorf("blocker summary missing or wrong count")
	}
	// Section order — each must appear, and Tables before Extensions
	sections := []string{
		"## Tables", "## RLS policies", "## Auth users + providers",
		"## Storage", "## Edge functions", "## Postgres extensions",
	}
	lastIdx := -1
	for _, s := range sections {
		idx := strings.Index(out, s)
		if idx == -1 {
			t.Errorf("section %q missing from report", s)
			continue
		}
		if idx < lastIdx {
			t.Errorf("section %q appears before an earlier section — ordering broken", s)
		}
		lastIdx = idx
	}
	// Next-step footer
	if !strings.Contains(out, "eurobase migrate supabase schema") {
		t.Errorf("next-step hint missing from footer")
	}
}

func TestWriteReport_NoBlockersHappyPath(t *testing.T) {
	r := &report{
		sourceURL:   "postgres://user@host/db",
		targetHint:  "next step",
		generatedAt: time.Now().UTC(),
		tables:      []item{{name: "public.orders", grade: gradeOK, note: "clean"}},
	}
	var buf bytes.Buffer
	if err := writeReport(&buf, r); err != nil {
		t.Fatalf("writeReport: %v", err)
	}
	if !strings.Contains(buf.String(), "No blockers") {
		t.Errorf("no-blockers happy-path summary missing")
	}
}
