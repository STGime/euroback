package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

// TestResolveEntrypoint_DefaultIndexTs confirms the fallback when
// no deno.json is present — Supabase's convention.
func TestResolveEntrypoint_DefaultIndexTs(t *testing.T) {
	dir := t.TempDir()
	if got := resolveEntrypoint(dir); got != "index.ts" {
		t.Errorf("no deno.json → want index.ts, got %q", got)
	}
}

// TestResolveEntrypoint_MainOverride pins #277 round-1 M #5: a
// deno.json with a "main" field points the CLI at the tenant's
// entrypoint instead of silently missing it.
func TestResolveEntrypoint_MainOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deno.json"), []byte(`{"main":"handler.ts"}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := resolveEntrypoint(dir); got != "handler.ts" {
		t.Errorf("main override missed: want handler.ts, got %q", got)
	}
}

func TestResolveEntrypoint_EntrypointOverride(t *testing.T) {
	// Some Supabase-templated deno.json files use `entrypoint`
	// instead of `main`. Accept either.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deno.json"), []byte(`{"entrypoint":"src/main.ts"}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := resolveEntrypoint(dir); got != "src/main.ts" {
		t.Errorf("entrypoint field missed: got %q", got)
	}
}

// A malformed deno.json falls back to index.ts (fail-soft — the
// tenant sees the "no index.ts" note if they need to fix it).
func TestResolveEntrypoint_MalformedFallsBack(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deno.json"), []byte(`{{{ not json`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := resolveEntrypoint(dir); got != "index.ts" {
		t.Errorf("malformed deno.json should fall back to index.ts, got %q", got)
	}
}

// TestTranslateOneFunction_NestedEntrypointCreatesSubdir pins the
// round-4 review H: a deno.json with `main: "src/handler.ts"` used
// to `WriteFile` into `<out>/<name>/src/handler.ts` where the `src/`
// dir was never created, aborting the whole migrate run.
// End-to-end test: the write must succeed and land the file at the
// nested path.
func TestTranslateOneFunction_NestedEntrypointCreatesSubdir(t *testing.T) {
	inputRoot := t.TempDir()
	outputRoot := t.TempDir()

	funcName := "hello"
	inputFuncDir := filepath.Join(inputRoot, funcName)
	if err := os.MkdirAll(filepath.Join(inputFuncDir, "src"), 0o755); err != nil {
		t.Fatalf("mkdir input src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inputFuncDir, "deno.json"),
		[]byte(`{"main":"src/handler.ts"}`), 0o644); err != nil {
		t.Fatalf("write deno.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inputFuncDir, "src", "handler.ts"),
		[]byte(`Deno.serve(async (req) => new Response("hi"));`),
		0o644); err != nil {
		t.Fatalf("write handler.ts: %v", err)
	}

	// Fake cobra.Command to capture stderr — translateOneFunction
	// only prints there, doesn't call os.Exit.
	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetOut(&bytes.Buffer{})

	summary := funcMigrationSummary{}
	if err := translateOneFunction(inputRoot, outputRoot, funcName, cmd, &summary); err != nil {
		t.Fatalf("translateOneFunction: %v (round-4 H not fixed)", err)
	}

	// Must have written the nested path.
	outPath := filepath.Join(outputRoot, funcName, "src", "handler.ts")
	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("expected file at %s, got: %v", outPath, err)
	}
	if !bytes.Contains(body, []byte("module.exports = async (req, ctx) =>")) {
		t.Errorf("nested-entrypoint file was written but not translated:\n%s", body)
	}
	if summary.ported != 1 {
		t.Errorf("summary.ported = %d, want 1", summary.ported)
	}
}
