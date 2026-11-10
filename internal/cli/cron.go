package cli

import (
	"encoding/json"
	"fmt"
	"time"

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

			var jobs []cliCronJob
			if err := json.Unmarshal(data, &jobs); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				PrintJSON(json.RawMessage(data)) // the full API response
				return nil
			}

			headers := []string{"ID", "Name", "Schedule", "Type", "Enabled", "Last Run", "Runs"}
			var rows [][]string
			for _, j := range jobs {
				enabled := colorRed + "no" + colorReset
				if j.Enabled {
					enabled = colorGreen + "yes" + colorReset
				}
				lastRun := "never"
				if j.LastRunAt != nil {
					lastRun = j.LastRunAt.Local().Format("2006-01-02 15:04:05")
				}
				rows = append(rows, []string{
					j.ID, j.Name, j.Schedule, j.ActionType, enabled, lastRun,
					fmt.Sprintf("%d", j.RunCount),
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

			var runs []cliCronRun
			if err := json.Unmarshal(data, &runs); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				PrintJSON(json.RawMessage(data)) // the full API response
				return nil
			}

			headers := []string{"Started", "Duration", "Status", "Result"}
			var rows [][]string
			for _, r := range runs {
				status := r.Status
				if status == "success" {
					status = colorGreen + status + colorReset
				} else if status == "error" || status == "failed" {
					status = colorRed + status + colorReset
				}
				duration := "-"
				if r.DurationMs != nil {
					duration = fmt.Sprintf("%d ms", *r.DurationMs)
				}
				result := ""
				if r.Error != nil && *r.Error != "" {
					result = *r.Error
				} else if r.Result != nil {
					result = *r.Result
				}
				rows = append(rows, []string{r.StartedAt.Local().Format("2006-01-02 15:04:05"), duration, status, result})
			}
			PrintTable(headers, rows)
			return nil
		},
	}
}

// cliCronJob is the part of the API's cron job (internal/cron.CronJob) the
// CLI shows. Field names must match the API's JSON.
type cliCronJob struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Schedule   string     `json:"schedule"`
	ActionType string     `json:"action_type"`
	Enabled    bool       `json:"enabled"`
	LastRunAt  *time.Time `json:"last_run_at"`
	RunCount   int        `json:"run_count"`
}

// cliCronRun is the part of the API's run record (internal/cron.CronJobRun)
// the CLI shows.
type cliCronRun struct {
	StartedAt  time.Time `json:"started_at"`
	DurationMs *int      `json:"duration_ms"`
	Status     string    `json:"status"`
	Result     *string   `json:"result"`
	Error      *string   `json:"error"`
}
