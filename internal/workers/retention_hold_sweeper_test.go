package workers

import (
	"strings"
	"testing"
)

// The sweeper's SQL is a single DELETE. It matters because a
// broken WHERE clause here either purges live holds (data loss)
// or purges nothing (indefinite growth). Pin the intent so a
// future edit that widens the predicate has to actively touch
// this test.
//
// Note this asserts against the package-level RetentionHoldSweeperDeleteSQL
// constant that runRetentionHoldSweeper actually executes — not a
// hand-copied mirror string. Editing the DELETE in retention_hold_sweeper.go
// will trip these assertions.
func TestRetentionHoldSweeper_QueryShape(t *testing.T) {
	got := RetentionHoldSweeperDeleteSQL
	for _, want := range []string{
		"DELETE FROM public.retention_holds",
		"WHERE expires_at < now()",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("sweeper query missing %q\ngot: %s", want, got)
		}
	}
	// Guard against a common failure mode: someone accidentally
	// drops the WHERE while refactoring. The presence of "WHERE"
	// is asserted above, but pin the "expires_at" reference
	// explicitly — a WHERE that filters on something else
	// (project_id, target_type) would silently misbehave.
	if !strings.Contains(got, "expires_at") {
		t.Fatalf("sweeper query lost expires_at predicate — would purge live holds")
	}
}
