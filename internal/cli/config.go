package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Config stored at ~/.eurobase/config.json
type Config struct {
	Token         string `json:"token"`
	Email         string `json:"email"`
	ActiveProject string `json:"active_project"`
	ProjectSlug   string `json:"project_slug"`
	APIURL        string `json:"api_url"`
}

// DefaultAPIURL returns the default Eurobase API URL.
func DefaultAPIURL() string { return "https://api.eurobase.app" }

// ConfigPath returns the path to the config file (~/.eurobase/config.json).
func ConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".eurobase", "config.json")
}

// LoadConfig reads the config from disk. Returns a default config if the file
// does not exist.
func LoadConfig() (*Config, error) {
	cfg := &Config{APIURL: DefaultAPIURL()}
	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if cfg.APIURL == "" {
		cfg.APIURL = DefaultAPIURL()
	}
	return cfg, nil
}

// SaveConfig writes the config to disk, creating the directory if needed.
func SaveConfig(cfg *Config) error {
	dir := filepath.Dir(ConfigPath())
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	if err := os.WriteFile(ConfigPath(), data, 0600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	return nil
}

// APIClient makes authenticated HTTP requests to the platform API.
type APIClient struct {
	BaseURL    string
	Token      string
	httpClient *http.Client
}

// NewClientFromConfig loads the config and returns an authenticated API client.
func NewClientFromConfig() (*APIClient, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("not logged in — run `eurobase login` first")
	}
	baseURL := cfg.APIURL
	if baseURL == "" {
		baseURL = DefaultAPIURL()
	}
	return &APIClient{
		BaseURL: baseURL,
		Token:   cfg.Token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

func (c *APIClient) do(method, path string, body interface{}) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding request body: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	url := c.BaseURL + path
	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

// Get performs an authenticated GET request.
func (c *APIClient) Get(path string) ([]byte, error) {
	return c.do(http.MethodGet, path, nil)
}

// Post performs an authenticated POST request with a JSON body.
func (c *APIClient) Post(path string, body interface{}) ([]byte, error) {
	return c.do(http.MethodPost, path, body)
}

// Patch performs an authenticated PATCH request with a JSON body.
func (c *APIClient) Patch(path string, body interface{}) ([]byte, error) {
	return c.do(http.MethodPatch, path, body)
}

// Delete performs an authenticated DELETE request.
func (c *APIClient) Delete(path string) ([]byte, error) {
	return c.do(http.MethodDelete, path, nil)
}

// GetRaw performs an authenticated GET and returns the raw response (for
// streaming or file downloads). The caller must close the response body.
func (c *APIClient) GetRaw(path string) (*http.Response, error) {
	url := c.BaseURL + path
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}
	return resp, nil
}

// RequireProject returns an error if no active project is set in the config.
func RequireProject(cfg *Config) error {
	if cfg.ActiveProject == "" {
		return fmt.Errorf("no active project — run `eurobase switch <slug>` first")
	}
	return nil
}
