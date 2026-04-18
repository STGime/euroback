package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// MigrationsCmd returns the parent "migrations" command.
func MigrationsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "migrations",
		Aliases: []string{"migrate"},
		Short:   "Manage database migrations",
	}
	cmd.AddCommand(migrationsCreateCmd())
	cmd.AddCommand(migrationsUpCmd())
	cmd.AddCommand(migrationsDownCmd())
	cmd.AddCommand(migrationsStatusCmd())
	return cmd
}

func migrationsCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new migration file pair",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.ReplaceAll(strings.ToLower(args[0]), " ", "_")
			dir := "migrations"

			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("creating migrations dir: %w", err)
			}

			// Determine next sequence number
			entries, _ := os.ReadDir(dir)
			maxSeq := 0
			for _, e := range entries {
				if len(e.Name()) >= 6 {
					var seq int
					if _, err := fmt.Sscanf(e.Name(), "%06d", &seq); err == nil && seq > maxSeq {
						maxSeq = seq
					}
				}
			}
			seq := maxSeq + 1

			upFile := filepath.Join(dir, fmt.Sprintf("%06d_%s.up.sql", seq, name))
			downFile := filepath.Join(dir, fmt.Sprintf("%06d_%s.down.sql", seq, name))

			ts := time.Now().Format("2006-01-02 15:04:05")
			upContent := fmt.Sprintf("-- Migration: %s\n-- Created: %s\n\n", name, ts)
			downContent := fmt.Sprintf("-- Rollback: %s\n-- Created: %s\n\n", name, ts)

			if err := os.WriteFile(upFile, []byte(upContent), 0644); err != nil {
				return fmt.Errorf("writing up migration: %w", err)
			}
			if err := os.WriteFile(downFile, []byte(downContent), 0644); err != nil {
				return fmt.Errorf("writing down migration: %w", err)
			}

			PrintSuccess(fmt.Sprintf("Created migration #%d: %s", seq, name))
			fmt.Printf("  %s\n", upFile)
			fmt.Printf("  %s\n", downFile)
			return nil
		},
	}
}

func migrationsUpCmd() *cobra.Command {
	var dryRun bool
	var dbURL string
	cmd := &cobra.Command{
		Use:   "up [N]",
		Short: "Run pending platform migrations against a local/dev database",
		Long: `Run pending migrations against the database pointed to by --database or
$DATABASE_URL. The CLI does NOT fetch a connection string from the gateway —
platform migrations are a deploy/development concern and must be run with a
credential you already hold (local dev DB, or the migrator role in CI/CD).

To change the structure of your own tenant's tables, use the console schema
editor or the /v1/db DDL endpoints; you do not need DATABASE_URL for that.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			connStr := dbURL
			if connStr == "" {
				connStr = os.Getenv("DATABASE_URL")
			}
			if connStr == "" {
				PrintError("DATABASE_URL is not set and --database was not provided.")
				fmt.Println("Platform migrations are run by the deploy pipeline or against a local dev DB.")
				fmt.Println("Set DATABASE_URL in your shell or pass --database <url>.")
				return fmt.Errorf("missing database url")
			}

			migrateArgs := []string{"-path", "./migrations", "-database", connStr, "up"}
			if len(args) > 0 {
				migrateArgs = append(migrateArgs, args[0])
			}

			if dryRun {
				fmt.Println("Run the following command:")
				fmt.Printf("  migrate %s\n", strings.Join(migrateArgs, " "))
				return nil
			}

			migrateBin, err := exec.LookPath("migrate")
			if err != nil {
				PrintWarning("'migrate' CLI not found in PATH. Install it:")
				fmt.Println("  brew install golang-migrate")
				fmt.Println("\nOr run manually:")
				fmt.Printf("  migrate %s\n", strings.Join(migrateArgs, " "))
				return nil
			}

			c := exec.Command(migrateBin, migrateArgs...)
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			if err := c.Run(); err != nil {
				return fmt.Errorf("migrate failed: %w", err)
			}
			PrintSuccess("Migrations applied successfully")
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the command instead of running it")
	cmd.Flags().StringVar(&dbURL, "database", "", "Database URL (overrides API lookup and $DATABASE_URL)")
	return cmd
}

func migrationsDownCmd() *cobra.Command {
	var dryRun bool
	var dbURL string
	cmd := &cobra.Command{
		Use:   "down [N]",
		Short: "Roll back platform migrations against a local/dev database",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			connStr := dbURL
			if connStr == "" {
				connStr = os.Getenv("DATABASE_URL")
			}
			if connStr == "" {
				PrintError("DATABASE_URL is not set and --database was not provided.")
				fmt.Println("Set DATABASE_URL in your shell or pass --database <url>.")
				return fmt.Errorf("missing database url")
			}

			steps := "1"
			if len(args) > 0 {
				steps = args[0]
			}
			migrateArgs := []string{"-path", "./migrations", "-database", connStr, "down", steps}

			if dryRun {
				fmt.Println("Run the following command:")
				fmt.Printf("  migrate %s\n", strings.Join(migrateArgs, " "))
				return nil
			}

			migrateBin, err := exec.LookPath("migrate")
			if err != nil {
				PrintWarning("'migrate' CLI not found in PATH. Install it:")
				fmt.Println("  brew install golang-migrate")
				fmt.Println("\nOr run manually:")
				fmt.Printf("  migrate %s\n", strings.Join(migrateArgs, " "))
				return nil
			}

			c := exec.Command(migrateBin, migrateArgs...)
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			if err := c.Run(); err != nil {
				return fmt.Errorf("migrate failed: %w", err)
			}
			PrintSuccess("Migration rolled back successfully")
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the command instead of running it")
	cmd.Flags().StringVar(&dbURL, "database", "", "Database URL (overrides API lookup and $DATABASE_URL)")
	return cmd
}

func migrationsStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "List local migration files",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "migrations"
			entries, err := os.ReadDir(dir)
			if err != nil {
				if os.IsNotExist(err) {
					PrintWarning("No migrations directory found. Run `eurobase migrations create <name>` to start.")
					return nil
				}
				return err
			}

			var files []string
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".up.sql") {
					files = append(files, e.Name())
				}
			}
			sort.Strings(files)

			if len(files) == 0 {
				PrintWarning("No migrations found.")
				return nil
			}

			headers := []string{"#", "Name", "File"}
			var rows [][]string
			for i, f := range files {
				name := strings.TrimSuffix(f, ".up.sql")
				rows = append(rows, []string{fmt.Sprintf("%d", i+1), name, f})
			}
			PrintTable(headers, rows)

			fmt.Printf("\n%d migration(s)\n", len(files))
			return nil
		},
	}
}

