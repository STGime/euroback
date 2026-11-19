package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

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
				Name             string `json:"name"`
				Status           string `json:"status"`
				Version          int    `json:"version"`
				VerifyJWT        bool   `json:"verify_jwt"`
				AllowServiceRole bool   `json:"allow_service_role"`
				CreatedAt        string `json:"created_at"`
				UpdatedAt        string `json:"updated_at"`
			}
			if err := json.Unmarshal(data, &fns); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				PrintJSON(fns)
				return nil
			}

			headers := []string{"Name", "Status", "Version", "JWT Required", "asService", "Updated"}
			var rows [][]string
			for _, f := range fns {
				jwt := "no"
				if f.VerifyJWT {
					jwt = "yes"
				}
				asService := "no"
				if f.AllowServiceRole {
					asService = "yes"
				}
				rows = append(rows, []string{
					f.Name,
					f.Status,
					fmt.Sprintf("v%d", f.Version),
					jwt,
					asService,
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

			if file == "" {
				// The usual places, single-file and folder layouts.
				candidates := []string{
					"functions/" + args[0] + ".ts",
					"functions/" + args[0] + ".js",
					"functions/" + args[0] + "/index.ts",
					"functions/" + args[0] + "/index.js",
					"eurobase/functions/" + args[0] + "/index.ts",
				}
				for _, c := range candidates {
					if _, err := os.Stat(c); err == nil {
						file = c
						break
					}
				}
				if file == "" {
					return fmt.Errorf("no file found — provide --file or place code at functions/%s.ts (or functions/%s/index.ts)", args[0], args[0])
				}
				fmt.Printf("Deploying from %s\n", file)
			}

			// A function that imports other modules is bundled into one
			// file here: the runner loads a single script.
			var code string
			if noBundle, _ := cmd.Flags().GetBool("no-bundle"); noBundle {
				content, err := os.ReadFile(file)
				if err != nil {
					return fmt.Errorf("reading file: %w", err)
				}
				code = string(content)
			} else {
				compat, _ := cmd.Flags().GetString("compat")
				switch compat {
				case "auto":
					compat = ""
					if src, err := os.ReadFile(file); err == nil && looksLikeSupabaseFunction(string(src)) {
						compat = compatSupabase
					}
				case compatNone:
					compat = ""
				case compatSupabase:
				default:
					return fmt.Errorf("--compat takes auto, supabase or none")
				}
				res, err := bundleFunction(file, bundleOptions{Compat: compat})
				if err != nil {
					return err
				}
				code = res.Code
				if res.Bundled {
					note := ""
					if res.Minified {
						note = ", minified to fit"
					}
					fmt.Printf("Bundled %d local and %d remote module(s) into one file (%d KB%s)\n", len(res.Local), len(res.Remote), len(code)/1024, note)
				}
				if res.Compat == compatSupabase {
					fmt.Println("Supabase compatibility is on: Deno.serve, Deno.env.get and createClient() work as written (--compat none switches it off)")
					if len(res.EnvNames) > 0 {
						fmt.Printf("  Reads these settings — set each as a function env var or in the Vault (eurobase vault set NAME VALUE): %s\n", strings.Join(res.EnvNames, ", "))
					}
					if res.UsesServiceRole && !cmd.Flags().Changed("allow-service-role") {
						fmt.Println("  Uses the service role key: its service-role queries need --allow-service-role when a signed-in user calls the function")
					}
				}
			}

			var allowService *bool
			if cmd.Flags().Changed("allow-service-role") {
				v, _ := cmd.Flags().GetBool("allow-service-role")
				allowService = &v
			}
			created, err := uploadFunction(client, cfg.ActiveProject, args[0], code, !noJWT, allowService)
			if err != nil {
				return err
			}
			if created {
				PrintSuccess(fmt.Sprintf("Function %q created and deployed to %s", args[0], ProjectLabel(cfg)))
				return nil
			}
			PrintSuccess(fmt.Sprintf("Function %q deployed to %s", args[0], ProjectLabel(cfg)))
			return nil
		},
	}
	cmd.Flags().StringP("file", "f", "", "Path to the function file")
	cmd.Flags().Bool("no-verify-jwt", false, "Allow unauthenticated invocations")
	cmd.Flags().Bool("no-bundle", false, "Deploy the file as it is, without bundling its imports")
	cmd.Flags().String("compat", "auto", "Supabase compatibility: auto (on for a function that calls Deno.serve or imports @supabase/supabase-js), supabase, or none")
	cmd.Flags().Bool("allow-service-role", false, "Let the function call ctx.db.asService(): SQL with the service identity while a signed-in user is calling (--allow-service-role=false switches it off; leave the flag out to keep the current setting)")
	return cmd
}

// uploadFunction updates the function, or creates it when it doesn't
// exist yet. allowService is only sent when set, so a plain redeploy
// keeps the function's current setting.
func uploadFunction(client *APIClient, projectID, name, code string, verifyJWT bool, allowService *bool) (created bool, err error) {
	payload := map[string]interface{}{
		"code":       code,
		"verify_jwt": verifyJWT,
	}
	if allowService != nil {
		payload["allow_service_role"] = *allowService
	}
	if _, err := client.Put("/platform/projects/"+projectID+"/functions/"+name, payload); err == nil {
		return false, nil
	}
	// The function doesn't exist yet: create it.
	payload["name"] = name
	if _, err := client.Post("/platform/projects/"+projectID+"/functions", payload); err != nil {
		return false, err
	}
	return true, nil
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

			PrintSuccess(fmt.Sprintf("Function %q deleted from %s", args[0], ProjectLabel(cfg)))
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
