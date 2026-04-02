package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// systemTables are platform-managed tables hidden from user listing.
var systemTables = map[string]bool{
	"users":           true,
	"refresh_tokens":  true,
	"storage_objects": true,
	"email_tokens":    true,
	"vault_secrets":   true,
}

// DbCmd returns the parent "db" command.
func DbCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Database operations",
	}
	cmd.AddCommand(dbTablesCmd())
	cmd.AddCommand(dbSchemaCmd())
	cmd.AddCommand(dbQueryCmd())
	cmd.AddCommand(dbDumpCmd())
	return cmd
}

func dbTablesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tables",
		Short: "List database tables",
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

			data, err := client.Get("/platform/projects/" + cfg.ActiveProject + "/schema")
			if err != nil {
				return err
			}

			var tables []struct {
				Name       string `json:"name"`
				ColumnCount int   `json:"column_count"`
				RowCount   int64  `json:"row_count"`
				RLS        bool   `json:"rls_enabled"`
			}
			if err := json.Unmarshal(data, &tables); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				var filtered []interface{}
				for _, t := range tables {
					if !systemTables[t.Name] {
						filtered = append(filtered, t)
					}
				}
				PrintJSON(filtered)
				return nil
			}

			headers := []string{"Name", "Columns", "Rows", "RLS"}
			var rows [][]string
			for _, t := range tables {
				if systemTables[t.Name] {
					continue
				}
				rls := "off"
				if t.RLS {
					rls = colorGreen + "on" + colorReset
				}
				rows = append(rows, []string{
					t.Name,
					fmt.Sprintf("%d", t.ColumnCount),
					fmt.Sprintf("%d", t.RowCount),
					rls,
				})
			}
			PrintTable(headers, rows)
			return nil
		},
	}
}

func dbSchemaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "schema [table]",
		Short: "Show table schema details",
		Args:  cobra.MaximumNArgs(1),
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

			if len(args) == 1 {
				// Show columns for a specific table — filter from full schema
				data, err := client.Get("/platform/projects/" + cfg.ActiveProject + "/schema")
				if err != nil {
					return err
				}

				var allTables []struct {
					Name    string `json:"name"`
					Columns []struct {
						Name     string `json:"name"`
						Type     string `json:"data_type"`
						Nullable bool   `json:"is_nullable"`
						Default  string `json:"default_value"`
						PK       bool   `json:"is_primary_key"`
					} `json:"columns"`
				}
				if err := json.Unmarshal(data, &allTables); err != nil {
					return fmt.Errorf("parsing response: %w", err)
				}

				var columns []struct {
					Name     string `json:"name"`
					Type     string `json:"data_type"`
					Nullable bool   `json:"is_nullable"`
					Default  string `json:"default_value"`
					PK       bool   `json:"is_primary_key"`
				}
				for _, t := range allTables {
					if t.Name == args[0] {
						columns = t.Columns
						break
					}
				}
				if columns == nil {
					return fmt.Errorf("table %q not found", args[0])
				}

				jsonOut, _ := cmd.Flags().GetBool("json")
				if jsonOut {
					PrintJSON(columns)
					return nil
				}

				fmt.Printf("%sTable: %s%s\n\n", colorBold, args[0], colorReset)
				headers := []string{"Column", "Type", "Nullable", "Default", "PK"}
				var rows [][]string
				for _, c := range columns {
					nullable := "NO"
					if c.Nullable {
						nullable = "YES"
					}
					pk := ""
					if c.PK {
						pk = "✓"
					}
					rows = append(rows, []string{c.Name, c.Type, nullable, c.Default, pk})
				}
				PrintTable(headers, rows)
				return nil
			}

			// Show all tables with their columns
			data, err := client.Get("/platform/projects/" + cfg.ActiveProject + "/schema")
			if err != nil {
				return err
			}

			var tables []struct {
				Name    string `json:"name"`
				Columns []struct {
					Name string `json:"name"`
					Type string `json:"type"`
				} `json:"columns"`
			}
			if err := json.Unmarshal(data, &tables); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				PrintJSON(tables)
				return nil
			}

			for _, t := range tables {
				if systemTables[t.Name] {
					continue
				}
				fmt.Printf("%s%s%s\n", colorBold, t.Name, colorReset)
				for _, c := range t.Columns {
					fmt.Printf("  %-30s %s\n", c.Name, c.Type)
				}
				fmt.Println()
			}
			return nil
		},
	}
}

func dbQueryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "query <sql>",
		Short: "Execute a SQL query",
		Args:  cobra.ExactArgs(1),
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

			body := map[string]string{"sql": args[0]}
			data, err := client.Post("/platform/projects/"+cfg.ActiveProject+"/data/sql", body)
			if err != nil {
				return err
			}

			var result struct {
				Columns     []string                   `json:"columns"`
				Rows        []map[string]interface{}    `json:"rows"`
				RowCount    int                         `json:"row_count"`
				ExecTimeMs  float64                     `json:"execution_time_ms"`
			}
			if err := json.Unmarshal(data, &result); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				PrintJSON(result)
				return nil
			}

			fmt.Printf("%d row(s) · %.1fms\n\n", result.RowCount, result.ExecTimeMs)

			if len(result.Columns) == 0 {
				PrintSuccess("Query executed successfully (no results)")
				return nil
			}

			var rows [][]string
			for _, row := range result.Rows {
				var cols []string
				for _, c := range result.Columns {
					v := row[c]
					if v == nil {
						cols = append(cols, "NULL")
					} else {
						cols = append(cols, fmt.Sprintf("%v", v))
					}
				}
				rows = append(rows, cols)
			}
			PrintTable(result.Columns, rows)
			return nil
		},
	}
}

func dbDumpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dump",
		Short: "Dump database schema as formatted text",
		Long:  "Output the database schema for the active project. Redirect to a file with > dump.txt",
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

			data, err := client.Get("/platform/projects/" + cfg.ActiveProject + "/schema")
			if err != nil {
				return err
			}

			var tables []struct {
				Name    string `json:"name"`
				Columns []struct {
					Name     string `json:"name"`
					Type     string `json:"type"`
					Nullable bool   `json:"nullable"`
					Default  string `json:"default_value"`
					PK       bool   `json:"is_primary_key"`
				} `json:"columns"`
				RLS bool `json:"rls_enabled"`
			}
			if err := json.Unmarshal(data, &tables); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			fmt.Println("-- Eurobase schema dump")
			fmt.Printf("-- Project: %s (%s)\n", cfg.ProjectSlug, cfg.ActiveProject)
			fmt.Println()

			for _, t := range tables {
				if systemTables[t.Name] {
					continue
				}
				fmt.Printf("-- Table: %s", t.Name)
				if t.RLS {
					fmt.Print(" (RLS enabled)")
				}
				fmt.Println()

				var pkCols []string
				for _, c := range t.Columns {
					nullable := "NOT NULL"
					if c.Nullable {
						nullable = "NULL"
					}
					def := ""
					if c.Default != "" {
						def = " DEFAULT " + c.Default
					}
					pk := ""
					if c.PK {
						pk = " [PK]"
						pkCols = append(pkCols, c.Name)
					}
					fmt.Printf("  %-30s %-20s %s%s%s\n", c.Name, c.Type, nullable, def, pk)
				}
				if len(pkCols) > 0 {
					fmt.Printf("  PRIMARY KEY (%s)\n", strings.Join(pkCols, ", "))
				}
				fmt.Println()
			}

			return nil
		},
	}
}
