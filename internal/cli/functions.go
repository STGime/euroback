package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"

	"github.com/spf13/cobra"
)

// FunctionsCmd returns the parent "functions" command.
func FunctionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "functions",
		Aliases: []string{"fn"},
		Short:   "Manage RPC functions",
	}
	cmd.AddCommand(functionsListCmd())
	cmd.AddCommand(functionsGetCmd())
	cmd.AddCommand(functionsCreateCmd())
	cmd.AddCommand(functionsDeleteCmd())
	return cmd
}

func functionsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List database functions",
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

			data, err := client.Get("/platform/projects/" + cfg.ActiveProject + "/schema/functions")
			if err != nil {
				return err
			}

			var functions []struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
				Language  string `json:"language"`
				Returns   string `json:"return_type"`
				Security  string `json:"security"`
			}
			if err := json.Unmarshal(data, &functions); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				PrintJSON(functions)
				return nil
			}

			headers := []string{"Name", "Arguments", "Language", "Returns", "Security"}
			var rows [][]string
			for _, f := range functions {
				rows = append(rows, []string{f.Name, f.Arguments, f.Language, f.Returns, f.Security})
			}
			PrintTable(headers, rows)
			return nil
		},
	}
}

// functionsGetCmd prints a function's definition (#820): the full
// CREATE OR REPLACE FUNCTION statement, ready to edit or save to a file.
func functionsGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <name>",
		Short: "Print a database function's definition",
		Long: "Print the CREATE OR REPLACE FUNCTION statement of a database function.\n" +
			"With several functions of that name, pick one with --args (the Arguments column of `functions list`).\n" +
			"Save it with: eurobase functions get <name> > <name>.sql",
		Args: cobra.ExactArgs(1),
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

			path := "/platform/projects/" + cfg.ActiveProject + "/schema/functions/" + url.PathEscape(args[0])
			if cmd.Flags().Changed("args") {
				fnArgs, _ := cmd.Flags().GetString("args")
				path += "?args=" + url.QueryEscape(fnArgs)
			}
			data, err := client.Get(path)
			if err != nil {
				return err
			}

			var fn struct {
				Name       string `json:"name"`
				Arguments  string `json:"arguments"`
				Language   string `json:"language"`
				Returns    string `json:"return_type"`
				Security   string `json:"security"`
				Definition string `json:"definition"`
				Platform   bool   `json:"platform,omitempty"`
			}
			if err := json.Unmarshal(data, &fn); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}
			if jsonOut, _ := cmd.Flags().GetBool("json"); jsonOut {
				PrintJSON(fn)
				return nil
			}
			if fn.Platform {
				fmt.Fprintln(cmd.OutOrStdout(), "-- Platform helper: managed by Eurobase, the same in every project. Don't change it.")
			}
			fmt.Fprintln(cmd.OutOrStdout(), fn.Definition)
			return nil
		},
	}
	cmd.Flags().String("args", "", `argument list of the overload to show, e.g. "team_id uuid"`)
	return cmd
}

func functionsCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a database function",
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

			file, _ := cmd.Flags().GetString("file")
			language, _ := cmd.Flags().GetString("language")
			returns, _ := cmd.Flags().GetString("returns")

			var body string
			if file != "" {
				content, err := os.ReadFile(file)
				if err != nil {
					return fmt.Errorf("reading file: %w", err)
				}
				body = string(content)
			} else {
				// Read from stdin
				PrintWarning("Reading function body from stdin (press Ctrl+D when done):")
				content, err := os.ReadFile("/dev/stdin")
				if err != nil {
					return fmt.Errorf("reading stdin: %w", err)
				}
				body = string(content)
			}

			payload := map[string]string{
				"name":        args[0],
				"body":        body,
				"language":    language,
				"return_type": returns,
			}

			_, err = client.Post("/platform/projects/"+cfg.ActiveProject+"/schema/functions", payload)
			if err != nil {
				return err
			}

			PrintSuccess(fmt.Sprintf("Function %q created", args[0]))
			return nil
		},
	}
	cmd.Flags().StringP("file", "f", "", "SQL file containing function body")
	cmd.Flags().StringP("language", "l", "plpgsql", "Function language (sql, plpgsql)")
	cmd.Flags().StringP("returns", "r", "void", "Return type")
	return cmd
}

func functionsDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a database function",
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

			_, err = client.Delete("/platform/projects/" + cfg.ActiveProject + "/schema/functions/" + args[0])
			if err != nil {
				return err
			}

			PrintSuccess(fmt.Sprintf("Function %q deleted", args[0]))
			return nil
		},
	}
}
