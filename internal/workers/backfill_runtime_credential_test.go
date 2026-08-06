package workers

import (
	"context"
	"errors"
	"testing"

	"github.com/eurobase/euroback/internal/dbprovider"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/riverqueue/river"
)

// TestBackfill_CancelsWithoutCipher — dev mode boots the worker
// without VAULT_ENCRYPTION_KEY, but a real backfill needs to open
// the sealed owner password to build the DSN. Must cancel (not
// retry) so River doesn't burn attempts on a config error.
func TestBackfill_CancelsWithoutCipher(t *testing.T) {
	w := &BackfillRuntimeCredentialWorker{
		Cipher: nil,
		Repo:   dbprovider.NewRepo(nil),
	}
	err := w.Work(context.Background(), stubJob(jobs.BackfillRuntimeCredentialArgs{
		ProjectDatabaseID: "pdb-abc",
	}, 1))
	if err == nil {
		t.Fatal("want error, got nil")
	}
	var cancelErr *river.JobCancelError
	if !errors.As(err, &cancelErr) {
		t.Errorf("want river.JobCancelError (unretryable config error), got %T: %v", err, err)
	}
}
