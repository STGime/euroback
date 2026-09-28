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
				Type      string `json:"type"`
				Prefix    string `json:"key_prefix"`
				LastUsed  string `json:"last_used_at"`
				PublicKey string `json:"public_key,omitempty"`
			}
			if err := json.Unmarshal(data, &keys); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				PrintJSON(keys)
				return nil
			}

			headers := []string{"Type", "Key", "Last Used"}
			var rows [][]string
			for _, k := range keys {
				lastUsed := k.LastUsed
				if lastUsed == "" {
					lastUsed = "never"
				}
				// The public key is shown in full (it's public); the secret
				// key only by prefix — it can't be retrieved again.
				key := k.PublicKey
				if key == "" {
					key = k.Prefix + "…"
				}
				rows = append(rows, []string{k.Type, key, lastUsed})
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
				PublicKey string `json:"public_key"`
				SecretKey string `json:"secret_key"`
			}
			if err := json.Unmarshal(data, &keys); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}
			if keys.PublicKey == "" || keys.SecretKey == "" {
				// The old keys are already gone; say so rather than print blanks.
				return fmt.Errorf("keys were regenerated but the response had no keys — regenerate again from the console")
			}

			PrintSuccess("API keys regenerated")
			fmt.Printf("\n  %sPublic key:%s %s\n", colorBold, colorReset, keys.PublicKey)
			fmt.Printf("  %sSecret key:%s %s\n", colorBold, colorReset, keys.SecretKey)
			PrintWarning("Save the secret key now — it will not be shown again (the public key stays visible via `eurobase keys show`).")
			return nil
		},
	}
	cmd.Flags().Bool("confirm", false, "Confirm key regeneration")
	return cmd
}
