package cli

// #272 (part of #267): `eurobase import supabase functions` — walk a
// Supabase project's edge-function tree, rewrite each function for
// Eurobase's runner contract, and write the translated files to an
// output directory ready to deploy.
//
// Input layout matches Supabase's convention:
//
//   ./supabase/functions/<name>/index.ts     (+ optional deno.json)
//
// Output layout matches Eurobase's:
//
//   ./eurobase/functions/<name>/index.ts
//
// Deployment is not automated here — the tenant reviews the diff, then
// deploys via `eurobase functions deploy <name>` (or by uploading via
// the console). Same shape as schema (#269) and data (#270): CLI
// translates + writes; tenant applies.
//
// Unsupported functions (in-handler Supabase SDK client) are SKIPPED
// with a printed note explaining the exact snippet to rewrite. The
// tenant fixes those by hand and reruns.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func importSupabaseFunctionsCmd() *cobra.Command {
	var (
		inputDir  string
		outputDir string
	)
	cmd := &cobra.Command{
		Use:   "functions",
		Short: "Translate Supabase edge functions to Eurobase's runner contract",
		Long: `Walk a Supabase project's ./supabase/functions/ tree and rewrite each
function for Eurobase's runner. Writes the translated files under an
output directory ready to deploy.

Rewrite rules applied:
  - Deno.serve(handler)                → module.exports = handler
  - Deno.serve((req) => …)             → module.exports = async (req, ctx) => …
  - Deno.env.get('KEY')                → ctx.env.KEY
  - Deno.env.get('SUPABASE_URL')       → ctx.env.SUPABASE_URL (also
                                          emits a warning — the value
                                          means something different on
                                          Eurobase)

Warns (does not rewrite):
  - import … from 'https://…'          — supported via esbuild-on-
                                          deploy but eyeball the dep.
  - Deno.env.get(<expr>)               — dynamic access; rewrite to
                                          ctx.env[<expr>] manually.

Skips (SDK-based functions require manual rewrite):
  - Any function that calls createClient(SUPABASE_URL, …) inside the
    handler. Eurobase functions get ctx.db + ctx.storage on the
    invocation context — no separate SDK client. The CLI prints the
    exact snippet to replace.

Deployment is out of scope for this command: review the emitted files,
then run "eurobase functions deploy <name>" for each.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if inputDir == "" {
				inputDir = "./supabase/functions"
			}
			if outputDir == "" {
				outputDir = "./eurobase/functions"
			}
			info, err := os.Stat(inputDir)
			if err != nil {
				return fmt.Errorf("read %s: %w — run this command from your Supabase project root (or pass --input)", inputDir, err)
			}
			if !info.IsDir() {
				return fmt.Errorf("%s is not a directory", inputDir)
			}
			entries, err := os.ReadDir(inputDir)
			if err != nil {
				return fmt.Errorf("list %s: %w", inputDir, err)
			}

			if err := os.MkdirAll(outputDir, 0o755); err != nil {
				return fmt.Errorf("create %s: %w", outputDir, err)
			}

			summary := funcMigrationSummary{}
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				if strings.HasPrefix(e.Name(), "_") || strings.HasPrefix(e.Name(), ".") {
					// Supabase convention: leading `_` means a shared
					// module, not a deployable function. `.git`,
					// `.DS_Store`, etc. also filtered out.
					continue
				}
				if err := translateOneFunction(inputDir, outputDir, e.Name(), cmd, &summary); err != nil {
					return err
				}
			}
			printFuncSummary(cmd, summary)
			if summary.unsupported > 0 {
				// Non-zero-but-not-error: the tenant needs to review
				// the unsupported ones before deploying. Return nil
				// so the shell exit code stays 0 — the print above
				// is loud enough.
				fmt.Fprintln(cmd.ErrOrStderr(), "Skipped functions above need manual rewrite before they can deploy.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&inputDir, "input", "", "Supabase functions directory (default ./supabase/functions)")
	cmd.Flags().StringVarP(&outputDir, "output", "o", "", "Eurobase functions output directory (default ./eurobase/functions)")
	return cmd
}

// funcMigrationSummary carries the per-run counts printed at the end.
type funcMigrationSummary struct {
	ported      int
	unsupported int
	warned      int
	rewrites    int
}

// translateOneFunction reads inputDir/<name>/index.ts, translates,
// and writes outputDir/<name>/index.ts. Anything but a plain
// index.ts is skipped with a note — Supabase supports other entry
// points but 95% of tenant functions use the canonical shape.
func translateOneFunction(inputDir, outputDir, name string, cmd *cobra.Command, summary *funcMigrationSummary) error {
	entryFile := resolveEntrypoint(filepath.Join(inputDir, name))
	inPath := filepath.Join(inputDir, name, entryFile)
	source, err := os.ReadFile(inPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(cmd.ErrOrStderr(), "  %-40s (skipped — no %s)\n", name, entryFile)
			return nil
		}
		return fmt.Errorf("read %s: %w", inPath, err)
	}

	res := TranslateFunction(string(source))
	if res.Unsupported {
		summary.unsupported++
		fmt.Fprintf(cmd.ErrOrStderr(), "  %-40s ✗ unsupported — %s\n", name, res.UnsupportedReason)
		return nil
	}

	outSubdir := filepath.Join(outputDir, name)
	if err := os.MkdirAll(outSubdir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", outSubdir, err)
	}
	outPath := filepath.Join(outSubdir, entryFile)
	if err := os.WriteFile(outPath, []byte(res.Source), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}

	summary.ported++
	nRewrites := 0
	for _, c := range res.Rewrites {
		nRewrites += c
	}
	summary.rewrites += nRewrites
	nWarn := len(res.Warnings)
	summary.warned += nWarn

	if nWarn == 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "  %-40s ✓ ported (%d rewrite(s))\n", name, nRewrites)
	} else {
		fmt.Fprintf(cmd.ErrOrStderr(), "  %-40s ✓ ported (%d rewrite(s), %d warning(s))\n", name, nRewrites, nWarn)
		for _, w := range res.Warnings {
			if w.line > 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "      line %d: %s\n", w.line, w.note)
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "      %s\n", w.note)
			}
		}
	}
	return nil
}

// resolveEntrypoint returns the filename the CLI should read as the
// function's entry. Defaults to `index.ts` (the Supabase convention),
// but honours a per-function `deno.json` `"main"` field if present.
// Round-1 review M #5 flagged that tenants with a `deno.json`-
// configured entry point silently got "no index.ts" and no rewrite.
func resolveEntrypoint(funcDir string) string {
	denoConfig := filepath.Join(funcDir, "deno.json")
	b, err := os.ReadFile(denoConfig)
	if err != nil {
		return "index.ts"
	}
	var cfg struct {
		Main       string `json:"main"`
		Entrypoint string `json:"entrypoint"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "index.ts"
	}
	// Deno CLI honours `"main"` officially; some Supabase-templated
	// projects use `"entrypoint"`. Accept either — the file we open
	// matters, not the config key.
	if cfg.Main != "" {
		return cfg.Main
	}
	if cfg.Entrypoint != "" {
		return cfg.Entrypoint
	}
	return "index.ts"
}

func printFuncSummary(cmd *cobra.Command, s funcMigrationSummary) {
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintf(cmd.OutOrStdout(),
		"Summary: %d ported (%d rewrite(s), %d warning(s)), %d unsupported\n",
		s.ported, s.rewrites, s.warned, s.unsupported,
	)
	if s.ported > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Next: review the translated files, then `eurobase functions deploy <name>` for each.")
	}
}
