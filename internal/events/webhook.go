package events

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"

	"github.com/eurobase/euroback/internal/netguard"
)

// MaxWebhookURLLength caps a stored webhook URL.
const MaxWebhookURLLength = 2048

// SignWebhookBody is the X-Eurobase-Signature value: hex HMAC-SHA256 of
// the raw body, keyed with the webhook's secret as shown in the console
// (the docs' Node example: createHmac('sha256', secret).update(payload)).
func SignWebhookBody(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// CheckWebhookURL is the rule for a webhook endpoint, applied when one is
// saved and again before every delivery: an absolute https URL, no
// credentials in it, and a host that can be a public server (not
// localhost, a single label, an internal suffix or a non-public
// address literal). Whether the name resolves to a public address is
// checked when connecting.
func CheckWebhookURL(raw string) error {
	if len(raw) > MaxWebhookURLLength {
		return errors.New("webhook URL is too long")
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return errors.New("webhook URL must be an absolute https:// URL")
	}
	if u.Scheme != "https" {
		return errors.New("webhook URL must use https")
	}
	if u.User != nil {
		return errors.New("webhook URL must not contain credentials")
	}
	if err := netguard.CheckHostName(u.Hostname()); err != nil {
		return errors.New("webhook URL must point to a public internet host")
	}
	return nil
}
