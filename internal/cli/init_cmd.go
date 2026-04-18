package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// InitCmd returns the "init" command for project initialization.
func InitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize project files (.env, CLAUDE.md, .cursorrules)",
		Long:  "Fetches connection details for the active project and writes local config files.",
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

			// Fetch connection details
			data, err := client.Get("/platform/projects/" + cfg.ActiveProject + "/connect")
			if err != nil {
				return err
			}

			var conn struct {
				AnonKey     string `json:"anon_key"`
				ServiceKey  string `json:"service_key"`
				APIURL      string `json:"api_url"`
				ProjectID   string `json:"project_id"`
				ProjectSlug string `json:"project_slug"`
			}
			if err := json.Unmarshal(data, &conn); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			var created []string

			// Write .env — never includes DATABASE_URL. Tenants access data
			// through the SDK/HTTP API, which is scoped by their API key.
			envContent := fmt.Sprintf(`# Eurobase project: %s
EUROBASE_URL=%s
EUROBASE_ANON_KEY=%s
EUROBASE_SERVICE_KEY=%s
EUROBASE_PROJECT_ID=%s
`, conn.ProjectSlug, conn.APIURL, conn.AnonKey, conn.ServiceKey, conn.ProjectID)

			if err := os.WriteFile(".env", []byte(envContent), 0600); err != nil {
				PrintError("Failed to write .env: " + err.Error())
			} else {
				created = append(created, ".env")
			}

			// Write CLAUDE.md
			claudeContent := fmt.Sprintf(`# Eurobase Project: %s

## API
- Base URL: %s
- Project ID: %s

## Database
- Access data through the SDK or REST API using EUROBASE_ANON_KEY / EUROBASE_SERVICE_KEY
- No raw Postgres connection string is exposed — the platform enforces tenant isolation via RLS on the gateway
- System tables (users, refresh_tokens, storage_objects, email_tokens, vault_secrets) are managed by the platform

## Auth
- Custom auth built in Go (email/password, magic links, OAuth)
- Anon key for public client access
- Service key for server-side access only — never expose in client code

## Sovereignty
- All infrastructure runs in EU (France) on Scaleway
- No US cloud services permitted (AWS, GCP, Azure, Cloudflare, Stripe, Vercel)
`, conn.ProjectSlug, conn.APIURL, conn.ProjectID)

			if err := os.WriteFile("CLAUDE.md", []byte(claudeContent), 0644); err != nil {
				PrintError("Failed to write CLAUDE.md: " + err.Error())
			} else {
				created = append(created, "CLAUDE.md")
			}

			// Write .cursorrules
			cursorContent := fmt.Sprintf(`# Eurobase Project: %s
# API URL: %s
# Project ID: %s

- Use EUROBASE_URL and EUROBASE_ANON_KEY from .env for client-side API calls
- Use EUROBASE_SERVICE_KEY for server-side only
- All data access goes through the Eurobase REST API or direct PostgreSQL
- RLS is enforced — always set tenant context
- EU-sovereign: no US cloud services
`, conn.ProjectSlug, conn.APIURL, conn.ProjectID)

			if err := os.WriteFile(".cursorrules", []byte(cursorContent), 0644); err != nil {
				PrintError("Failed to write .cursorrules: " + err.Error())
			} else {
				created = append(created, ".cursorrules")
			}

			PrintSuccess(fmt.Sprintf("Initialized project %s", conn.ProjectSlug))
			for _, f := range created {
				fmt.Printf("  %s%s%s %s\n", colorGreen, "created", colorReset, f)
			}
			PrintWarning("Add .env to .gitignore — it contains secrets.")
			return nil
		},
	}
}
