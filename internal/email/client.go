// Package email provides Scaleway TEM integration for transactional email.
package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// EmailClient sends transactional emails via the Scaleway TEM REST API.
type EmailClient struct {
	httpClient *http.Client
	authToken  string
	region     string
	projectID  string
	fromEmail  string
	fromName   string
}

// NewEmailClient creates a new TEM email client.
// If authToken is empty, emails will be logged instead of sent.
func NewEmailClient(authToken, region, projectID, fromEmail, fromName string) *EmailClient {
	if region == "" {
		region = "fr-par"
	}
	if fromName == "" {
		fromName = "Eurobase"
	}
	return &EmailClient{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		authToken:  authToken,
		region:     region,
		projectID:  projectID,
		fromEmail:  fromEmail,
		fromName:   fromName,
	}
}

// Configured returns true if the client has TEM credentials.
func (c *EmailClient) Configured() bool {
	return c.authToken != "" && c.fromEmail != ""
}

// temRequest is the JSON body for the Scaleway TEM send API.
type temRequest struct {
	From      temAddress   `json:"from"`
	To        []temAddress `json:"to"`
	Bcc       []temAddress `json:"bcc,omitempty"`
	Subject   string       `json:"subject"`
	HTML      string       `json:"html"`
	ProjectID string       `json:"project_id"`
}

type temAddress struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

// Send sends an email via Scaleway TEM. If unconfigured, it logs instead.
func (c *EmailClient) Send(ctx context.Context, to, subject, htmlBody string) error {
	if !c.Configured() {
		slog.Warn("email not configured, logging instead",
			"to", to,
			"subject", subject,
		)
		slog.Debug("email body (not sent)", "html", htmlBody)
		return nil
	}

	payload := temRequest{
		From:      temAddress{Email: c.fromEmail, Name: c.fromName},
		To:        []temAddress{{Email: to}},
		Subject:   subject,
		HTML:      htmlBody,
		ProjectID: c.projectID,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal email payload: %w", err)
	}

	url := fmt.Sprintf("https://api.scaleway.com/transactional-email/v1alpha1/regions/%s/emails", c.region)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Token", c.authToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		slog.Error("TEM API request failed", "error", err, "to", to, "subject", subject)
		return fmt.Errorf("TEM API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		slog.Error("TEM API error",
			"status", resp.StatusCode,
			"body", string(respBody),
			"to", to,
			"subject", subject,
		)
		return fmt.Errorf("TEM API returned %d: %s", resp.StatusCode, string(respBody))
	}

	slog.Info("email sent", "to", to, "subject", subject)
	return nil
}

// SendBulk sends one HTML email to every recipient via BCC so recipients
// don't see each other's addresses. The visible To is the sender itself
// (configured fromEmail), which is standard for announcements.
// Returns early with an error and does NOT send if the client is
// unconfigured, to avoid silently dropping an admin broadcast.
func (c *EmailClient) SendBulk(ctx context.Context, bccRecipients []string, subject, htmlBody string) error {
	if !c.Configured() {
		return fmt.Errorf("email client not configured (TEM credentials missing)")
	}
	if len(bccRecipients) == 0 {
		return fmt.Errorf("no recipients")
	}

	bcc := make([]temAddress, 0, len(bccRecipients))
	for _, r := range bccRecipients {
		bcc = append(bcc, temAddress{Email: r})
	}

	payload := temRequest{
		From:      temAddress{Email: c.fromEmail, Name: c.fromName},
		To:        []temAddress{{Email: c.fromEmail, Name: c.fromName}},
		Bcc:       bcc,
		Subject:   subject,
		HTML:      htmlBody,
		ProjectID: c.projectID,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal bulk email: %w", err)
	}

	url := fmt.Sprintf("https://api.scaleway.com/transactional-email/v1alpha1/regions/%s/emails", c.region)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Token", c.authToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("TEM bulk request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("TEM API returned %d: %s", resp.StatusCode, string(respBody))
	}

	slog.Info("bulk email sent", "recipients", len(bccRecipients), "subject", subject)
	return nil
}
