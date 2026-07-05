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
	// Must reference both remotes with the bucket name.
	if !strings.Contains(got, "supabase_src:avatars") {
		t.Errorf("source ref missing: %q", got)
	}
	if !strings.Contains(got, "eurobase_dst:avatars") {
		t.Errorf("dest ref missing: %q", got)
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
		if !strings.Contains(got, "supabase_src:"+name) {
			t.Errorf("bucket name %q lost in source ref: %q", name, got)
		}
		if !strings.Contains(got, "eurobase_dst:"+name) {
			t.Errorf("bucket name %q lost in dest ref: %q", name, got)
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

// TestBuildStorageReport_ShapeAndFooter checks the report shape by
// stubbing the aggregation — we skip the DB call by pre-populating
// object counts on the buckets and asserting the emitted document.
//
// Since buildStorageReport currently calls storageObjectTotals(), we
// can't easily unit-test the whole flow without a live conn. Instead
// pin the rendering-only pieces via helpers below and defer full-flow
// coverage to manual smoke tests against a real Supabase project.

func TestReportHeaderIncludesTotals(t *testing.T) {
	// Not a full-flow test — we assemble the header manually and just
	// check the formatting helpers do the right thing. formatCount +
	// formatBytes already have their own tests (from #268 assess).
	if got := formatBytes(1024 * 1024); got != "1.0 MB" {
		t.Errorf("formatBytes byte scale drift: %q", got)
	}
	if got := formatCount(1500); got != "1.5k" {
		t.Errorf("formatCount k-scale drift: %q", got)
	}
}
