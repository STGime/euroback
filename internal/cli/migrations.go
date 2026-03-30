package cli

import (
	"encoding/json"
	"fmt"
	"os"
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
	return &cobra.Command{
		Use:   "up",
		Short: "Show command to run migrations up",
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

			connStr, err := getConnectionString(client, cfg)
			if err != nil {
				PrintWarning("Could not fetch connection string: " + err.Error())
				fmt.Println("\nRun manually:")
				fmt.Println("  migrate -path ./migrations -database \"$DATABASE_URL\" up")
				return nil
			}

			fmt.Println("Run the following command:")
			fmt.Printf("  migrate -path ./migrations -database \"%s\" up\n", connStr)
			return nil
		},
	}
}

func migrationsDownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down",
		Short: "Show command to run migrations down",
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

			connStr, err := getConnectionString(client, cfg)
			if err != nil {
				PrintWarning("Could not fetch connection string: " + err.Error())
				fmt.Println("\nRun manually:")
				fmt.Println("  migrate -path ./migrations -database \"$DATABASE_URL\" down 1")
				return nil
			}

			fmt.Println("Run the following command:")
			fmt.Printf("  migrate -path ./migrations -database \"%s\" down 1\n", connStr)
			return nil
		},
	}
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

func getConnectionString(client *APIClient, cfg *Config) (string, error) {
	data, err := client.Get("/platform/projects/" + cfg.ActiveProject + "/connect")
	if err != nil {
		return "", err
	}
	var conn struct {
		DatabaseURL string `json:"database_url"`
	}
	if err := json.Unmarshal(data, &conn); err != nil {
		return "", err
	}
	return conn.DatabaseURL, nil
}
