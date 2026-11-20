package cli

// `eurobase import supabase functions` — check a Supabase project's edge
// functions against Eurobase and deploy them.
//
// The functions are not rewritten. `eurobase edge-functions deploy` builds
// a function written for Supabase with a compatibility layer (see
// edge_compat.go), so Deno.serve, Deno.env.get, the Authorization header
// and createClient() keep working. This command does the part that needs
// the whole project in view:
//
//   - builds every function the way a deploy would, so one that can't be
//     bundled (a Node-only library, a missing import) is found now
//   - points at calls the compatibility layer doesn't have, by file and
//     line, with what to use instead
//   - lists the secrets the functions read, which go into the Vault
//   - reads supabase/config.toml for functions that take calls without a
//     signed-in user (verify_jwt = false)
//   - with --deploy, deploys the functions that are ready

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

func importSupabaseFunctionsCmd() *cobra.Command {
	var (
		inputDir string
		deploy   bool
		noBuild  bool
		only     []string
	)
	cmd := &cobra.Command{
		Use:   "functions",
		Short: "Check a Supabase project's edge functions and deploy them",
		Long: `Check the edge functions under ./supabase/functions and, with --deploy,
deploy them to the active project.

The functions run as they are written: the deploy builds each one with a
compatibility layer that supplies what the code had on Supabase —

  Deno.serve(handler)      the handler becomes the function's handler
  Deno.env.get("NAME")     the function's env vars, then the project's Vault
  Authorization header     present again when a signed-in user calls
  createClient(url, key)   auth.getUser, from().select/insert/update/
                           upsert/delete, rpc() and storage, run as SQL
                           through ctx.db
  imports                  https://, npm:, jsr: and ../_shared files are
                           bundled into the function

A client made with the service role key runs its queries with the service
role; such a function is deployed with --allow-service-role.

For each function the command reports whether it is ready, and lists by
file and line the calls the compatibility layer doesn't have (calling
another function, realtime channels, embedded selects, or() filters, most
auth.admin calls). Those need a change in the function's code.

Without --deploy nothing is changed anywhere.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			if inputDir == "" {
				inputDir = "./supabase/functions"
			}
			fns, err := listSupabaseFunctions(inputDir)
			if err != nil {
				return err
			}
			if len(only) > 0 {
				want := map[string]bool{}
				for _, n := range only {
					want[n] = true
				}
				var kept []*supabaseFunction
				for _, fn := range fns {
					if want[fn.Name] {
						kept = append(kept, fn)
						delete(want, fn.Name)
					}
				}
				for n := range want {
					return fmt.Errorf("no function %q in %s", n, inputDir)
				}
				fns = kept
			}
			if len(fns) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "No functions found in %s.\n", inputDir)
				return nil
			}

			out := cmd.OutOrStdout()
			loader := newRemoteLoader() // one download of each module for all functions
			ready, secrets := 0, map[string]bool{}
			for _, fn := range fns {
				fn.analyze()
				if !noBuild && fn.Entry != "" {
					res, err := bundleFunction(fn.Entry, bundleOptions{Compat: compatSupabase, loader: loader})
					if err != nil {
						fn.BuildError = err.Error()
					} else {
						fn.code = res.Code
						fn.SizeKB = len(res.Code) / 1024
					}
				}
				printFunctionStatus(out, fn)
				if fn.Ready() {
					ready++
				}
				for _, s := range fn.Secrets {
					secrets[s] = true
				}
			}

			fmt.Fprintf(out, "\n%d function(s): %d ready, %d need changes\n", len(fns), ready, len(fns)-ready)
			if len(secrets) > 0 {
				names := make([]string, 0, len(secrets))
				for s := range secrets {
					names = append(names, s)
				}
				sort.Strings(names)
				fmt.Fprintf(out, "\nSecrets the functions read — set each one in the project's Vault before the functions run\n(console → Vault, or `eurobase vault set NAME VALUE`):\n  %s\n", strings.Join(names, "\n  "))
			}

			if !deploy {
				if ready > 0 {
					fmt.Fprintln(out, "\nNext: run again with --deploy to deploy the functions that are ready.")
				}
				return nil
			}
			if noBuild {
				return fmt.Errorf("--deploy needs the build; drop --no-build")
			}
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			if err := RequireProject(cfg); err != nil {
				return err
			}
			client, err := NewClientFromConfig()
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "\nDeploying to %s\n", ProjectLabel(cfg))
			failed := 0
			for _, fn := range fns {
				if !fn.Ready() {
					continue
				}
				// Set either way, so a function that no longer uses the
				// service role key loses the setting on the next import.
				allow := &fn.UsesServiceRole
				if _, err := uploadFunction(client, cfg.ActiveProject, fn.Name, fn.code, fn.VerifyJWT, allow); err != nil {
					failed++
					fmt.Fprintf(out, "  %-40s ✗ %v\n", fn.Name, err)
					continue
				}
				fmt.Fprintf(out, "  %-40s ✓ deployed%s\n", fn.Name, deployNote(fn))
			}
			if failed > 0 {
				return fmt.Errorf("%d function(s) could not be deployed", failed)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&inputDir, "input", "", "Supabase functions directory (default ./supabase/functions)")
	cmd.Flags().BoolVar(&deploy, "deploy", false, "Deploy the functions that are ready to the active project")
	cmd.Flags().BoolVar(&noBuild, "no-build", false, "Only read the code; don't build each function (no downloads)")
	cmd.Flags().StringSliceVar(&only, "only", nil, "Only these functions (comma-separated names)")
	return cmd
}

func deployNote(fn *supabaseFunction) string {
	var parts []string
	if fn.UsesServiceRole {
		parts = append(parts, "service role allowed")
	}
	if !fn.VerifyJWT {
		parts = append(parts, "open to calls without a signed-in user")
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func printFunctionStatus(out interface{ Write([]byte) (int, error) }, fn *supabaseFunction) {
	status := "✓ ready"
	if !fn.Ready() {
		status = "✗ needs changes"
	}
	var facts []string
	if fn.SizeKB > 0 {
		facts = append(facts, fmt.Sprintf("%d KB", fn.SizeKB))
	}
	if fn.UsesServiceRole {
		facts = append(facts, "service role")
	}
	if !fn.VerifyJWT {
		facts = append(facts, "no sign-in required")
	}
	if len(fn.Secrets) > 0 {
		facts = append(facts, "reads "+strings.Join(fn.Secrets, ", "))
	}
	line := fmt.Sprintf("  %-40s %s", fn.Name, status)
	if len(facts) > 0 {
		line += "  (" + strings.Join(facts, "; ") + ")"
	}
	fmt.Fprintln(out, line)
	if fn.Entry == "" {
		fmt.Fprintf(out, "      no index.ts in %s\n", fn.Dir)
	}
	if fn.BuildError != "" {
		fmt.Fprintf(out, "      %s\n", strings.ReplaceAll(fn.BuildError, "\n", "\n      "))
	}
	for _, f := range fn.Findings {
		fmt.Fprintf(out, "      %s:%d  %s — %s\n", f.File, f.Line, f.What, f.Instead)
	}
	for _, n := range fn.Notes {
		fmt.Fprintf(out, "      note: %s\n", n)
	}
}

// ── one function ────────────────────────────────────────────────────

// supabaseFunction is one folder under supabase/functions.
type supabaseFunction struct {
	Name string
	Dir  string
	// Entry is the file the function starts in; "" when there is none.
	Entry string
	// VerifyJWT is false when supabase/config.toml says the function takes
	// calls without a signed-in user.
	VerifyJWT bool
	// Findings are calls the compatibility layer doesn't have.
	Findings []functionFinding
	// Secrets are the settings the code reads, other than Supabase's own.
	Secrets []string
	// UsesServiceRole: the code reads the service role key.
	UsesServiceRole bool
	// Notes are things to know that don't stop a deploy.
	Notes []string
	// BuildError is why the function can't be bundled.
	BuildError string
	SizeKB     int

	code string
}

// Ready: the function can be deployed as it is.
func (fn *supabaseFunction) Ready() bool {
	return fn.Entry != "" && fn.BuildError == "" && len(fn.Findings) == 0
}

type functionFinding struct {
	File    string
	Line    int
	What    string
	Instead string
}

// listSupabaseFunctions returns the functions in dir, by name. A folder
// whose name starts with "_" holds shared code, not a function.
func listSupabaseFunctions(dir string) ([]*supabaseFunction, error) {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no edge functions folder at %s — pass --input with your Supabase project's supabase/functions folder; if the project has no edge functions, there is nothing to import", dir)
		}
		return nil, fmt.Errorf("read %s: %w — run this from your Supabase project's root, or pass --input", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", dir, err)
	}
	open := functionsWithoutJWT(filepath.Join(dir, "..", "config.toml"))
	var fns []*supabaseFunction
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "_") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		fn := &supabaseFunction{Name: e.Name(), Dir: filepath.Join(dir, e.Name()), VerifyJWT: !open[e.Name()]}
		entry := filepath.Join(fn.Dir, resolveEntrypoint(fn.Dir))
		if _, err := os.Stat(entry); err == nil {
			fn.Entry = entry
		}
		fns = append(fns, fn)
	}
	sort.Slice(fns, func(i, j int) bool { return fns[i].Name < fns[j].Name })
	return fns, nil
}

var (
	tomlSectionRe = regexp.MustCompile(`^\s*\[\s*functions\.("?)([A-Za-z0-9_-]+)("?)\s*\]`)
	tomlAnyRe     = regexp.MustCompile(`^\s*\[`)
	tomlNoJWTRe   = regexp.MustCompile(`^\s*verify_jwt\s*=\s*false\b`)
)

// functionsWithoutJWT reads `verify_jwt = false` per function from a
// Supabase config.toml. A missing file means every function verifies.
func functionsWithoutJWT(path string) map[string]bool {
	out := map[string]bool{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	current := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if m := tomlSectionRe.FindStringSubmatch(line); m != nil {
			current = m[2]
			continue
		}
		if tomlAnyRe.MatchString(line) {
			current = ""
			continue
		}
		if current != "" && tomlNoJWTRe.MatchString(line) {
			out[current] = true
		}
	}
	return out
}

// resolveEntrypoint returns the file a function starts in: index.ts, or
// what its deno.json names as "main" / "entrypoint" — as long as that
// stays inside the function's folder.
func resolveEntrypoint(funcDir string) string {
	b, err := os.ReadFile(filepath.Join(funcDir, "deno.json"))
	if err != nil {
		return "index.ts"
	}
	var cfg struct {
		Main       string `json:"main"`
		Entrypoint string `json:"entrypoint"`
	}
	if err := json.Unmarshal(relaxJSON(b), &cfg); err != nil {
		return "index.ts"
	}
	candidate := cfg.Main
	if candidate == "" {
		candidate = cfg.Entrypoint
	}
	if !isSafeEntrypoint(candidate) {
		return "index.ts"
	}
	return candidate
}

// isSafeEntrypoint reports whether p stays inside the function's folder.
func isSafeEntrypoint(p string) bool {
	if p == "" || filepath.IsAbs(p) {
		return false
	}
	clean := filepath.Clean(p)
	return clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

// ── reading the code ────────────────────────────────────────────────

// compatGap is a call the compatibility layer doesn't have.
type compatGap struct {
	re      *regexp.Regexp
	what    string
	instead string
	// near: only a finding when one of the three lines up to this one
	// matches (a method name too common to flag on its own).
	near *regexp.Regexp
}

var compatGaps = []compatGap{
	{re: regexp.MustCompile(`\.functions\s*\.\s*invoke\s*\(`), what: "functions.invoke()",
		instead: "a function can't call the project's own API; move the shared code into a file both functions import"},
	{re: regexp.MustCompile(`\.(channel|removeChannel)\s*\(|\.realtime\b`), what: "a realtime channel",
		instead: "realtime is used from your app, not from a function"},
	{re: regexp.MustCompile("\\.select\\s*\\(\\s*[\"'`][^\"'`]*\\("), what: "select() with an embedded table",
		instead: "join the tables in SQL with ctx.db.sql, or select from a view"},
	{re: regexp.MustCompile("\\.or\\s*\\(\\s*[\"'`]"), what: "an or() filter",
		instead: "write the query as SQL with ctx.db.sql"},
	{re: regexp.MustCompile(`\.(textSearch|containedBy|overlaps|rangeGt|rangeGte|rangeLt|rangeLte|rangeAdjacent)\s*\(`), what: "a filter the compatibility layer doesn't have",
		instead: "write the query as SQL with ctx.db.sql"},
	{re: regexp.MustCompile(`\.contains\s*\(`), near: regexp.MustCompile(`\.from\s*\(|\.select\s*\(|\.eq\s*\(`), what: "a contains() filter",
		instead: "write the query as SQL with ctx.db.sql (the @> operator)"},
	{re: regexp.MustCompile(`auth\s*\.\s*admin\s*\.\s*(listUsers|createUser|updateUserById|deleteUser|inviteUserByEmail|generateLink)\b`), what: "an auth.admin call",
		instead: "manage users with the users API from your server, or with SQL on the users table through ctx.db.asService()"},
	{re: regexp.MustCompile(`auth\s*\.\s*(signInWithPassword|signInWithOtp|signInWithOAuth|signUp|resetPasswordForEmail|refreshSession|exchangeCodeForSession|verifyOtp)\b`), what: "a sign-in call",
		instead: "sign users in from your app with the Eurobase SDK"},
	{re: regexp.MustCompile(`\.(getPublicUrl|listBuckets|createBucket|getBucket|createSignedUrls)\s*\(`), what: "a storage call the compatibility layer doesn't have",
		instead: "use upload, createSignedUrl, createSignedUploadUrl and remove, or ctx.storage"},
	{re: regexp.MustCompile(`\.(download|list|move|copy)\s*\(`), near: regexp.MustCompile(`\.storage\b`), what: "a storage call the compatibility layer doesn't have",
		instead: "use upload, createSignedUrl, createSignedUploadUrl and remove, or ctx.storage"},
	{re: regexp.MustCompile(`\.schema\s*\(\s*["']`), what: "schema()",
		instead: "a project's tables are in one schema; drop the call"},
	{re: regexp.MustCompile(`\bDeno\s*\.\s*(readFile|readTextFile|writeFile|writeTextFile|readDir|openKv|connect|listen|Command|run|cron)\b`), what: "a Deno API that functions can't use",
		instead: "functions have network access only (fetch); keep files in storage and data in the database"},
	{re: regexp.MustCompile(`["']postgres(ql)?://|SUPABASE_DB_URL|deno\.land/x/postgres`), what: "a direct database connection",
		instead: "use ctx.db.sql; functions don't get a database URL"},
	{re: regexp.MustCompile(`/(rest|functions|auth|storage|realtime)/v1\b`), what: "a direct call to the Supabase API",
		instead: "there is no such URL on Eurobase; use the client (createClient) or ctx"},
	{re: regexp.MustCompile(`auth\s*\.\s*(getClaims|updateUser|admin\s*\.\s*signOut)\b`), what: "an auth call the compatibility layer doesn't have",
		instead: "use auth.getUser() for the caller; change users with the users API or SQL through ctx.db.asService()"},
	{re: regexp.MustCompile(`\bEdgeRuntime\s*\.\s*waitUntil\b`), what: "EdgeRuntime.waitUntil()",
		instead: "a function ends with its response; await the work, or hand it to a cron job"},
	// The service role key in a comparison or sent somewhere: on Supabase
	// that is how a function recognises a trusted caller (a cron job) or
	// calls the API as admin. Here the key is a random value per request
	// unless the project sets one, and the Authorization header of a call
	// isn't passed to the function.
	{re: regexp.MustCompile("(===?|!==?)[^;\n]*SERVICE_ROLE_KEY|SERVICE_ROLE_KEY[^;\n]*(===?|!==?)|Bearer[^;\n]*SERVICE_ROLE_KEY|fetch\\s*\\([^;\n]*SERVICE_ROLE_KEY"),
		what:    "the service role key compared with or sent in a request",
		instead: "to recognise a trusted caller, set your own secret (eurobase vault set …) and compare a custom header with it — the Authorization header isn't passed to a function, and without a value of your own SUPABASE_SERVICE_ROLE_KEY is a new random value in every request"},
}

var localImportRe = regexp.MustCompile(`(?:from|import)\s*\(?\s*["'](\.{1,2}/[^"']+)["']`)

// analyze reads the function's entry file and the local files it imports
// and fills Findings, Secrets and UsesServiceRole.
func (fn *supabaseFunction) analyze() {
	if fn.Entry == "" {
		return
	}
	seen := map[string]bool{}
	var sources []string
	var walk func(path string, depth int)
	walk = func(path string, depth int) {
		real, err := filepath.Abs(path)
		if err != nil || seen[real] || depth > 12 {
			return
		}
		raw, err := os.ReadFile(real)
		if err != nil {
			return
		}
		seen[real] = true
		src := string(raw)
		sources = append(sources, src)
		shown := filepath.ToSlash(real)
		if rel, err := filepath.Rel(filepath.Dir(fn.Dir), real); err == nil {
			shown = filepath.ToSlash(rel)
		}
		fn.Findings = append(fn.Findings, findCompatGaps(shown, src)...)
		for _, m := range localImportRe.FindAllStringSubmatch(src, -1) {
			next := filepath.Join(filepath.Dir(real), filepath.FromSlash(m[1]))
			if _, err := os.Stat(next); err != nil {
				for _, ext := range []string{".ts", ".js", ".tsx", ".mjs"} {
					if _, err := os.Stat(next + ext); err == nil {
						next += ext
						break
					}
				}
			}
			walk(next, depth+1)
		}
	}
	walk(fn.Entry, 0)
	names := map[string]bool{}
	for _, src := range sources {
		got, service := envNamesRead(src, "", nil)
		fn.UsesServiceRole = fn.UsesServiceRole || service
		for _, n := range got {
			names[n] = true
		}
	}
	for n := range names {
		fn.Secrets = append(fn.Secrets, n)
	}
	sort.Strings(fn.Secrets)
	if !fn.VerifyJWT && strings.Contains(strings.Join(sources, "\n"), "SUPABASE_ANON_KEY") {
		fn.Notes = append(fn.Notes, "takes calls without a signed-in user and makes a client with the anon key: that client's queries are refused when nobody is signed in (function SQL has no anon role on Eurobase) — use the service role key client for what the function does on its own behalf")
	}
}

// findCompatGaps scans one file. Lines that are only a comment are
// skipped; a call inside a string would still be reported, which errs on
// the side of looking.
func findCompatGaps(file, src string) []functionFinding {
	var out []functionFinding
	lines := strings.Split(src, "\n")
	inBlock := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if inBlock {
			if strings.Contains(trimmed, "*/") {
				inBlock = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "/*") {
			inBlock = !strings.Contains(trimmed, "*/")
			continue
		}
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
			continue
		}
		for _, gap := range compatGaps {
			if !gap.re.MatchString(line) {
				continue
			}
			if gap.near != nil {
				from := i - 3
				if from < 0 {
					from = 0
				}
				if !gap.near.MatchString(strings.Join(lines[from:i+1], "\n")) {
					continue
				}
			}
			out = append(out, functionFinding{File: file, Line: i + 1, What: gap.what, Instead: gap.instead})
		}
	}
	return out
}
