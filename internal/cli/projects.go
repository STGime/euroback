package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// ProjectsCmd returns the parent "projects" command.
func ProjectsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "projects",
		Aliases: []string{"project"},
		Short:   "Manage Eurobase projects",
	}
	cmd.AddCommand(projectsListCmd())
	cmd.AddCommand(projectsCreateCmd())
	cmd.AddCommand(projectsDeleteCmd())
	return cmd
}

func projectsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all projects",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := NewClientFromConfig()
			if err != nil {
				return err
			}

			data, err := client.Get("/v1/tenants")
			if err != nil {
				return err
			}

			var projects []struct {
				ID     string `json:"id"`
				Name   string `json:"name"`
				Slug   string `json:"slug"`
				Plan   string `json:"plan"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal(data, &projects); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				PrintJSON(projects)
				return nil
			}

			cfg, _ := LoadConfig()
			headers := []string{"ID", "Name", "Slug", "Plan", "Status"}
			var rows [][]string
			for _, p := range projects {
				marker := ""
				if cfg != nil && p.ID == cfg.ActiveProject {
					marker = " *"
				}
				rows = append(rows, []string{p.ID, p.Name + marker, p.Slug, p.Plan, p.Status})
			}
			PrintTable(headers, rows)
			return nil
		},
	}
}

func projectsCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := NewClientFromConfig()
			if err != nil {
				return err
			}

			name := args[0]
			slug := strings.ToLower(strings.ReplaceAll(name, " ", "-"))

			body := map[string]string{
				"name": name,
				"slug": slug,
			}
			data, err := client.Post("/v1/tenants", body)
			if err != nil {
				return err
			}

			var project struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Slug string `json:"slug"`
			}
			if err := json.Unmarshal(data, &project); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			PrintSuccess(fmt.Sprintf("Created project: %s (%s)", project.Name, project.Slug))
			fmt.Printf("  ID: %s\n", project.ID)
			return nil
		},
	}
}

func projectsDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			confirm, _ := cmd.Flags().GetBool("confirm")
			if !confirm {
				PrintError("This will permanently delete the project and all its data.")
				fmt.Println("  Re-run with --confirm to proceed.")
				return nil
			}

			client, err := NewClientFromConfig()
			if err != nil {
				return err
			}

			_, err = client.Delete("/v1/tenants/" + args[0])
			if err != nil {
				return err
			}

			// Clear active project if it was deleted
			cfg, _ := LoadConfig()
			if cfg != nil && cfg.ActiveProject == args[0] {
				cfg.ActiveProject = ""
				cfg.ProjectSlug = ""
				_ = SaveConfig(cfg)
			}

			PrintSuccess("Project deleted")
			return nil
		},
	}
	cmd.Flags().Bool("confirm", false, "Confirm deletion")
	return cmd
}

// SwitchCmd returns the "switch" command for changing the active project.
func SwitchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "switch <slug-or-id>",
		Short: "Switch active project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := NewClientFromConfig()
			if err != nil {
				return err
			}

			target := args[0]

			data, err := client.Get("/v1/tenants")
			if err != nil {
				return err
			}

			var projects []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Slug string `json:"slug"`
			}
			if err := json.Unmarshal(data, &projects); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			for _, p := range projects {
				if p.ID == target || p.Slug == target {
					cfg, err := LoadConfig()
					if err != nil {
						return err
					}
					cfg.ActiveProject = p.ID
					cfg.ProjectSlug = p.Slug
					if err := SaveConfig(cfg); err != nil {
						return err
					}
					PrintSuccess(fmt.Sprintf("Switched to project: %s (%s)", p.Name, p.Slug))
					return nil
				}
			}

			return fmt.Errorf("project %q not found", target)
		},
	}
}
