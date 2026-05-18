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
	"os"
	"strconv"
	"time"
)

// defaultMaxBCC is the upper bound on recipients per TEM POST when we
// don't have an explicit override. Closes #35. Scaleway TEM caps
// recipients per message at TemEmailsMaxRecipients (10 on a default
// domain) — and that quota counts the visible To slot too, so we send
// 1 To + at most defaultMaxBCC BCCs per chunk. Operators with a raised
// Scaleway quota set TEM_MAX_RECIPIENTS_PER_MESSAGE to the new limit
// without a deploy.
const defaultMaxBCC = 9

// BulkChunkError records a single per-chunk failure during a bulk send.
// Recipients is the BCC list that didn't go out; Err is the wrapped
// underlying error (typically a TEM API non-2xx response).
type BulkChunkError struct {
	Recipients []string `json:"recipients"`
	Error      string   `json:"error"`
}

// BulkResult is the per-call summary of a chunked bulk send. Sent /
// Failed are recipient counts (not chunk counts) so the UI can surface
// "12 / 15 delivered" directly. Errors lists the chunks that didn't go
// out so an operator can retry just those.
type BulkResult struct {
	Sent   int              `json:"sent"`
	Failed int              `json:"failed"`
	Errors []BulkChunkError `json:"errors,omitempty"`
}

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

// SendBulk sends one HTML email to every recipient via BCC so
// recipients don't see each other's addresses. The visible To is the
// sender itself (configured fromEmail), which is standard for
// announcements.
//
// Closes #35. The recipient list is split into chunks of at most
// maxBCCPerMessage(), one TEM POST per chunk. A chunk that fails
// (network error, TEM 403/429/5xx) is recorded in BulkResult.Errors
// and the loop continues; this matters because the original behaviour
// — a single POST with N recipients — returned 403 from TEM the
// moment N exceeded the per-message quota, dropping the whole send
// even though up to 10 of the addresses would have gone out fine. The
// caller decides how to surface partial success; the wrapping error
// is non-nil only when every chunk failed.
//
// Recipients are sent in the order received; ordering matters for
// audit-trail reproducibility (the first 9 of an alphabetical list
// will land in chunk 1, etc.).
func (c *EmailClient) SendBulk(ctx context.Context, bccRecipients []string, subject, htmlBody string) (BulkResult, error) {
	res := BulkResult{}
	if !c.Configured() {
		return res, fmt.Errorf("email client not configured (TEM credentials missing)")
	}
	if len(bccRecipients) == 0 {
		return res, fmt.Errorf("no recipients")
	}

	chunkSize := maxBCCPerMessage()
	chunks := chunkRecipients(bccRecipients, chunkSize)

	for i, chunk := range chunks {
		if err := c.sendBulkChunk(ctx, chunk, subject, htmlBody); err != nil {
			res.Failed += len(chunk)
			res.Errors = append(res.Errors, BulkChunkError{
				Recipients: chunk,
				Error:      err.Error(),
			})
			slog.Error("bulk email chunk failed",
				"chunk_index", i,
				"chunk_size", len(chunk),
				"total_chunks", len(chunks),
				"error", err,
				"subject", subject,
			)
			continue
		}
		res.Sent += len(chunk)
	}

	slog.Info("bulk email completed",
		"sent", res.Sent,
		"failed", res.Failed,
		"chunks", len(chunks),
		"subject", subject,
	)

	if res.Sent == 0 {
		return res, fmt.Errorf("every chunk failed: %d recipients, %d errors", len(bccRecipients), len(res.Errors))
	}
	return res, nil
}

// sendBulkChunk issues one TEM POST for a single chunk of recipients.
// The chunk size is bounded by the caller (SendBulk) to fit the
// TemEmailsMaxRecipients quota; this function only does the HTTP.
func (c *EmailClient) sendBulkChunk(ctx context.Context, bccRecipients []string, subject, htmlBody string) error {
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

	return nil
}

// chunkRecipients splits the recipient list into contiguous chunks of
// at most size. Stable order: chunk[0] is recipients[0:size], chunk[1]
// is recipients[size:2*size], etc. Empty input returns an empty
// slice; size<=0 falls back to defaultMaxBCC so a misconfigured env
// override can't disable batching entirely.
func chunkRecipients(recipients []string, size int) [][]string {
	if size <= 0 {
		size = defaultMaxBCC
	}
	if len(recipients) == 0 {
		return nil
	}
	chunks := make([][]string, 0, (len(recipients)+size-1)/size)
	for i := 0; i < len(recipients); i += size {
		end := i + size
		if end > len(recipients) {
			end = len(recipients)
		}
		chunks = append(chunks, recipients[i:end])
	}
	return chunks
}

// maxBCCPerMessage returns the per-TEM-POST BCC cap. Reads the
// TEM_MAX_RECIPIENTS_PER_MESSAGE env var and falls back to
// defaultMaxBCC (9) on missing/invalid values. Operators with a
// raised Scaleway quota flip this env var; no deploy needed.
func maxBCCPerMessage() int {
	if raw := os.Getenv("TEM_MAX_RECIPIENTS_PER_MESSAGE"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
		slog.Warn("ignoring invalid TEM_MAX_RECIPIENTS_PER_MESSAGE", "value", raw)
	}
	return defaultMaxBCC
}
