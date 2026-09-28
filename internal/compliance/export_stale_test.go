package compliance

import (
	"context"
	"os"
	"testing"
)

// Exports stuck in "running" (worker killed, job discarded or cancelled)
// are failed by the hourly cleanup once well past the longest legitimate
// run; recent running exports and finished ones are left alone. Runs on
// the gateway login, like the gateway's cleanup loop.
func TestFailStaleRunningExports(t *testing.T) {
	devURL, gwURL := os.Getenv("EXPORT_TEST_DEV_URL"), os.Getenv("EXPORT_TEST_GATEWAY_URL")
	if devURL == "" || gwURL == "" {
		t.Skip("EXPORT_TEST_DEV_URL / EXPORT_TEST_GATEWAY_URL not set")
	}
	ctx := context.Background()
	dev := mustPool(t, devURL)
	gw := mustPool(t, gwURL)
	projectID, _, userID := provisionRLSFixture(t, dev)

	insert := func(status, startedAgo string) string {
		t.Helper()
		var id string
		if err := gw.QueryRow(ctx,
			`INSERT INTO export_requests (project_id, status, requested_by, started_at)
			 VALUES ($1, $2, $3, now() - $4::interval) RETURNING id`,
			projectID, status, userID, startedAgo).Scan(&id); err != nil {
			t.Fatalf("insert %s: %v", status, err)
		}
		return id
	}
	stale := insert("running", "4 hours")
	recent := insert("running", "1 hour")
	done := insert("completed", "5 hours")

	(&ExportService{Pool: gw}).failStaleRunning(ctx)

	for id, want := range map[string]string{stale: "failed", recent: "running", done: "completed"} {
		var status string
		var errText *string
		if err := gw.QueryRow(ctx, `SELECT status, error FROM export_requests WHERE id = $1`, id).Scan(&status, &errText); err != nil {
			t.Fatal(err)
		}
		if status != want {
			t.Errorf("export started %s: status %q, want %q", map[string]string{stale: "4h ago (running)", recent: "1h ago (running)", done: "5h ago (completed)"}[id], status, want)
		}
		if id == stale && (errText == nil || *errText == "") {
			t.Error("stale export failed without an error message")
		}
	}
}
