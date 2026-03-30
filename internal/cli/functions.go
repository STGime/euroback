package cli

import (
	"encoding/json"
	"fmt"
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
				Name     string `json:"name"`
				Language string `json:"language"`
				Returns  string `json:"return_type"`
			}
			if err := json.Unmarshal(data, &functions); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				PrintJSON(functions)
				return nil
			}

			headers := []string{"Name", "Language", "Returns"}
			var rows [][]string
			for _, f := range functions {
				rows = append(rows, []string{f.Name, f.Language, f.Returns})
			}
			PrintTable(headers, rows)
			return nil
		},
	}
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
