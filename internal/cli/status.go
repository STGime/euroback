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

			var usage struct {
				ProjectName string  `json:"project_name"`
				ProjectSlug string  `json:"project_slug"`
				Plan        string  `json:"plan"`
				DbUsageMB   float64 `json:"db_usage_mb"`
				DbLimitMB   float64 `json:"db_limit_mb"`
				StorageMB   float64 `json:"storage_usage_mb"`
				StorageMax  float64 `json:"storage_limit_mb"`
				MAU         int     `json:"mau"`
				MAULimit    int     `json:"mau_limit"`
			}
			if err := json.Unmarshal(data, &usage); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				PrintJSON(usage)
				return nil
			}

			fmt.Printf("%sProject:%s %s (%s)\n", colorBold, colorReset, usage.ProjectName, usage.ProjectSlug)
			fmt.Printf("%sPlan:%s    %s\n", colorBold, colorReset, usage.Plan)
			fmt.Println()

			printBar("Database", usage.DbUsageMB, usage.DbLimitMB, "MB")
			printBar("Storage", usage.StorageMB, usage.StorageMax, "MB")
			printBarInt("MAU", usage.MAU, usage.MAULimit)

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
