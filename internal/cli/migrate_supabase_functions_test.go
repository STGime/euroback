package cli

import (
	"os"
	"path/filepath"
	"testing"
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
