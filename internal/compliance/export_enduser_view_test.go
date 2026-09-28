package compliance

import (
	"encoding/json"
	"strings"
	"testing"
)

// #665: an end user's own export shows `complete`, never the per-table
// warnings or the internal failure text (table names, Postgres errors).
func TestEndUserExportView(t *testing.T) {
	complete := false
	errText := `build zip: table "secret_internal_table": permission denied (SQLSTATE 42501)`
	key := "exports/p1/e1.zip"
	req := &ExportRequest{
		ID: "e1", ProjectID: "p1", Status: "failed", Format: "json",
		S3Key: &key, Error: &errText, RequestedBy: "u1", RequestedByType: "end_user",
		Complete: &complete, Warnings: []string{`table "secret_internal_table" was not exported: boom`},
		DownloadURL: "https://s3.example/x",
	}
	v := EndUserExportView(req)
	b, _ := json.Marshal(v)
	out := string(b)
	for _, leak := range []string{"secret_internal_table", "SQLSTATE", "warnings", "s3_key", "exports/p1"} {
		if strings.Contains(out, leak) {
			t.Errorf("end-user view contains %q: %s", leak, out)
		}
	}
	if v.Complete == nil || *v.Complete || v.Error == nil || *v.Error != endUserExportFailed || v.Status != "failed" || v.DownloadURL == "" {
		t.Errorf("end-user view lost fields: %s", out)
	}
	// The original is untouched (the platform API still returns it all).
	if req.Warnings == nil || *req.Error != errText || req.S3Key == nil {
		t.Error("EndUserExportView modified its argument")
	}
	if EndUserExportView(nil) != nil {
		t.Error("nil in, non-nil out")
	}
	ok := &ExportRequest{ID: "e2", Status: "completed"}
	if v := EndUserExportView(ok); v.Error != nil {
		t.Errorf("no error on success, got %q", *v.Error)
	}
}
