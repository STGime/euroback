package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// VaultCmd returns the parent "vault" command.
func VaultCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vault",
		Short: "Manage vault secrets",
	}
	cmd.AddCommand(vaultListCmd())
	cmd.AddCommand(vaultGetCmd())
	cmd.AddCommand(vaultSetCmd())
	cmd.AddCommand(vaultDeleteCmd())
	return cmd
}

func vaultListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List vault secrets",
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

			data, err := client.Get("/platform/projects/" + cfg.ActiveProject + "/vault")
			if err != nil {
				return err
			}

			var secrets []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				CreatedAt   string `json:"created_at"`
			}
			if err := json.Unmarshal(data, &secrets); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				PrintJSON(secrets)
				return nil
			}

			headers := []string{"Name", "Description", "Created"}
			var rows [][]string
			for _, s := range secrets {
				rows = append(rows, []string{s.Name, s.Description, s.CreatedAt})
			}
			PrintTable(headers, rows)
			return nil
		},
	}
}

func vaultGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <name>",
		Short: "Get a secret value",
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

			data, err := client.Get("/platform/projects/" + cfg.ActiveProject + "/vault/" + args[0])
			if err != nil {
				return err
			}

			var secret struct {
				Value string `json:"value"`
			}
			if err := json.Unmarshal(data, &secret); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			fmt.Print(secret.Value)
			return nil
		},
	}
}

func vaultSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <name> <value>",
		Short: "Set a secret",
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

			description, _ := cmd.Flags().GetString("description")

			body := map[string]string{
				"name":        args[0],
				"value":       args[1],
				"description": description,
			}

			_, err = client.Post("/platform/projects/"+cfg.ActiveProject+"/vault", body)
			if err != nil {
				return err
			}

			PrintSuccess(fmt.Sprintf("Secret %q saved", args[0]))
			return nil
		},
	}
	cmd.Flags().StringP("description", "d", "", "Secret description")
	return cmd
}

func vaultDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a secret",
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

			_, err = client.Delete("/platform/projects/" + cfg.ActiveProject + "/vault/" + args[0])
			if err != nil {
				return err
			}

			PrintSuccess(fmt.Sprintf("Secret %q deleted", args[0]))
			return nil
		},
	}
}
