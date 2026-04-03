package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// ComplianceCmd returns the parent "compliance" command.
func ComplianceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compliance",
		Short: "GDPR compliance tools",
	}
	cmd.AddCommand(complianceReportCmd())
	cmd.AddCommand(complianceSubProcessorsCmd())
	return cmd
}

func complianceReportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "report",
		Short: "Generate a DPA compliance report for the active project",
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

			data, err := client.Get("/platform/projects/" + cfg.ActiveProject + "/compliance/dpa-report")
			if err != nil {
				return err
			}

			// Always output as formatted JSON — the report is the value.
			var report interface{}
			if err := json.Unmarshal(data, &report); err != nil {
				return fmt.Errorf("parsing report: %w", err)
			}
			PrintJSON(report)
			return nil
		},
	}
}

func complianceSubProcessorsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sub-processors",
		Short: "List active sub-processors for the active project",
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

			data, err := client.Get("/platform/projects/" + cfg.ActiveProject + "/compliance/sub-processors")
			if err != nil {
				return err
			}

			var processors []struct {
				Name           string   `json:"name"`
				Country        string   `json:"country"`
				CountryCode    string   `json:"country_code"`
				Service        string   `json:"service"`
				Purpose        string   `json:"purpose"`
				SecurityCerts  []string `json:"security_certs"`
				CloudActRisk   bool     `json:"cloud_act_risk"`
			}
			if err := json.Unmarshal(data, &processors); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				PrintJSON(processors)
				return nil
			}

			headers := []string{"Name", "Country", "Service", "Purpose", "Certs", "CLOUD Act"}
			var rows [][]string
			for _, p := range processors {
				risk := colorGreen + "No" + colorReset
				if p.CloudActRisk {
					risk = colorRed + "YES" + colorReset
				}
				rows = append(rows, []string{
					p.Name,
					p.Country + " (" + p.CountryCode + ")",
					p.Service,
					truncate(p.Purpose, 40),
					strings.Join(p.SecurityCerts, ", "),
					risk,
				})
			}
			PrintTable(headers, rows)
			return nil
		},
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
