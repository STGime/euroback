package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// StatusCmd returns the "status" command showing project usage.
func StatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show active project status and usage",
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

			data, err := client.Get("/platform/projects/" + cfg.ActiveProject + "/usage")
			if err != nil {
				return err
			}

			// The endpoint (internal/plans/handler.go HandleGetUsage)
			// returns {usage: {...}, limits: {...}} — the CLI previously
			// expected a flat shape that matched nothing, so status
			// printed `Project:  ()` with zeroed bars (issue #192).
			var resp struct {
				Usage struct {
					DatabaseSizeMB float64 `json:"database_size_mb"`
					StorageSizeMB  float64 `json:"storage_size_mb"`
					MAUCount       int     `json:"mau_count"`
				} `json:"usage"`
				Limits struct {
					Plan      string `json:"plan"`
					DBSizeMB  int    `json:"db_size_mb"`
					StorageMB int    `json:"storage_mb"`
					MAULimit  int    `json:"mau_limit"`
				} `json:"limits"`
			}
			if err := json.Unmarshal(data, &resp); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				PrintJSON(resp)
				return nil
			}

			fmt.Printf("%sProject:%s %s\n", colorBold, colorReset, ProjectLabel(cfg))
			fmt.Printf("%sPlan:%s    %s\n", colorBold, colorReset, resp.Limits.Plan)
			fmt.Println()

			printBar("Database", resp.Usage.DatabaseSizeMB, float64(resp.Limits.DBSizeMB), "MB")
			printBar("Storage", resp.Usage.StorageSizeMB, float64(resp.Limits.StorageMB), "MB")
			printBarInt("MAU", resp.Usage.MAUCount, resp.Limits.MAULimit)

			return nil
		},
	}
}

func printBar(label string, used, limit float64, unit string) {
	pct := 0.0
	if limit > 0 {
		pct = used / limit * 100
	}
	barLen := 30
	filled := int(pct / 100 * float64(barLen))
	if filled > barLen {
		filled = barLen
	}
	color := colorGreen
	if pct > 80 {
		color = colorYellow
	}
	if pct > 95 {
		color = colorRed
	}
	bar := color + repeatChar('█', filled) + colorReset + repeatChar('░', barLen-filled)
	fmt.Printf("  %-10s [%s] %.1f / %.1f %s (%.0f%%)\n", label, bar, used, limit, unit, pct)
}

func printBarInt(label string, used, limit int) {
	pct := 0.0
	if limit > 0 {
		pct = float64(used) / float64(limit) * 100
	}
	barLen := 30
	filled := int(pct / 100 * float64(barLen))
	if filled > barLen {
		filled = barLen
	}
	color := colorGreen
	if pct > 80 {
		color = colorYellow
	}
	if pct > 95 {
		color = colorRed
	}
	bar := color + repeatChar('█', filled) + colorReset + repeatChar('░', barLen-filled)
	fmt.Printf("  %-10s [%s] %d / %d (%.0f%%)\n", label, bar, used, limit, pct)
}

func repeatChar(ch rune, count int) string {
	if count <= 0 {
		return ""
	}
	s := make([]rune, count)
	for i := range s {
		s[i] = ch
	}
	return string(s)
}
