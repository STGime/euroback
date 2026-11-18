package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// #275 review ship-blocker #2: the tenant-migrations endpoint caps
// each body at 512 KiB, so the emitter must split across files.
// sqlFileWriter is the piece that enforces the split — bugs here
// would either produce oversized files (endpoint rejects) or split
// mid-statement (endpoint sees invalid SQL).

func TestSQLFileWriter_SplitsAtSizeCap(t *testing.T) {
	dir := t.TempDir()
	// Small cap so we can verify split with reasonable-sized inputs.
	w := newSQLFileWriter(dir, "test", time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC), 2*1024)

	// Write 20 statements, each ~200 bytes. Should span multiple files.
	stmt := strings.Repeat("x", 190) + ";\n"
	for i := 0; i < 20; i++ {
		if err := w.WriteStatement(stmt); err != nil {
			t.Fatalf("WriteStatement: %v", err)
		}
	}
	paths, err := w.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(paths) < 2 {
		t.Fatalf("expected split across ≥ 2 files, got %d", len(paths))
	}
	// Each file must be under the cap.
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if info.Size() > 2*1024 {
			t.Errorf("file %s exceeds cap: %d bytes", p, info.Size())
		}
	}
	// Filenames must be version-distinct (endpoint applies in order).
	seenVersions := map[string]bool{}
	for _, p := range paths {
		base := filepath.Base(p)
		ver := strings.SplitN(base, "_", 2)[0]
		if seenVersions[ver] {
			t.Errorf("duplicate version %q across files: %v", ver, paths)
		}
		seenVersions[ver] = true
	}
}

func TestSQLFileWriter_EachFileGetsHeader(t *testing.T) {
	dir := t.TempDir()
	// Use a larger cap so header + one 900B statement fits comfortably
	// under the cap — the previous version of this test used cap=1024
	// which resulted in each file being ~1130 bytes (over cap) but the
	// test only counted paths, not size. #275 round-2 L #6.
	cap := 2048
	w := newSQLFileWriter(dir, "test", time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC), cap)
	// 3 large statements — each 1500B, so the first fits but the
	// second forces rotation → 3 files total.
	stmt := strings.Repeat("y", 1490) + ";\n"
	for i := 0; i < 3; i++ {
		if err := w.WriteStatement(stmt); err != nil {
			t.Fatalf("WriteStatement: %v", err)
		}
	}
	paths, err := w.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(paths) != 3 {
		t.Fatalf("expected 3 files, got %d", len(paths))
	}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		// Each file must fit under the cap — otherwise the endpoint
		// rejects it and we've failed silently.
		if info.Size() > int64(cap) {
			t.Errorf("file %s exceeds cap (%d > %d)", p, info.Size(), cap)
		}
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		if !strings.HasPrefix(string(body), "-- Written by `eurobase import supabase data`") {
			t.Errorf("file %s missing header — endpoint applies parts out of context without it", p)
		}
	}
}

func TestSQLFileWriter_EmptyWriterProducesNoFiles(t *testing.T) {
	dir := t.TempDir()
	w := newSQLFileWriter(dir, "test", time.Now(), 1024)
	paths, err := w.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(paths) != 0 {
		t.Errorf("empty writer wrote %d files: %v", len(paths), paths)
	}
}

func TestSQLFileWriter_DoesNotSplitMidStatement(t *testing.T) {
	dir := t.TempDir()
	// 100-byte cap; write a 500-byte statement. It should land in one
	// file (single oversized statement is a loud fail at apply, not a
	// silent split — the split boundary is at WriteStatement calls).
	w := newSQLFileWriter(dir, "test", time.Now(), 100)
	huge := strings.Repeat("z", 500) + ";\n"
	if err := w.WriteStatement(huge); err != nil {
		t.Fatalf("WriteStatement: %v", err)
	}
	paths, err := w.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("oversized single statement should be one file, got %d", len(paths))
	}
	body, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(body), huge) {
		t.Error("oversized statement got truncated / split")
	}
}

// TestEmitOneTable_ByteBatchingRespectsCap pins #275 round-2
// ship-blocker #1: batching must respect a byte ceiling, not just
// a row-count ceiling. Wide-row tables previously produced 1000-row
// batches that individually exceeded the 512 KiB endpoint cap. We
// can't easily invoke emitOneTable from a unit test (needs a live
// pgx.Conn) — assert the estimator itself here, plus the constant
// wiring.
func TestMaxBatchBytes_BelowEndpointCap(t *testing.T) {
	// The endpoint's cap is 512 KiB; our batch cap must be strictly
	// below (needs headroom for the per-file header + writer slack).
	if maxBatchBytes >= 512*1024 {
		t.Errorf("maxBatchBytes (%d) must be < 512 KiB endpoint cap", maxBatchBytes)
	}
	// And not so tiny it wrecks throughput on realistic rows.
	if maxBatchBytes < 100*1024 {
		t.Errorf("maxBatchBytes (%d) too small — ingest would crawl", maxBatchBytes)
	}
}

func TestEstimateRowBytes_ScalesWithColumnCount(t *testing.T) {
	row1 := []interface{}{"a", "b"}
	row10 := make([]interface{}, 10)
	for i := range row10 {
		row10[i] = "a"
	}
	if estimateRowBytes(row10) <= estimateRowBytes(row1) {
		t.Errorf("byte estimate should grow with column count")
	}
}

func TestEstimateRowBytes_NoUnderestimateOnRealShapes(t *testing.T) {
	// A realistic auth.users row: uuid + email + bcrypt + jsonb + times.
	// The estimator must be conservative — never smaller than the
	// actual emitted bytes.
	email := "someone-with-a-longer-than-average-email-address@example.co.uk"
	pw := "$2b$12$abcdefghijklmnopqrstuvwxyz.1234567890abcdefghij"
	tm := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	row := []interface{}{
		"8e6a6b74-5a5f-45d2-9c2f-2d3a5b8e1a1c",
		&email,
		(*string)(nil),
		&pw,
		jsonbLiteral(`{"display_name":"Alice","avatar_url":"https://example.com/avatars/a.png"}`),
		&tm, &tm, &tm, (*time.Time)(nil), tm, tm,
	}
	est := estimateRowBytes(row)
	// What one more row adds to the statement, measured exactly.
	var one, two strings.Builder
	emitInsertBatch(&one, "users", authUserColumns, [][]interface{}{row})
	emitInsertBatch(&two, "users", authUserColumns, [][]interface{}{row, row})
	actualForOneRow := two.Len() - one.Len()
	if est < actualForOneRow {
		t.Errorf("byte estimate underestimated a realistic row: est %d, actual %d",
			est, actualForOneRow)
	}
	// Text that is all quotes doubles in size; the estimate must follow.
	quotes := []interface{}{strings.Repeat("'", 1000)}
	one.Reset()
	two.Reset()
	emitInsertBatch(&one, "t", []string{"c"}, [][]interface{}{quotes})
	emitInsertBatch(&two, "t", []string{"c"}, [][]interface{}{quotes, quotes})
	if est, actual := estimateRowBytes(quotes), two.Len()-one.Len(); est < actual {
		t.Errorf("quote-heavy row: est %d, actual %d", est, actual)
	}
}
