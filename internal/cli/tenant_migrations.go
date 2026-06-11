package cli

// Tenant-level schema migrations (#190): `eurobase migrations` applies
// versioned .sql files from the project's migrations/ directory against
// the ACTIVE PROJECT's schema, via POST /platform/projects/{id}/migrations.
//
// File convention: migrations/<version>_<name>.sql where <version> is a
// positive integer (e.g. 0001_create_listings.sql). Files apply in
// version order; applied versions are recorded server-side, so `up` is
// idempotent and safe to run in CI. Down migrations are not supported —
// write a new forward migration instead.
//
// (The PLATFORM migration tooling that previously owned this command
// name lives under `eurobase admin migrations`.)

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

const tenantMigrationsDir = "migrations"

var tenantMigFileRe = regexp.MustCompile(`^(\d+)_([a-zA-Z0-9_-]+)\.sql$`)

// localMigration is one migrations/*.sql file on disk.
type localMigration struct {
	Version int64
	Name    string
	Path    string
}

// TenantMigrationsCmd returns the user-facing "migrations" command group.
func TenantMigrationsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "migrations",
		Aliases: []string{"migrate"},
		Short:   "Versioned schema migrations for the active project",
		Long: `Apply versioned SQL migrations from ./migrations/*.sql to the active
project's database schema.

Files are named <version>_<name>.sql (version = positive integer) and
apply in version order. Applied versions are tracked by the platform, so
"up" only runs what's pending and is safe to re-run (also from CI).

Example:
  eurobase migrations new create_listings     # writes migrations/0001_create_listings.sql
  $EDITOR migrations/0001_create_listings.sql
  eurobase migrations up                       # applies pending migrations
  eurobase migrations status                   # local files vs applied versions`,
	}
	cmd.AddCommand(tenantMigrationsNewCmd())
	cmd.AddCommand(tenantMigrationsUpCmd())
	cmd.AddCommand(tenantMigrationsStatusCmd())
	return cmd
}

func tenantMigrationsNewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "new <name>",
		Short: "Create the next numbered migration file in ./migrations",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(args[0])), " ", "_")
			if !regexp.MustCompile(`^[a-z0-9_-]+$`).MatchString(name) {
				return fmt.Errorf("invalid name %q — use letters, digits, underscores, hyphens", name)
			}
			if err := os.MkdirAll(tenantMigrationsDir, 0o755); err != nil {
				return fmt.Errorf("creating %s/: %w", tenantMigrationsDir, err)
			}

			local, err := loadLocalMigrations()
			if err != nil {
				return err
			}
			next := int64(1)
			if len(local) > 0 {
				next = local[len(local)-1].Version + 1
			}

			path := filepath.Join(tenantMigrationsDir, fmt.Sprintf("%04d_%s.sql", next, name))
			content := fmt.Sprintf(`-- Migration %04d: %s
-- Runs inside your project schema in a single transaction. Use
-- unqualified table names; RLS helpers public.is_service_role() and
-- public.current_end_user_id() are available for policies.

`, next, name)
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return fmt.Errorf("writing %s: %w", path, err)
			}
			PrintSuccess("Created " + path)
			return nil
		},
	}
}

func tenantMigrationsUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "up",
		Short: "Apply pending migrations to the active project",
		RunE: func(cmd *cobra.Command, args []string) error {
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

			local, err := loadLocalMigrations()
			if err != nil {
				return err
			}
			if len(local) == 0 {
				return fmt.Errorf("no migrations found — create one with `eurobase migrations new <name>` (files go in ./%s)", tenantMigrationsDir)
			}

			applied, err := fetchAppliedVersions(client, cfg.ActiveProject)
			if err != nil {
				return err
			}

			pending := 0
			for _, m := range local {
				if applied[m.Version] {
					continue
				}
				pending++
				sqlBytes, err := os.ReadFile(m.Path)
				if err != nil {
					return fmt.Errorf("reading %s: %w", m.Path, err)
				}
				fmt.Printf("Applying %04d_%s ...\n", m.Version, m.Name)
				body := map[string]any{"version": m.Version, "name": m.Name, "sql": string(sqlBytes)}
				if _, err := client.Post("/platform/projects/"+cfg.ActiveProject+"/migrations", body); err != nil {
					return fmt.Errorf("migration %04d_%s failed (nothing from it was applied — it runs in a transaction): %w", m.Version, m.Name, err)
				}
			}

			if pending == 0 {
				PrintSuccess(fmt.Sprintf("Already up to date on %s (%d applied)", ProjectLabel(cfg), len(local)))
				return nil
			}
			PrintSuccess(fmt.Sprintf("Applied %d migration(s) to %s", pending, ProjectLabel(cfg)))
			return nil
		},
	}
}

func tenantMigrationsStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show local migrations vs what the active project has applied",
		RunE: func(cmd *cobra.Command, args []string) error {
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

			local, err := loadLocalMigrations()
			if err != nil {
				return err
			}
			applied, err := fetchAppliedVersions(client, cfg.ActiveProject)
			if err != nil {
				return err
			}

			fmt.Printf("%sProject:%s %s\n\n", colorBold, colorReset, ProjectLabel(cfg))
			headers := []string{"Version", "Name", "Status"}
			var rows [][]string
			seen := map[int64]bool{}
			for _, m := range local {
				status := "pending"
				if applied[m.Version] {
					status = "applied"
				}
				rows = append(rows, []string{fmt.Sprintf("%04d", m.Version), m.Name, status})
				seen[m.Version] = true
			}
			// Versions applied on the server but missing locally — worth
			// surfacing (another machine or the console applied them).
			var remoteOnly []int64
			for v := range applied {
				if !seen[v] {
					remoteOnly = append(remoteOnly, v)
				}
			}
			sort.Slice(remoteOnly, func(i, j int) bool { return remoteOnly[i] < remoteOnly[j] })
			for _, v := range remoteOnly {
				rows = append(rows, []string{fmt.Sprintf("%04d", v), "(no local file)", "applied"})
			}
			PrintTable(headers, rows)
			return nil
		},
	}
}

// loadLocalMigrations reads migrations/*.sql, validates naming, and
// returns them sorted by version with duplicate versions rejected.
func loadLocalMigrations() ([]localMigration, error) {
	entries, err := os.ReadDir(tenantMigrationsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s/: %w", tenantMigrationsDir, err)
	}

	byVersion := map[int64]string{}
	var out []localMigration
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fname := e.Name()
		if !strings.HasSuffix(fname, ".sql") {
			continue
		}
		m := tenantMigFileRe.FindStringSubmatch(fname)
		if m == nil {
			return nil, fmt.Errorf("unexpected file %s/%s — name migrations <version>_<name>.sql (e.g. 0001_create_listings.sql)", tenantMigrationsDir, fname)
		}
		version, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("invalid version in %s/%s", tenantMigrationsDir, fname)
		}
		if prev, dup := byVersion[version]; dup {
			return nil, fmt.Errorf("duplicate migration version %d: %s and %s", version, prev, fname)
		}
		byVersion[version] = fname
		out = append(out, localMigration{Version: version, Name: m[2], Path: filepath.Join(tenantMigrationsDir, fname)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// fetchAppliedVersions returns the set of versions the platform has
// recorded as applied for the project.
func fetchAppliedVersions(client *APIClient, projectID string) (map[int64]bool, error) {
	data, err := client.Get("/platform/projects/" + projectID + "/migrations")
	if err != nil {
		return nil, fmt.Errorf("fetching applied migrations: %w", err)
	}
	var resp struct {
		Migrations []struct {
			Version int64 `json:"version"`
		} `json:"migrations"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	applied := map[int64]bool{}
	for _, m := range resp.Migrations {
		applied[m.Version] = true
	}
	return applied, nil
}
