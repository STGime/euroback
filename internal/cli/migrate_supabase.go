// Package cli — Supabase → Eurobase migration path (#267).
//
// This file is the parent command tree for `eurobase import supabase …`.
// Each subcommand (assess, schema, data, storage, functions) lives in its
// own file so the top-level surface stays scannable.
//
// The migration path is read-first: `assess` runs against a Supabase
// project without touching it, produces a compat report, and lets the
// tenant decide whether the actual migration steps (schema/data/…) are
// worth committing to. See the umbrella #267 for the full plan.
package cli

import (
	"github.com/spf13/cobra"
)

// ImportCmd returns the "import" parent command. Named `import` (not
// `migrate`) because `eurobase migrations` already aliases to
// `migrate` for DDL migrations — collision would confuse anyone
// switching between DDL work and a cross-BaaS import.
//
// Structured as a parent because we may grow into "import firebase" /
// "import planetscale" etc. later — the "supabase" leaf is the first.
func ImportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import a project from another BaaS provider",
		Long: `Import a project from another BaaS provider into Eurobase.

Currently supports:
  supabase — Supabase → Eurobase (auth, database, storage, functions).

Each source-provider tree ships its own subcommands. See
"eurobase import supabase --help" for the four-step flow.`,
	}
	cmd.AddCommand(importSupabaseCmd())
	return cmd
}

// importSupabaseCmd is the "supabase" sub-parent. Subcommands run in
// order — a tenant typically does assess → schema → data → storage →
// functions — but they're independently invocable so a partial run
// can be resumed.
func importSupabaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "supabase",
		Short: "Import a Supabase project into Eurobase",
		Long: `Import a Supabase project into Eurobase.

Typical flow (each step is its own subcommand):

  1. assess     — read-only reconnaissance. Enumerates what's in the
                  Supabase project and produces a compat report.
  2. schema     — translate DDL + RLS policies (Supabase → Eurobase
                  conventions) and apply to the target Eurobase tenant.
  3. data       — stream row data + auth users into Eurobase.
  4. storage    — copy object bytes + rewrite bucket policies.
  5. functions  — port Deno edge functions to Eurobase's handler
                  contract and deploy.

Every subcommand needs the Supabase source credentials:

  SUPABASE_DB_URL       postgres://…  (the tenant's Supabase Postgres URL)
  SUPABASE_SERVICE_KEY  the Supabase service-role key

Target Eurobase project comes from the standard Eurobase CLI config
(run "eurobase login" + "eurobase switch <project>" first).

Everything except "assess" WRITES to the target Eurobase project. Run
into an empty project unless you're comfortable overwriting.`,
	}
	cmd.AddCommand(importSupabaseAssessCmd())
	cmd.AddCommand(importSupabaseSchemaCmd())
	cmd.AddCommand(importSupabaseDataCmd())
	cmd.AddCommand(importSupabaseStorageCmd())
	// Placeholder subcommand lands in follow-up PR #272.
	return cmd
}
