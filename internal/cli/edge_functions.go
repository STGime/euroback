package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// EdgeFunctionsCmd returns the "edge-functions" command group.
func EdgeFunctionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "edge-functions",
		Aliases: []string{"ef"},
		Short:   "Manage Edge Functions (serverless TypeScript/JavaScript)",
	}
	cmd.AddCommand(efListCmd())
	cmd.AddCommand(efDeployCmd())
	cmd.AddCommand(efGetCmd())
	cmd.AddCommand(efDeleteCmd())
	cmd.AddCommand(efLogsCmd())
	cmd.AddCommand(efInvokeCmd())
	return cmd
}

func efListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List edge functions",
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

			data, err := client.Get("/platform/projects/" + cfg.ActiveProject + "/functions")
			if err != nil {
				return err
			}

			var fns []struct {
				Name      string `json:"name"`
				Status    string `json:"status"`
				Version   int    `json:"version"`
				VerifyJWT bool   `json:"verify_jwt"`
				CreatedAt string `json:"created_at"`
				UpdatedAt string `json:"updated_at"`
			}
			if err := json.Unmarshal(data, &fns); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				PrintJSON(fns)
				return nil
			}

			headers := []string{"Name", "Status", "Version", "JWT Required", "Updated"}
			var rows [][]string
			for _, f := range fns {
				jwt := "no"
				if f.VerifyJWT {
					jwt = "yes"
				}
				rows = append(rows, []string{
					f.Name,
					f.Status,
					fmt.Sprintf("v%d", f.Version),
					jwt,
					f.UpdatedAt,
				})
			}
			PrintTable(headers, rows)
			return nil
		},
	}
}

func efDeployCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy <name>",
		Short: "Deploy an edge function from a local file",
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
			noJWT, _ := cmd.Flags().GetBool("no-verify-jwt")

			var code string
			if file != "" {
				content, err := os.ReadFile(file)
				if err != nil {
					return fmt.Errorf("reading file: %w", err)
				}
				code = string(content)
			} else {
				// Try default path: functions/<name>.ts
				defaultPath := "functions/" + args[0] + ".ts"
				content, err := os.ReadFile(defaultPath)
				if err != nil {
					// Try .js
					defaultPath = "functions/" + args[0] + ".js"
					content, err = os.ReadFile(defaultPath)
					if err != nil {
						return fmt.Errorf("no file found — provide --file or place code at functions/%s.ts", args[0])
					}
				}
				code = string(content)
				fmt.Printf("Deploying from %s\n", defaultPath)
			}

			verifyJWT := true
			if noJWT {
				verifyJWT = false
			}

			// Try update first, create if not found.
			payload := map[string]interface{}{
				"code":       code,
				"verify_jwt": verifyJWT,
			}

			_, err = client.Put("/platform/projects/"+cfg.ActiveProject+"/functions/"+args[0], payload)
			if err != nil {
				// Function doesn't exist yet — create it.
				createPayload := map[string]interface{}{
					"name":       args[0],
					"code":       code,
					"verify_jwt": verifyJWT,
				}
				_, createErr := client.Post("/platform/projects/"+cfg.ActiveProject+"/functions", createPayload)
				if createErr != nil {
					return createErr
				}
				PrintSuccess(fmt.Sprintf("Function %q created and deployed", args[0]))
				return nil
			}

			PrintSuccess(fmt.Sprintf("Function %q deployed", args[0]))
			return nil
		},
	}
	cmd.Flags().StringP("file", "f", "", "Path to the function file")
	cmd.Flags().Bool("no-verify-jwt", false, "Allow unauthenticated invocations")
	return cmd
}

func efGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <name>",
		Short: "Get edge function details and code",
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

			data, err := client.Get("/platform/projects/" + cfg.ActiveProject + "/functions/" + args[0])
			if err != nil {
				return err
			}

			var fn map[string]interface{}
			if err := json.Unmarshal(data, &fn); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			PrintJSON(fn)
			return nil
		},
	}
}

func efDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete an edge function",
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

			_, err = client.Delete("/platform/projects/" + cfg.ActiveProject + "/functions/" + args[0])
			if err != nil {
				return err
			}

			PrintSuccess(fmt.Sprintf("Function %q deleted", args[0]))
			return nil
		},
	}
}

func efLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "View execution logs for an edge function",
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

			limit, _ := cmd.Flags().GetInt("limit")
			path := fmt.Sprintf("/platform/projects/%s/functions/%s/logs?limit=%d", cfg.ActiveProject, args[0], limit)

			data, err := client.Get(path)
			if err != nil {
				return err
			}

			var logs []struct {
				Status     int     `json:"status"`
				DurationMs int     `json:"duration_ms"`
				Error      *string `json:"error"`
				Method     string  `json:"request_method"`
				CreatedAt  string  `json:"created_at"`
			}
			if err := json.Unmarshal(data, &logs); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				PrintJSON(logs)
				return nil
			}

			headers := []string{"Method", "Status", "Duration", "Error", "Time"}
			var rows [][]string
			for _, l := range logs {
				errStr := ""
				if l.Error != nil {
					errStr = *l.Error
					if len(errStr) > 50 {
						errStr = errStr[:50] + "..."
					}
				}
				rows = append(rows, []string{
					l.Method,
					fmt.Sprintf("%d", l.Status),
					fmt.Sprintf("%dms", l.DurationMs),
					errStr,
					l.CreatedAt,
				})
			}
			PrintTable(headers, rows)
			return nil
		},
	}
	cmd.Flags().IntP("limit", "n", 20, "Number of log entries to show")
	return cmd
}

func efInvokeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "invoke <name>",
		Short: "Invoke an edge function",
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

			dataStr, _ := cmd.Flags().GetString("data")

			var body interface{}
			if dataStr != "" {
				if err := json.Unmarshal([]byte(dataStr), &body); err != nil {
					return fmt.Errorf("invalid JSON data: %w", err)
				}
			}

			data, err := client.Post("/platform/projects/"+cfg.ActiveProject+"/functions/"+args[0]+"/test", body)
			if err != nil {
				return err
			}

			fmt.Println(string(data))
			return nil
		},
	}
	cmd.Flags().StringP("data", "d", "", "JSON request body")
	return cmd
}
