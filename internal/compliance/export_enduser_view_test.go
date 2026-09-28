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
	// A completed export with a stale error from a failed earlier attempt
	// reports no error.
	stale := &ExportRequest{ID: "e3", Status: "completed", Error: &errText, DownloadURL: "https://s3.example/y"}
	if v := EndUserExportView(stale); v.Error != nil {
		t.Errorf("completed export shows an error: %q", *v.Error)
	}
}

// #665: the per-user archive (it goes to the data subject) keeps each
// table's status but not the database error text.
func TestUserArchiveMetadataRedaction(t *testing.T) {
	tables := []TableExport{
		{Table: "orders", Status: TableExported, Rows: 3},
		{Table: "notes", Status: TableFailed, Error: `permission denied for table notes (SQLSTATE 42501)`},
		{Table: "events", Status: TableTruncated, Rows: 100000},
	}
	warnings := append(tableWarnings(tables), "the audit log could not be exported")

	red := redactTableErrors(tables)
	if red[1].Error != "" || red[1].Status != TableFailed || tables[1].Error == "" {
		t.Errorf("redactTableErrors: got %+v (original %+v)", red[1], tables[1])
	}
	got := strings.Join(userArchiveWarnings(tables, warnings), "\n")
	if strings.Contains(got, "SQLSTATE") || strings.Contains(got, "permission denied") {
		t.Errorf("archive warnings carry error text: %s", got)
	}
	for _, want := range []string{`table "notes" was not exported`, `table "events" was truncated`, "the audit log could not be exported"} {
		if !strings.Contains(got, want) {
			t.Errorf("archive warnings lack %q: %s", want, got)
		}
	}
	if n := len(userArchiveWarnings(tables, warnings)); n != len(warnings) {
		t.Errorf("warning count %d, want %d", n, len(warnings))
	}
}
