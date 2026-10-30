package email

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
		emails, pageLen, err := parseFailedEmailsPage(body)
		if err != nil {
			return nil, err
		}
		out = append(out, emails...)
		if pageLen < failedEmailsPageSize {
			return out, nil
		}
	}
	slog.Warn("listing failed emails stopped at the page limit", "pages", failedEmailsMaxPages)
	return out, nil
}

type temListResponse struct {
	Emails []struct {
		MailRcpt      string    `json:"mail_rcpt"`
		Status        string    `json:"status"`
		StatusDetails string    `json:"status_details"`
		Flags         []string  `json:"flags"`
		CreatedAt     time.Time `json:"created_at"`
		LastTries     []struct {
			Message string `json:"message"`
		} `json:"last_tries"`
	} `json:"emails"`
}

// parseFailedEmails reads one page of the provider's email list, keeping
// only permanent failures: emails the provider flags as a hard bounce, a
// missing mailbox or a blocklisted recipient, or whose recipient domain
// doesn't exist. Other failures (an unreachable mail server, a rejection
// the provider doesn't flag) may be temporary and are not kept.
func parseFailedEmails(body []byte) ([]FailedEmail, error) {
	out, _, err := parseFailedEmailsPage(body)
	return out, err
}

// parseFailedEmailsPage also returns how many emails the page held (for
// paging), before filtering.
func parseFailedEmailsPage(body []byte) ([]FailedEmail, int, error) {
	var parsed temListResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, 0, fmt.Errorf("parse failed emails: %w", err)
	}
	out := make([]FailedEmail, 0, len(parsed.Emails))
	for _, e := range parsed.Emails {
		rcpt := strings.ToLower(strings.TrimSpace(e.MailRcpt))
		if e.Status != "failed" || rcpt == "" {
			continue
		}
		lastMsg := ""
		if n := len(e.LastTries); n > 0 {
			lastMsg = e.LastTries[n-1].Message
		}
		if !isHardFailure(e.Flags, lastMsg) {
			continue
		}
		reason := e.StatusDetails
		if len(e.Flags) > 0 {
			reason = strings.TrimSpace(reason + " [" + strings.Join(e.Flags, ",") + "]")
		}
		if lastMsg != "" {
			reason = strings.TrimSpace(reason + ": " + lastMsg)
		}
		if r := []rune(reason); len(r) > 300 {
			reason = string(r[:300])
		}
		reason = strings.ToValidUTF8(reason, "")
		if reason == "" {
			reason = "failed"
		}
		out = append(out, FailedEmail{Recipient: rcpt, Reason: reason, At: e.CreatedAt})
	}
	return out, len(parsed.Emails), nil
}

// isHardFailure: the provider's own permanent-failure flags, or a
// recipient domain that doesn't exist.
func isHardFailure(flags []string, lastMessage string) bool {
	for _, f := range flags {
		switch f {
		case "hard_bounce", "mailbox_not_found", "blocklisted":
			return true
		}
	}
	return strings.Contains(strings.ToLower(lastMessage), "non-existing domain")
}
