package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// LogsCmd returns the "logs" command for viewing request logs.
func LogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "View project request logs",
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

			tail, _ := cmd.Flags().GetBool("tail")
			method, _ := cmd.Flags().GetString("method")
			status, _ := cmd.Flags().GetInt("status")
			limit, _ := cmd.Flags().GetInt("limit")

			query := fmt.Sprintf("?limit=%d", limit)
			if method != "" {
				query += "&method=" + method
			}
			if status > 0 {
				query += fmt.Sprintf("&status_min=%d", status)
			}

			path := "/platform/projects/" + cfg.ActiveProject + "/logs" + query

			if !tail {
				return fetchAndPrintLogs(client, path, cmd)
			}

			// Tail mode: poll every 2 seconds
			var lastSeen string
			for {
				p := path
				if lastSeen != "" {
					p += "&after=" + lastSeen
				}
				data, err := client.Get(p)
				if err != nil {
					PrintError("Error fetching logs: " + err.Error())
					time.Sleep(2 * time.Second)
					continue
				}

				var entries []logEntry
				if err := json.Unmarshal(data, &entries); err != nil {
					PrintError("Error parsing logs: " + err.Error())
					time.Sleep(2 * time.Second)
					continue
				}

				for _, e := range entries {
					printLogEntry(e)
					lastSeen = e.Timestamp
				}

				time.Sleep(2 * time.Second)
			}
		},
	}
	cmd.Flags().BoolP("tail", "t", false, "Stream logs in real time")
	cmd.Flags().StringP("method", "m", "", "Filter by HTTP method")
	cmd.Flags().IntP("status", "s", 0, "Minimum status code filter")
	cmd.Flags().IntP("limit", "l", 50, "Number of log entries")
	return cmd
}

type logEntry struct {
	Timestamp string `json:"timestamp"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Status    int    `json:"status"`
	Latency   string `json:"latency"`
}

func fetchAndPrintLogs(client *APIClient, path string, cmd *cobra.Command) error {
	data, err := client.Get(path)
	if err != nil {
		return err
	}

	var entries []logEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("parsing response: %w", err)
	}

	jsonOut, _ := cmd.Flags().GetBool("json")
	if jsonOut {
		PrintJSON(entries)
		return nil
	}

	headers := []string{"Time", "Method", "Path", "Status", "Latency"}
	var rows [][]string
	for _, e := range entries {
		statusStr := fmt.Sprintf("%d", e.Status)
		if e.Status >= 500 {
			statusStr = colorRed + statusStr + colorReset
		} else if e.Status >= 400 {
			statusStr = colorYellow + statusStr + colorReset
		}
		rows = append(rows, []string{e.Timestamp, e.Method, e.Path, statusStr, e.Latency})
	}
	PrintTable(headers, rows)
	return nil
}

func printLogEntry(e logEntry) {
	statusStr := fmt.Sprintf("%d", e.Status)
	if e.Status >= 500 {
		statusStr = colorRed + statusStr + colorReset
	} else if e.Status >= 400 {
		statusStr = colorYellow + statusStr + colorReset
	}
	fmt.Printf("%s  %-6s %-40s %s  %s\n", e.Timestamp, e.Method, e.Path, statusStr, e.Latency)
}
