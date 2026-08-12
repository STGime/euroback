package workers

import (
	"testing"

	"github.com/eurobase/euroback/internal/jobs"
)

// TestRestoreTeamDatabaseWorker_Timeout guards against the same
// latent bug fixed on ProvisionTeamDatabaseWorker: River's 1-minute
// default JobTimeout kills pollUntilActive well before the restored
// Scaleway RDB instance finishes WAL replay + reaches `ready`. The
// invariant is `Timeout() >= defaultRestorePollTimeout + restoreSlack`.
func TestRestoreTeamDatabaseWorker_Timeout(t *testing.T) {
	w := &RestoreTeamDatabaseWorker{}
	got := w.Timeout(stubJob(jobs.RestoreTeamDatabaseArgs{}, 1))
	want := defaultRestorePollTimeout + restoreSlack
	if got < want {
		t.Fatalf("Timeout() = %v; want >= defaultRestorePollTimeout + restoreSlack (%v) so outer River deadline outlasts pollUntilActive plus surrounding work",
			got, want)
	}
}
