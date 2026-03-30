package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// CronCmd returns the parent "cron" command.
func CronCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cron",
		Short: "Manage cron jobs",
	}
	cmd.AddCommand(cronListCmd())
	cmd.AddCommand(cronLogsCmd())
	return cmd
}

func cronListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List cron jobs",
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

			data, err := client.Get("/platform/projects/" + cfg.ActiveProject + "/cron")
			if err != nil {
				return err
			}

			var jobs []struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				Schedule string `json:"schedule"`
				Type     string `json:"type"`
				Enabled  bool   `json:"enabled"`
				LastRun  string `json:"last_run"`
				Runs     int    `json:"total_runs"`
			}
			if err := json.Unmarshal(data, &jobs); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				PrintJSON(jobs)
				return nil
			}

			headers := []string{"ID", "Name", "Schedule", "Type", "Enabled", "Last Run", "Runs"}
			var rows [][]string
			for _, j := range jobs {
				enabled := colorRed + "no" + colorReset
				if j.Enabled {
					enabled = colorGreen + "yes" + colorReset
				}
				lastRun := j.LastRun
				if lastRun == "" {
					lastRun = "never"
				}
				rows = append(rows, []string{
					j.ID, j.Name, j.Schedule, j.Type, enabled, lastRun,
					fmt.Sprintf("%d", j.Runs),
				})
			}
			PrintTable(headers, rows)
			return nil
		},
	}
}

func cronLogsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs <job-id>",
		Short: "Show run history for a cron job",
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

			data, err := client.Get("/platform/projects/" + cfg.ActiveProject + "/cron/" + args[0] + "/runs")
			if err != nil {
				return err
			}

			var runs []struct {
				Time     string `json:"timestamp"`
				Duration string `json:"duration"`
				Status   string `json:"status"`
				Result   string `json:"result"`
			}
			if err := json.Unmarshal(data, &runs); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				PrintJSON(runs)
				return nil
			}

			headers := []string{"Time", "Duration", "Status", "Result"}
			var rows [][]string
			for _, r := range runs {
				status := r.Status
				if status == "success" {
					status = colorGreen + status + colorReset
				} else if status == "error" || status == "failed" {
					status = colorRed + status + colorReset
				}
				rows = append(rows, []string{r.Time, r.Duration, status, r.Result})
			}
			PrintTable(headers, rows)
			return nil
		},
	}
}
