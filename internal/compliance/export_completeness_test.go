package compliance

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ── #654: completeness — pure unit tests (no DB) ────────────────────────────

// fakeRows yields rows of one "n" column, then errs (if set).
type fakeRows struct {
	vals []int
	i    int
	err  error
}

func (r *fakeRows) Close()                        {}
func (r *fakeRows) Err() error                    { return r.errIfDone() }
func (r *fakeRows) CommandTag() pgconn.CommandTag { return pgconn.CommandTag{} }
func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription {
	return []pgconn.FieldDescription{{Name: "n"}}
}
func (r *fakeRows) Next() bool {
	if r.i >= len(r.vals) {
		return false
	}
	r.i++
	return true
}
func (r *fakeRows) Scan(...any) error      { return errors.New("not used") }
func (r *fakeRows) Values() ([]any, error) { return []any{r.vals[r.i-1]}, nil }
func (r *fakeRows) RawValues() [][]byte    { return nil }
func (r *fakeRows) Conn() *pgx.Conn        { return nil }
func (r *fakeRows) errIfDone() error {
	if r.i >= len(r.vals) {
		return r.err
	}
	return nil
}

type fakeQuerier struct{ rows *fakeRows }

func (q fakeQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) { return q.rows, nil }

// zipEntries streams into a zip and returns its entries by name.
func zipEntries(t *testing.T, fn func(zw *zip.Writer)) map[string][]byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fn(zw)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = b
	}
	return out
}

func TestStreamQueryToZip_FailurePartWayLeavesValidJSON(t *testing.T) {
	boom := errors.New("connection reset")
	var res streamResult
	var err error
	entries := zipEntries(t, func(zw *zip.Writer) {
		res, err = streamQueryToZip(context.Background(), fakeQuerier{&fakeRows{vals: []int{1, 2}, err: boom}},
			zw, "q", nil, "tables/t", "json", streamOpts{writeEmpty: true})
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if res.Rows != 2 || !res.Written {
		t.Fatalf("res = %+v, want 2 rows written", res)
	}
	var rows []map[string]any
	if err := json.Unmarshal(entries["tables/t.json"], &rows); err != nil {
		t.Fatalf("partial file is not valid JSON: %v\n%s", err, entries["tables/t.json"])
	}
	if len(rows) != 2 {
		t.Errorf("partial file has %d rows, want 2", len(rows))
	}
}

func TestStreamQueryToZip_EmptyTableGetsAFile(t *testing.T) {
	for _, format := range []string{"json", "csv"} {
		entries := zipEntries(t, func(zw *zip.Writer) {
			if _, err := streamQueryToZip(context.Background(), fakeQuerier{&fakeRows{}}, zw, "q", nil,
				"tables/empty", format, streamOpts{writeEmpty: true}); err != nil {
				t.Fatal(err)
			}
		})
		body, ok := entries["tables/empty."+format]
		if !ok {
			t.Fatalf("%s: no file for an empty table; have %v", format, entries)
		}
		if format == "json" {
			var rows []any
			if err := json.Unmarshal(body, &rows); err != nil || len(rows) != 0 {
				t.Errorf("json: %q is not an empty array (%v)", body, err)
			}
		} else {
			recs, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
			if err != nil || len(recs) != 1 || recs[0][0] != "n" {
				t.Errorf("csv: want header only, got %q (%v)", body, err)
			}
		}
	}
	// Without writeEmpty (audit log, as before) an empty result has no file.
	entries := zipEntries(t, func(zw *zip.Writer) {
		_, _ = streamQueryToZip(context.Background(), fakeQuerier{&fakeRows{}}, zw, "q", nil, "_audit_log", "json", streamOpts{})
	})
	if len(entries) != 0 {
		t.Errorf("writeEmpty=false wrote %v", entries)
	}
}

func TestStreamQueryToZip_Truncation(t *testing.T) {
	var res streamResult
	entries := zipEntries(t, func(zw *zip.Writer) {
		var err error
		res, err = streamQueryToZip(context.Background(), fakeQuerier{&fakeRows{vals: []int{1, 2, 3}}}, zw, "q", nil,
			"tables/big", "json", streamOpts{writeEmpty: true, limit: 2})
		if err != nil {
			t.Fatal(err)
		}
	})
	if !res.Truncated || res.Rows != 2 {
		t.Fatalf("res = %+v, want 2 rows, truncated", res)
	}
	var rows []any
	if err := json.Unmarshal(entries["tables/big.json"], &rows); err != nil || len(rows) != 2 {
		t.Errorf("truncated file: %d rows (%v), want 2", len(rows), err)
	}

	// Exactly `limit` rows is not truncation.
	zipEntries(t, func(zw *zip.Writer) {
		res, _ = streamQueryToZip(context.Background(), fakeQuerier{&fakeRows{vals: []int{1, 2}}}, zw, "q", nil,
			"tables/exact", "json", streamOpts{writeEmpty: true, limit: 2})
	})
	if res.Truncated {
		t.Error("exactly limit rows reported as truncated")
	}
}

func TestZipFileNames_SafeAndUnique(t *testing.T) {
	got := zipFileNames("tables", []string{"bookings", "a/../../etc", "..", "", "Foo", "foo", "Straße"})
	want := map[string]string{
		"bookings":    "tables/bookings",
		"a/../../etc": "tables/a_.._.._etc",
		"..":          "tables/_..",
		"":            "tables/_",
		"Foo":         "tables/Foo",
		"foo":         "tables/foo-2",
		"Straße":      "tables/Stra_e",
	}
	for table, w := range want {
		if got[table] != w {
			t.Errorf("zipFileNames[%q] = %q, want %q", table, got[table], w)
		}
		if strings.Contains(strings.TrimPrefix(got[table], "tables/"), "/") {
			t.Errorf("%q escapes tables/: %q", table, got[table])
		}
	}
}

func TestTableWarnings(t *testing.T) {
	w := tableWarnings([]TableExport{
		{Table: "ok", Status: TableExported, Rows: 3},
		{Table: "big", Status: TableTruncated, Rows: 100000},
		{Table: "forced", Status: TableFailed, Error: `query would be affected by row-level security policy for table "forced"`},
	})
	if len(w) != 2 || !strings.Contains(w[0], `"big"`) || !strings.Contains(w[1], `"forced"`) {
		t.Errorf("warnings = %q", w)
	}
}

func TestExportValue_UUIDs(t *testing.T) {
	id := [16]byte{0x48, 0xf7, 0x24, 0x09, 0x55, 0x57, 0x4b, 0x3e, 0x91, 0x7a, 0x56, 0x24, 0x0f, 0xc1, 0x3b, 0x66}
	const want = "48f72409-5557-4b3e-917a-56240fc13b66"
	if got := exportValue(id); got != want {
		t.Errorf("uuid = %v, want %s", got, want)
	}
	arr, ok := exportValue([]any{id, nil}).([]any)
	if !ok || arr[0] != want || arr[1] != nil {
		t.Errorf("uuid[] = %#v", arr)
	}
	if got := formatCSVCell(exportValue(id)); got != want {
		t.Errorf("csv uuid = %q, want %s", got, want)
	}
	if got := exportValue("text"); got != "text" {
		t.Errorf("text changed: %v", got)
	}
}
