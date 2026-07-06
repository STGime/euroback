package cli

import (
	"strings"
	"testing"
)

// #271: pure-logic helpers get pinned so the report shape + rclone
// command shape can't silently drift. The DB-touching enumerators
// (listSupabaseBuckets, storageObjectTotals) exercise a live
// Supabase project — no reasonable unit test.

func TestRcloneCommandFor_Shape(t *testing.T) {
	b := supabaseBucket{name: "avatars"}
	got := rcloneCommandFor(b)
	// Must reference both remotes with the bucket name and a
	// trailing `/` — the standard rclone convention for "sync the
	// whole bucket root". This matches the layout Eurobase's
	// multi-bucket follow-up will consume (#276 review H).
	if !strings.Contains(got, "supabase_src:avatars/") {
		t.Errorf("source ref shape wrong (want supabase_src:avatars/): %q", got)
	}
	if !strings.Contains(got, "eurobase_dst:avatars/") {
		t.Errorf("dest ref shape wrong (want eurobase_dst:avatars/): %q", got)
	}
	// --checksum is the idempotency guarantor — without it, Supabase's
	// mtime-zero shim would trigger a full re-copy on rerun (#276
	// review H — mtime-based reruns weren't actually idempotent).
	if !strings.Contains(got, "--checksum") {
		t.Errorf("--checksum missing (rerun would silently full-copy): %q", got)
	}
	// --progress + --transfers are the two sanity knobs — a tenant
	// copying a 500 MB bucket over their home connection wants both.
	if !strings.Contains(got, "--progress") {
		t.Errorf("--progress missing: %q", got)
	}
	if !strings.Contains(got, "--transfers") {
		t.Errorf("--transfers missing: %q", got)
	}
	// Must be `sync` not `copy` — `copy` leaves orphans at the
	// destination if a source bucket has deletions.
	if !strings.Contains(got, "rclone sync") {
		t.Errorf("must use `rclone sync`, not `copy`: %q", got)
	}
}

func TestRcloneCommandFor_BucketNameWithSpecialCharsRemains(t *testing.T) {
	// Supabase allows bucket names with `-` and `_`. The command
	// must reproduce them verbatim (no escaping needed for these).
	for _, name := range []string{"user-avatars", "photos_2026", "backups"} {
		b := supabaseBucket{name: name}
		got := rcloneCommandFor(b)
		if !strings.Contains(got, "supabase_src:"+name+"/") {
			t.Errorf("bucket name %q lost in source ref: %q", name, got)
		}
		if !strings.Contains(got, "eurobase_dst:"+name+"/") {
			t.Errorf("bucket name %q lost in dest ref: %q", name, got)
		}
	}
}

// TestIsSafeBucketName pins the shell-embedding guard — the rclone
// command is emitted inside a Markdown code fence, so any character
// outside Supabase's actual bucket-name grammar (`[a-zA-Z0-9-_.]`)
// is a rejection. Allowlist design: adding a new attack char to the
// blocklist can't accidentally miss.
func TestIsSafeBucketName(t *testing.T) {
	safe := []string{"avatars", "user-photos", "photos_2026", "backup.v2", "a"}
	unsafe := []string{
		"",              // empty
		"a`b",           // backtick — closes code fence
		"a\nb",          // newline — breaks fence
		"a\tb",          // control char
		"a\"b",          // quote
		"a'b",           // quote
		"a\\b",          // backslash
		"a$b",           // shell expansion
		"\x00nul",       // NUL
		"a b",           // space — breaks arg into two tokens
		"a;b", "a|b", "a&b", "a<b", "a>b",
		"a(b", "a)b", "a{b", "a}b", "a#b",
		"a b",      // Unicode line separator
		"a b",      // Unicode paragraph separator
		"a‮b",      // Right-to-left override (visual reorder)
		"a/b",           // path separator
		"日本語",           // non-ASCII (outside Supabase grammar)
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

// TestRcloneCommandFor_UnsafeBucketNameRefusesCommand pins the
// defense-in-depth: a hostile bucket name doesn't just get escaped
// — the whole command is replaced with a skip comment so nothing
// bad can land in the tenant's shell paste.
func TestRcloneCommandFor_UnsafeBucketNameRefusesCommand(t *testing.T) {
	got := rcloneCommandFor(supabaseBucket{name: "avatars`\n# Fake"})
	if strings.Contains(got, "rclone sync") {
		t.Errorf("unsafe bucket name emitted a real command:\n%s", got)
	}
	if !strings.Contains(got, "skipped") {
		t.Errorf("skip comment missing:\n%s", got)
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
	got := renderBucketSection(b, true)
	// Backticks + asterisks in the name must be escaped — the raw
	// header can't inject a Markdown H1.
	if strings.Contains(got, "\n# Fake H1") {
		t.Errorf("hostile bucket name injected an H1:\n%s", got)
	}
	// Positive assertion: the header line must show the escaped forms
	// (backslash-escaped backtick + asterisks, and the ↩ replacement
	// mdEscape uses for newlines). Without this, a future change to
	// mdEscape that let raw newlines through would still make the
	// negative "no \n# Fake H1" check pass by accident.
	if !strings.Contains(got, `\`+"`") && !strings.Contains(got, "↩") {
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
	// the tenant knows the byte total is uninformative — but the
	// rclone sync still copies everything.
	b := supabaseBucket{
		name:        "legacy",
		objectCount: 100,
		totalBytes:  0,
	}
	got := renderBucketSection(b, true)
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
	got := renderBucketSection(b, false)
	if strings.Contains(got, "\n# Fake header") {
		t.Errorf("hostile MIME injected a header:\n%s", got)
	}
}
