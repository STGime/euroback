// Package sms provides GatewayAPI integration for transactional SMS (OTP).
package sms

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Client sends SMS via the GatewayAPI REST API.
type Client struct {
	httpClient *http.Client
	apiToken   string
	sender     string
}

// NewClient creates a new GatewayAPI SMS client.
// If apiToken is empty, SMS will be logged instead of sent.
func NewClient(apiToken, sender string) *Client {
	if sender == "" {
		sender = "Eurobase"
	}
	// GatewayAPI alphanumeric sender max 11 chars.
	if len(sender) > 11 {
		sender = sender[:11]
	}
	return &Client{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		apiToken:   apiToken,
		sender:     sender,
	}
}

// Configured returns true if the client has API credentials.
func (c *Client) Configured() bool {
	return c.apiToken != ""
}

// gatewayRequest is the JSON body for the GatewayAPI send SMS endpoint.
type gatewayRequest struct {
	Sender     string              `json:"sender"`
	Message    string              `json:"message"`
	Recipients []gatewayRecipient  `json:"recipients"`
}

type gatewayRecipient struct {
	MSISDN int64 `json:"msisdn"`
}

// Send sends an SMS to the given phone number via GatewayAPI.
// phone must be in E.164 format (e.g., "+33612345678").
func (c *Client) Send(ctx context.Context, phone, message string) error {
	if !c.Configured() {
		slog.Warn("sms not configured, logging instead",
			"phone", phone,
			"message", message,
		)
		return nil
	}

	msisdn, err := parseMSISDN(phone)
	if err != nil {
		return fmt.Errorf("invalid phone number %q: %w", phone, err)
	}

	payload := gatewayRequest{
		Sender:     c.sender,
		Message:    message,
		Recipients: []gatewayRecipient{{MSISDN: msisdn}},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal sms payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://gatewayapi.com/rest/mtsms", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create sms request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Token "+c.apiToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		slog.Error("GatewayAPI request failed", "error", err, "phone", phone)
		return fmt.Errorf("GatewayAPI request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		slog.Error("GatewayAPI error",
			"status", resp.StatusCode,
			"body", string(respBody),
			"phone", phone,
		)
		return fmt.Errorf("GatewayAPI returned %d: %s", resp.StatusCode, string(respBody))
	}

	slog.Info("sms sent", "phone", phone)
	return nil
}

// parseMSISDN converts an E.164 phone number to an integer MSISDN.
// E.164 format: "+33612345678" → 33612345678
func parseMSISDN(phone string) (int64, error) {
	phone = strings.TrimSpace(phone)
	if !strings.HasPrefix(phone, "+") {
		return 0, fmt.Errorf("phone must start with +")
	}
	digits := phone[1:]
	if len(digits) < 7 || len(digits) > 15 {
		return 0, fmt.Errorf("phone must be 7-15 digits")
	}
	var msisdn int64
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("phone contains non-digit character")
		}
		msisdn = msisdn*10 + int64(c-'0')
	}
	return msisdn, nil
}
