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
		if !strings.HasPrefix(string(body), "-- Auto-translated from Supabase") {
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

// ── column-type routing (ship-blocker #3/#4) ────────────────────────

func TestNeedsTextCast_JsonbAndNumericFamily(t *testing.T) {
	// The types Postgres returns via pgtype wrappers that sqlLiteral's
	// fallback would `%v`-print into an unusable Go-struct dump.
	positive := []string{
		"jsonb", "json", "numeric", "numeric(10,2)", "interval",
		"inet", "cidr", "macaddr", "date", "money",
		"time(3) without time zone",
	}
	for _, ty := range positive {
		if !needsTextCast(ty) {
			t.Errorf("%q should route through ::text cast", ty)
		}
	}
	// Types that pgx already returns cleanly — leave alone.
	negative := []string{
		"text", "uuid", "bytea", "boolean", "integer", "bigint",
		"timestamptz", "timestamp with time zone",
		"timestamp(3) with time zone",
	}
	for _, ty := range negative {
		if needsTextCast(ty) {
			t.Errorf("%q should NOT be cast to text (pgx handles it natively)", ty)
		}
	}
}

func TestWrapValue_JsonbWrapsAsJsonbValue(t *testing.T) {
	// The whole point of the fix: JSONB comes back as `string` after
	// the ::text cast. wrapValue must lift it back into jsonbValue so
	// sqlLiteral emits the ::jsonb cast on the way out. Without this,
	// the emitted `'{"k":"v"}'` (bare string) would fail to insert
	// into a JSONB column.
	c := columnInfo{name: "metadata", pgType: "jsonb"}
	got := wrapValue(c, `{"k":"v"}`)
	if _, ok := got.(jsonbValue); !ok {
		t.Errorf("jsonb text-cast result should wrap as jsonbValue, got %T", got)
	}
}

func TestWrapValue_NumericWrapsAsTypedLiteral(t *testing.T) {
	c := columnInfo{name: "price", pgType: "numeric(10,2)"}
	got := wrapValue(c, "123.45")
	tl, ok := got.(typedLiteral)
	if !ok {
		t.Fatalf("numeric text-cast result should wrap as typedLiteral, got %T", got)
	}
	if tl.pgType != "numeric" {
		t.Errorf("typedLiteral pgType should collapse to 'numeric', got %q", tl.pgType)
	}
	if tl.value != "123.45" {
		t.Errorf("typedLiteral value lost: %q", tl.value)
	}
}

func TestWrapValue_ArrayTypeRoundTrips(t *testing.T) {
	// integer[] round-trip through ::text cast → wrapValue emits
	// `'{1,2,3}'::integer[]` on insert.
	c := columnInfo{name: "tags", pgType: "integer[]"}
	got := wrapValue(c, "{1,2,3}")
	tl, ok := got.(typedLiteral)
	if !ok {
		t.Fatalf("array text-cast result should wrap as typedLiteral, got %T", got)
	}
	if tl.pgType != "integer[]" {
		t.Errorf("array pgType should keep the []: got %q", tl.pgType)
	}
}

func TestWrapValue_NilStaysNil(t *testing.T) {
	c := columnInfo{name: "x", pgType: "jsonb"}
	if got := wrapValue(c, nil); got != nil {
		t.Errorf("nil should pass through, got %v", got)
	}
}

// TestSelectExprFor_JsonbCastsToText pins the SELECT expression side
// of the same fix.
func TestSelectExprFor_JsonbCastsToText(t *testing.T) {
	got := selectExprFor(columnInfo{name: "metadata", pgType: "jsonb"})
	if !strings.Contains(got, "::text") {
		t.Errorf("jsonb column should be cast to text in SELECT: %q", got)
	}
	// Alias back to the column name so rows.Values() lines up.
	if !strings.Contains(got, `"metadata"`) {
		t.Errorf("SELECT expr should keep the column name alias: %q", got)
	}
}

func TestSelectExprFor_PlainTypeIsBareIdent(t *testing.T) {
	got := selectExprFor(columnInfo{name: "id", pgType: "uuid"})
	// UUID needs no cast — pgx handles it. Should be bare.
	if strings.Contains(got, "::text") {
		t.Errorf("uuid column should NOT be text-cast: %q", got)
	}
}

// TestNeedsTextCast_ExtendedPgTypes pins #275 round-2 M #4 — range,
// bit, hstore, ltree, citext, pg_lsn, regclass etc. must all route
// through the ::text cast so pgx doesn't return pgtype wrappers that
// sqlLiteral's default fallback would %v-print.
func TestNeedsTextCast_ExtendedPgTypes(t *testing.T) {
	for _, ty := range []string{
		"int4range", "int8range", "numrange",
		"tsrange", "tstzrange", "daterange",
		"int4multirange", "int8multirange",
		"tstzmultirange", "datemultirange",
		"bit", "bit varying",
		"hstore", "ltree", "citext", "pg_lsn",
		"regclass", "regconfig", "regrole",
	} {
		if !needsTextCast(ty) {
			t.Errorf("%q should route through ::text cast (silent %%v-print on wrong path)", ty)
		}
	}
}

// TestWrapValue_RangeTypeWrapsCorrectly confirms `tstzrange` and the
// friends route back through typedLiteral so the emit is
// `'[…,…)'::tstzrange` — Postgres re-parses on insert.
func TestWrapValue_RangeTypeWrapsCorrectly(t *testing.T) {
	c := columnInfo{name: "window", pgType: "tstzrange"}
	got := wrapValue(c, `["2026-01-01 00:00:00+00","2026-01-02 00:00:00+00")`)
	tl, ok := got.(typedLiteral)
	if !ok {
		t.Fatalf("range text-cast result should wrap as typedLiteral, got %T", got)
	}
	if tl.pgType != "tstzrange" {
		t.Errorf("tstzrange pgType lost: %q", tl.pgType)
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
	// Actually render the row.
	var buf strings.Builder
	emitInsertBatch(&buf, "users", authUserColumns, [][]interface{}{row})
	// The batch emits header + one row + trailing `;`. We're interested
	// in the row's contribution — the header is at most 300 bytes for
	// this column list. Estimate must cover row + wrapping.
	actualForOneRow := buf.Len() - 100 // subtract rough header
	if est < actualForOneRow {
		t.Errorf("byte estimate underestimated a realistic row: est %d, actual ~%d",
			est, actualForOneRow)
	}
}
