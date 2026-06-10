package cli

import "testing"

// Issue #192: a stale active project (deleted, or belonging to a different
// account) silently received deploys after login. ReconcileActiveProject is
// the login-time guard against that.

func TestReconcileActiveProject_SingleProjectAutoSelected(t *testing.T) {
	cfg := &Config{ActiveProject: "stale-id", ProjectSlug: "stale"}
	ReconcileActiveProject(cfg, []ProjectRef{{ID: "p1", Slug: "predwell"}})
	if cfg.ActiveProject != "p1" || cfg.ProjectSlug != "predwell" {
		t.Fatalf("expected auto-select of the only project, got %q (%q)", cfg.ProjectSlug, cfg.ActiveProject)
	}
}

func TestReconcileActiveProject_StaleSelectionCleared(t *testing.T) {
	cfg := &Config{ActiveProject: "other-account-id", ProjectSlug: "newtek"}
	ReconcileActiveProject(cfg, []ProjectRef{
		{ID: "p1", Slug: "predwell"},
		{ID: "p2", Slug: "demo"},
	})
	if cfg.ActiveProject != "" || cfg.ProjectSlug != "" {
		t.Fatalf("expected stale selection to be cleared, got %q (%q)", cfg.ProjectSlug, cfg.ActiveProject)
	}
}

func TestReconcileActiveProject_ValidSelectionKeptAndSlugHealed(t *testing.T) {
	cfg := &Config{ActiveProject: "p2"} // slug missing, e.g. written by an older CLI
	ReconcileActiveProject(cfg, []ProjectRef{
		{ID: "p1", Slug: "predwell"},
		{ID: "p2", Slug: "demo"},
	})
	if cfg.ActiveProject != "p2" || cfg.ProjectSlug != "demo" {
		t.Fatalf("expected selection kept with slug healed, got %q (%q)", cfg.ProjectSlug, cfg.ActiveProject)
	}
}

func TestReconcileActiveProject_NoSelectionStaysEmpty(t *testing.T) {
	cfg := &Config{}
	ReconcileActiveProject(cfg, []ProjectRef{
		{ID: "p1", Slug: "predwell"},
		{ID: "p2", Slug: "demo"},
	})
	if cfg.ActiveProject != "" {
		t.Fatalf("expected no auto-select with multiple projects, got %q", cfg.ActiveProject)
	}
}

func TestReconcileActiveProject_EmptyListClearsSelection(t *testing.T) {
	// Zero projects: nothing to validate against — clear the selection,
	// the account provably has no project with that ID.
	cfg := &Config{ActiveProject: "p9", ProjectSlug: "ghost"}
	ReconcileActiveProject(cfg, []ProjectRef{})
	if cfg.ActiveProject != "" || cfg.ProjectSlug != "" {
		t.Fatalf("expected selection cleared for empty project list, got %q (%q)", cfg.ProjectSlug, cfg.ActiveProject)
	}
}

func TestProjectLabel(t *testing.T) {
	withSlug := &Config{ActiveProject: "0515e4e2", ProjectSlug: "predwell"}
	if got := ProjectLabel(withSlug); got != "predwell (0515e4e2)" {
		t.Fatalf("ProjectLabel with slug = %q", got)
	}
	idOnly := &Config{ActiveProject: "0515e4e2"}
	if got := ProjectLabel(idOnly); got != "0515e4e2" {
		t.Fatalf("ProjectLabel without slug = %q", got)
	}
}
