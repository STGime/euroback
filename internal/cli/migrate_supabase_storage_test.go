package cli

import (
	"strings"
	"testing"
)

// Pure helpers of the storage command: the report's rendering and the
// bucket-name guard. The copy is tested in
// migrate_supabase_storage_copy_test.go.

// TestIsSafeBucketName pins the bucket-name guard: any character
// outside Supabase's bucket-name grammar (`[a-zA-Z0-9-_.]`) is a
// rejection. Allowlist design: adding a new attack char to the
// blocklist can't accidentally miss.
func TestIsSafeBucketName(t *testing.T) {
	safe := []string{"avatars", "user-photos", "photos_2026", "backup.v2", "a"}
	unsafe := []string{
		"",        // empty
		"a`b",     // backtick — closes code fence
		"a\nb",    // newline — breaks fence
		"a\tb",    // control char
		"a\"b",    // quote
		"a'b",     // quote
		"a\\b",    // backslash
		"a$b",     // shell expansion
		"\x00nul", // NUL
		"a b",     // space — breaks arg into two tokens
		"a;b", "a|b", "a&b", "a<b", "a>b",
		"a(b", "a)b", "a{b", "a}b", "a#b",
		"a b", // Unicode line separator
		"a b", // Unicode paragraph separator
		"a‮b", // Right-to-left override (visual reorder)
		"a/b", // path separator
		"日本語", // non-ASCII (outside Supabase grammar)
	}
	for _, s := range safe {
		if !isSafeBucketName(s) {
			t.Errorf("safe name rejected: %q", s)
		}
	}
	for _, s := range unsafe {
		if isSafeBucketName(s) {
			t.Errorf("unsafe name accepted: %q", s)
		}
	}
}

func TestJoinBucketNames_SortedAndCommaJoined(t *testing.T) {
	got := joinBucketNames([]supabaseBucket{
		{name: "widgets"}, {name: "avatars"}, {name: "photos"},
	})
	want := "avatars, photos, widgets"
	if got != want {
		t.Errorf("joinBucketNames = %q, want %q", got, want)
	}
}

func TestJoinBucketNames_Empty(t *testing.T) {
	if got := joinBucketNames(nil); got != "" {
		t.Errorf("empty list should render as empty string, got %q", got)
	}
}

// TestRenderBucketSection covers the pure-Markdown rendering path for
// one bucket. Uses the internal helper (extracted from
// buildStorageReport) so we don't need a live pgx.Conn for full-flow
// coverage. Pins mdEscape usage on the bucket name + MIME list so
// hostile input can't inject structural Markdown (#276 review M).
func TestRenderBucketSection_EscapesHostileBucketName(t *testing.T) {
	// A bucket name with backticks + a fake H1 injection attempt.
	limit := int64(1024 * 1024)
	b := supabaseBucket{
		name:             "avatars`**\n# Fake H1",
		public:           true,
		fileSizeLimit:    &limit,
		allowedMimeTypes: []string{"image/png", "image/jpeg"},
		objectCount:      42,
		totalBytes:       12345,
	}
	got := renderBucketSection(b)
	// Backticks + asterisks in the name must be escaped — the raw
	// header can't inject a Markdown H1.
	if strings.Contains(got, "\n# Fake H1") {
		t.Errorf("hostile bucket name injected an H1:\n%s", got)
	}
	// Positive assertion: the header line must show BOTH escaped forms
	// — the backslash-escaped backtick (from `\``) AND the ↩ mdEscape
	// uses for newlines. `||` (fail if EITHER missing), not `&&`,
	// because a future regression that let raw newlines through would
	// still emit the backtick escape, and `&&` would short-circuit
	// through the tautology round-2 review flagged. (#276 round-3 M.)
	if !strings.Contains(got, `\`+"`") || !strings.Contains(got, "↩") {
		t.Errorf("hostile bucket name didn't route through mdEscape (missing escape markers):\n%s", got)
	}
	// Object count + size still land in the output.
	if !strings.Contains(got, "Objects: 42") {
		t.Errorf("object count missing: %s", got)
	}
	if !strings.Contains(got, "Size:") {
		t.Errorf("size line missing: %s", got)
	}
	// MIME types are listed but escaped — a MIME with a backtick
	// can't close the surrounding code span.
	if !strings.Contains(got, "image/png") {
		t.Errorf("MIME type list missing: %s", got)
	}
}

func TestRenderBucketSection_ZeroBytesWithObjectsWarns(t *testing.T) {
	// Older Supabase projects didn't populate metadata->>'size'.
	// Object count > 0 && total_bytes == 0 must surface a note so
	// the tenant knows the byte total is uninformative.
	b := supabaseBucket{
		name:        "legacy",
		objectCount: 100,
		totalBytes:  0,
	}
	got := renderBucketSection(b)
	if !strings.Contains(got, "metadata->>'size'") {
		t.Errorf("expected a note about metadata->>'size' on 0-byte bucket:\n%s", got)
	}
}

func TestRenderBucketSection_EscapesMimeTypes(t *testing.T) {
	// A hostile MIME type must not inject Markdown — same class as
	// the bucket-name case.
	b := supabaseBucket{
		name:             "hostile",
		allowedMimeTypes: []string{"image/png`, \n# Fake header"},
		objectCount:      1,
	}
	got := renderBucketSection(b)
	if strings.Contains(got, "\n# Fake header") {
		t.Errorf("hostile MIME injected a header:\n%s", got)
	}
}
