package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// KeysCmd returns the parent "keys" command.
func KeysCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keys",
		Short: "Manage API keys",
	}
	cmd.AddCommand(keysShowCmd())
	cmd.AddCommand(keysRegenerateCmd())
	return cmd
}

func keysShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show API keys",
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

			data, err := client.Get("/platform/projects/" + cfg.ActiveProject + "/api-keys")
			if err != nil {
				return err
			}

			var keys []struct {
				Type     string `json:"type"`
				Prefix   string `json:"prefix"`
				LastUsed string `json:"last_used"`
			}
			if err := json.Unmarshal(data, &keys); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				PrintJSON(keys)
				return nil
			}

			headers := []string{"Type", "Prefix", "Last Used"}
			var rows [][]string
			for _, k := range keys {
				lastUsed := k.LastUsed
				if lastUsed == "" {
					lastUsed = "never"
				}
				rows = append(rows, []string{k.Type, k.Prefix, lastUsed})
			}
			PrintTable(headers, rows)
			return nil
		},
	}
}

func keysRegenerateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "regenerate",
		Short: "Regenerate API keys",
		RunE: func(cmd *cobra.Command, args []string) error {
			confirm, _ := cmd.Flags().GetBool("confirm")
			if !confirm {
				PrintError("This will invalidate all existing API keys.")
				fmt.Println("  Re-run with --confirm to proceed.")
				return nil
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

			data, err := client.Post("/platform/projects/"+cfg.ActiveProject+"/api-keys/regenerate", nil)
			if err != nil {
				return err
			}

			var keys struct {
				AnonKey    string `json:"anon_key"`
				ServiceKey string `json:"service_key"`
			}
			if err := json.Unmarshal(data, &keys); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			PrintSuccess("API keys regenerated")
			fmt.Printf("\n  %sAnon key:%s    %s\n", colorBold, colorReset, keys.AnonKey)
			fmt.Printf("  %sService key:%s %s\n", colorBold, colorReset, keys.ServiceKey)
			PrintWarning("Save these keys — they will not be shown again.")
			return nil
		},
	}
	cmd.Flags().Bool("confirm", false, "Confirm key regeneration")
	return cmd
}
