package email

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// FailedEmail is one email the provider gave up on (a hard bounce or a
// rejection): the recipient and why.
type FailedEmail struct {
	Recipient string
	Reason    string
	At        time.Time
}

const (
	failedEmailsPageSize = 100
	failedEmailsMaxPages = 50
)

// ListFailedEmails returns the emails of this client's project that the
// provider marked as failed (permanently undeliverable) since the given
// time. Read-only.
func (c *EmailClient) ListFailedEmails(ctx context.Context, since time.Time) ([]FailedEmail, error) {
	if !c.Configured() {
		return nil, nil
	}
	var out []FailedEmail
	for page := 1; page <= failedEmailsMaxPages; page++ {
		q := url.Values{}
		q.Set("statuses", "failed")
		q.Set("since", since.UTC().Format(time.RFC3339))
		q.Set("page", strconv.Itoa(page))
		q.Set("page_size", strconv.Itoa(failedEmailsPageSize))
		if c.projectID != "" {
			q.Set("project_id", c.projectID)
		}
		u := fmt.Sprintf("https://api.scaleway.com/transactional-email/v1alpha1/regions/%s/emails?%s", c.region, q.Encode())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("X-Auth-Token", c.authToken)
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list failed emails: %w", err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read failed emails: %w", err)
		}
		if resp.StatusCode >= 300 {
			return nil, &TEMError{Status: resp.StatusCode, Body: string(body)}
		}
		emails, err := parseFailedEmails(body)
		if err != nil {
			return nil, err
		}
		out = append(out, emails...)
		if len(emails) < failedEmailsPageSize {
			break
		}
	}
	return out, nil
}

type temListResponse struct {
	Emails []struct {
		MailRcpt      string    `json:"mail_rcpt"`
		Status        string    `json:"status"`
		StatusDetails string    `json:"status_details"`
		CreatedAt     time.Time `json:"created_at"`
		LastTries     []struct {
			Message string `json:"message"`
		} `json:"last_tries"`
	} `json:"emails"`
}

// parseFailedEmails reads one page of the provider's email list, keeping
// only failed emails with a recipient.
func parseFailedEmails(body []byte) ([]FailedEmail, error) {
	var parsed temListResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse failed emails: %w", err)
	}
	out := make([]FailedEmail, 0, len(parsed.Emails))
	for _, e := range parsed.Emails {
		rcpt := strings.ToLower(strings.TrimSpace(e.MailRcpt))
		if e.Status != "failed" || rcpt == "" {
			continue
		}
		reason := e.StatusDetails
		if n := len(e.LastTries); n > 0 && e.LastTries[n-1].Message != "" {
			reason = strings.TrimSpace(reason + ": " + e.LastTries[n-1].Message)
		}
		if len(reason) > 300 {
			reason = reason[:300]
		}
		if reason == "" {
			reason = "failed"
		}
		out = append(out, FailedEmail{Recipient: rcpt, Reason: reason, At: e.CreatedAt})
	}
	return out, nil
}
