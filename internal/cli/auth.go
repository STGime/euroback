package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// LoginCmd returns the login command.
func LoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Log in to Eurobase",
		Long:  "Authenticate with your Eurobase account using email and password.",
		RunE: func(cmd *cobra.Command, args []string) error {
			reader := bufio.NewReader(os.Stdin)

			fmt.Print("Email: ")
			email, _ := reader.ReadString('\n')
			email = strings.TrimSpace(email)

			fmt.Print("Password (input visible): ")
			password, _ := reader.ReadString('\n')
			password = strings.TrimSpace(password)

			if email == "" || password == "" {
				return fmt.Errorf("email and password are required")
			}

			cfg, err := LoadConfig()
			if err != nil {
				return err
			}

			// Check for API URL override
			apiURL, _ := cmd.Flags().GetString("api-url")
			if apiURL != "" {
				cfg.APIURL = apiURL
			}
			if cfg.APIURL == "" {
				cfg.APIURL = DefaultAPIURL()
			}

			// Sign in using a temporary unauthenticated client
			client := &APIClient{
				BaseURL: cfg.APIURL,
				Token:   "",
				httpClient: &http.Client{
					Timeout: 30 * time.Second,
				},
			}

			body := map[string]string{
				"email":    email,
				"password": password,
			}
			respData, err := client.Post("/platform/auth/signin", body)
			if err != nil {
				PrintError("Login failed: " + err.Error())
				return nil
			}

			var result struct {
				AccessToken string `json:"access_token"`
				User        struct {
					ID    string `json:"id"`
					Email string `json:"email"`
				} `json:"user"`
			}
			if err := json.Unmarshal(respData, &result); err != nil {
				return fmt.Errorf("parsing login response: %w", err)
			}
			if result.AccessToken == "" {
				return fmt.Errorf("no token in login response")
			}

			cfg.Token = result.AccessToken
			cfg.Email = email

			// Try to fetch projects and auto-select if only one
			client.Token = result.AccessToken
			projectsData, err := client.Get("/v1/tenants")
			if err == nil {
				var projects []struct {
					ID   string `json:"id"`
					Slug string `json:"slug"`
				}
				if json.Unmarshal(projectsData, &projects) == nil && len(projects) == 1 {
					cfg.ActiveProject = projects[0].ID
					cfg.ProjectSlug = projects[0].Slug
				}
			}

			if err := SaveConfig(cfg); err != nil {
				return err
			}

			PrintSuccess(fmt.Sprintf("Logged in as %s", email))
			if cfg.ActiveProject != "" {
				PrintSuccess(fmt.Sprintf("Active project: %s", cfg.ProjectSlug))
			}
			return nil
		},
	}
}

// LogoutCmd returns the logout command.
func LogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Log out of Eurobase",
		Long:  "Clear all stored credentials and active project.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := &Config{APIURL: DefaultAPIURL()}
			if err := SaveConfig(cfg); err != nil {
				return err
			}
			PrintSuccess("Logged out")
			return nil
		},
	}
}
