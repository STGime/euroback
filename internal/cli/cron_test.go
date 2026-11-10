package cli

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/cron"
)

// The CLI's cron types must read the API's JSON: field names drifted once
// and `cron list` showed every job as never run, with no type.
func TestCronTypesMatchAPI(t *testing.T) {
	last := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	data, _ := json.Marshal([]cron.CronJob{{ID: "j1", Name: "n", Schedule: "* * * * *", ActionType: "rpc", Enabled: true, LastRunAt: &last, RunCount: 2}})
	var jobs []cliCronJob
	if err := json.Unmarshal(data, &jobs); err != nil {
		t.Fatal(err)
	}
	if j := jobs[0]; j.ActionType != "rpc" || j.RunCount != 2 || j.LastRunAt == nil || !j.LastRunAt.Equal(last) {
		t.Errorf("job = %+v", j)
	}

	ms, msg := 12, "boom"
	data, _ = json.Marshal([]cron.CronJobRun{{StartedAt: last, DurationMs: &ms, Status: "error", Error: &msg}})
	var runs []cliCronRun
	if err := json.Unmarshal(data, &runs); err != nil {
		t.Fatal(err)
	}
	if r := runs[0]; !r.StartedAt.Equal(last) || r.DurationMs == nil || *r.DurationMs != 12 || r.Error == nil || *r.Error != "boom" {
		t.Errorf("run = %+v", r)
	}
}
