package workers

import (
	"testing"

	"github.com/eurobase/euroback/internal/jobs"
)

// TestRestoreTeamDatabaseWorker_Timeout guards against the same
// latent bug fixed on ProvisionTeamDatabaseWorker: River's 1-minute
// default JobTimeout kills pollUntilActive well before the restored
// Scaleway RDB instance finishes WAL replay + reaches `ready`.
// Timeout() must return more than the internal PollTimeout so the
// outer River context is not the binding limit.
func TestRestoreTeamDatabaseWorker_Timeout(t *testing.T) {
	w := &RestoreTeamDatabaseWorker{}
	got := w.Timeout(stubJob(jobs.RestoreTeamDatabaseArgs{}, 1))
	if got <= defaultRestorePollTimeout {
		t.Fatalf("Timeout() = %v; want > defaultRestorePollTimeout (%v) so outer River deadline outlasts pollUntilActive",
			got, defaultRestorePollTimeout)
	}
}
