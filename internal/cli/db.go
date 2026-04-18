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
	cmd.AddCommand(dbCreateTableCmd())
	cmd.AddCommand(dbDropTableCmd())
	cmd.AddCommand(dbAddColumnCmd())
	cmd.AddCommand(dbDropColumnCmd())
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

// columnDef is the request shape accepted by the gateway DDL handler.
// Mirrors query.ColumnDefinition but duplicated here so the CLI package
// does not import the gateway server packages.
type columnDef struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Nullable   bool   `json:"nullable,omitempty"`
	Default    string `json:"default_value,omitempty"`
	PrimaryKey bool   `json:"primary_key,omitempty"`
}

// parseColumnSpec parses a "name:type[:pk][:null][:default=<val>]" spec.
// Examples: "id:uuid:pk", "title:text", "note:text:null", "created_at:timestamptz:default=now()".
func parseColumnSpec(spec string) (columnDef, error) {
	parts := strings.Split(spec, ":")
	if len(parts) < 2 {
		return columnDef{}, fmt.Errorf("column %q must be name:type[:pk][:null][:default=VAL]", spec)
	}
	col := columnDef{Name: parts[0], Type: parts[1]}
	for _, mod := range parts[2:] {
		switch {
		case mod == "pk" || mod == "primary_key":
			col.PrimaryKey = true
		case mod == "null" || mod == "nullable":
			col.Nullable = true
		case strings.HasPrefix(mod, "default="):
			col.Default = strings.TrimPrefix(mod, "default=")
		default:
			return columnDef{}, fmt.Errorf("unknown modifier %q in column %q", mod, spec)
		}
	}
	return col, nil
}

func dbCreateTableCmd() *cobra.Command {
	var (
		rlsPreset  string
		userIDCol  string
		disableRLS bool
	)
	cmd := &cobra.Command{
		Use:   "create-table <name> <col:type[:mod...]>...",
		Short: "Create a new table in your tenant schema",
		Long: `Create a table in your tenant schema. RLS is enabled by default with an
auto-detected preset (owner_access if an owner-like column is present).
Pass --disable-rls ONLY for genuinely public data; the response will
include a warning so you see that the table is unprotected.

Columns are specified as name:type[:modifier...]. Modifiers:
  pk           — primary key
  null         — allow NULL (default: NOT NULL)
  default=VAL  — default expression

Example:
  eurobase db create-table posts id:uuid:pk title:text body:text:null \
    created_at:timestamptz:default=now()`,
		Args: cobra.MinimumNArgs(2),
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

			name := args[0]
			var cols []columnDef
			for _, spec := range args[1:] {
				c, err := parseColumnSpec(spec)
				if err != nil {
					return err
				}
				cols = append(cols, c)
			}

			body := map[string]interface{}{
				"name":    name,
				"columns": cols,
			}
			if rlsPreset != "" {
				body["rls_preset"] = rlsPreset
			}
			if userIDCol != "" {
				body["rls_user_id_column"] = userIDCol
			}
			if disableRLS {
				body["disable_rls"] = true
			}

			data, err := client.Post("/platform/projects/"+cfg.ActiveProject+"/schema/tables/", body)
			if err != nil {
				return err
			}

			var resp struct {
				Status     string `json:"status"`
				Table      string `json:"table"`
				RLSPreset  string `json:"rls_preset"`
				RLSEnabled bool   `json:"rls_enabled"`
				Warning    string `json:"warning,omitempty"`
			}
			if err := json.Unmarshal(data, &resp); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			PrintSuccess(fmt.Sprintf("Table %q created", resp.Table))
			if resp.RLSEnabled {
				if resp.RLSPreset != "" {
					fmt.Printf("  RLS: on  preset=%s\n", resp.RLSPreset)
				} else {
					fmt.Printf("  RLS: on  (no policy — deny-all to end-users)\n")
				}
			}
			if resp.Warning != "" {
				PrintWarning(resp.Warning)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&rlsPreset, "rls-preset", "", "RLS preset: owner_access, public_read_owner_write, authenticated_read_owner_write, full_access, read_only, or none")
	cmd.Flags().StringVar(&userIDCol, "rls-user-column", "", "Column to use as the owner identifier for RLS (default: auto-detect)")
	cmd.Flags().BoolVar(&disableRLS, "disable-rls", false, "Turn RLS OFF — use only for genuinely public data")
	return cmd
}

func dbDropTableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "drop-table <name>",
		Short: "Drop a table from your tenant schema",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			if err := RequireProject(cfg); err != nil {
				return err
			}
			if systemTables[args[0]] {
				return fmt.Errorf("%q is a platform-managed table and cannot be dropped", args[0])
			}
			client, err := NewClientFromConfig()
			if err != nil {
				return err
			}
			if _, err := client.Delete("/platform/projects/" + cfg.ActiveProject + "/schema/tables/" + args[0]); err != nil {
				return err
			}
			PrintSuccess(fmt.Sprintf("Table %q dropped", args[0]))
			return nil
		},
	}
}

func dbAddColumnCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add-column <table> <col:type[:mod...]>",
		Short: "Add a column to an existing table",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			if err := RequireProject(cfg); err != nil {
				return err
			}
			col, err := parseColumnSpec(args[1])
			if err != nil {
				return err
			}
			client, err := NewClientFromConfig()
			if err != nil {
				return err
			}
			body := map[string]interface{}{
				"name":     col.Name,
				"type":     col.Type,
				"nullable": col.Nullable,
			}
			if col.Default != "" {
				body["default_value"] = col.Default
			}
			if _, err := client.Post("/platform/projects/"+cfg.ActiveProject+"/schema/tables/"+args[0]+"/columns", body); err != nil {
				return err
			}
			PrintSuccess(fmt.Sprintf("Column %q added to %q", col.Name, args[0]))
			return nil
		},
	}
}

func dbDropColumnCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "drop-column <table> <column>",
		Short: "Drop a column from an existing table",
		Args:  cobra.ExactArgs(2),
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
			if _, err := client.Delete("/platform/projects/" + cfg.ActiveProject + "/schema/tables/" + args[0] + "/columns/" + args[1]); err != nil {
				return err
			}
			PrintSuccess(fmt.Sprintf("Column %q dropped from %q", args[1], args[0]))
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
