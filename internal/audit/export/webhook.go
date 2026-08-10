package export

// Webhook deliverer (#354). Builds a JSON envelope of audit_log
// events, HMAC-signs the canonicalised body with the tenant's
// vault-stored secret, and POSTs to the destination endpoint.
//
// Envelope shape (json format — cef is a future extension):
//
//   {
//     "events": [
//       {"id": ..., "project_id": ..., "action": ..., ...audit_log row},
//       ...
//     ],
//     "cursor":       <max(seq) in this batch>,
//     "delivered_at": "<RFC3339>"
//   }
//
// Headers on the POST:
//   Content-Type:          application/json
//   X-Eurobase-Timestamp:  <unix seconds> — replay-window guard
//   X-Eurobase-Signature:  sha256=<hex(HMAC-SHA256(secret, ts + "." + body))>
//
// The timestamp is included in the HMAC input so a replay of a
// captured request cannot be reused an hour later — sinks that
// verify the signature MUST also enforce |now - ts| ≤ 300s.
// Documented in docs/compliance/audit-export.md.
//
// Missing secret (secret_ref = NULL in the DB): the deliverer omits
// BOTH headers rather than signing with an empty key (an HMAC with
// a known key is a checksum, not a signature). The console labels
// such destinations "unauthenticated" so operators make the trust
// choice knowingly (#356 review).

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// EventRow is the shape of one audit_log row inside the envelope.
// Field names deliberately match the DB columns so a downstream
// consumer can restore into the same schema without translation.
type EventRow struct {
	ID         string          `json:"id"`
	ProjectID  *string         `json:"project_id,omitempty"`
	ActorID    *string         `json:"actor_id,omitempty"`
	ActorEmail string          `json:"actor_email"`
	Action     string          `json:"action"`
	TargetType *string         `json:"target_type,omitempty"`
	TargetID   *string         `json:"target_id,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
	IPAddress  *string         `json:"ip_address,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	Seq        int64           `json:"seq"`
	RowHash    string          `json:"row_hash"`
}

// Envelope is the full POST body.
type Envelope struct {
	Events      []EventRow `json:"events"`
	Cursor      int64      `json:"cursor"`
	DeliveredAt time.Time  `json:"delivered_at"`
}

// DeliveryResult carries the outcome of a single POST — enough
// for the caller to decide whether to advance the cursor (only on
// 2xx) and what to log/audit on failure.
type DeliveryResult struct {
	StatusCode int
	Cursor     int64 // max seq delivered on success; unchanged on failure
	Err        error
}

// Deliverer is the reusable transport around an SSRF-safe client.
// One Deliverer per worker process is fine — it's stateless.
type Deliverer struct {
	client *http.Client
}

func NewDeliverer(cfg ClientConfig) *Deliverer {
	return &Deliverer{client: NewSSRFSafeClient(cfg)}
}

// PostEnvelope signs (if secret is non-empty) and POSTs the envelope
// to endpoint. Returns DeliveryResult; Err is non-nil for any
// transport failure OR any non-2xx response (the caller uses this
// to hold the cursor).
//
// secret is the raw HMAC signing key resolved from the tenant vault
// by the caller — this function does not touch the vault so it stays
// unit-testable.
func (d *Deliverer) PostEnvelope(ctx context.Context, endpoint string, secret []byte, env *Envelope) DeliveryResult {
	body, err := json.Marshal(env)
	if err != nil {
		return DeliveryResult{Err: fmt.Errorf("marshal envelope: %w", err)}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return DeliveryResult{Err: fmt.Errorf("build request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "eurobase-audit-webhook/1.0")

	if len(secret) > 0 {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		sig := hex.EncodeToString(SignEnvelope(secret, ts, body))
		req.Header.Set("X-Eurobase-Timestamp", ts)
		req.Header.Set("X-Eurobase-Signature", "sha256="+sig)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return DeliveryResult{Err: err}
	}
	defer resp.Body.Close()

	// Drain up to 4 KiB of the response body so keep-alive can be
	// reused (we DisableKeepAlives elsewhere, but this is cheap
	// insurance if that ever flips) and any sink-side error message
	// is available in logs.
	_, _ = io.CopyN(io.Discard, resp.Body, 4096)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return DeliveryResult{
			StatusCode: resp.StatusCode,
			Err:        fmt.Errorf("sink returned %d %s", resp.StatusCode, resp.Status),
		}
	}
	return DeliveryResult{StatusCode: resp.StatusCode, Cursor: env.Cursor}
}

// SignEnvelope returns the raw (unhex) HMAC-SHA256 over
// timestamp || "." || body. Split out so the docs' verification
// example is byte-exact against what the deliverer computes — one
// implementation, one canonicalisation.
//
// Canonicalisation: the timestamp (as decimal unix seconds), a
// literal ".", and the JSON body as-emitted by the deliverer.
// Sinks MUST use the raw request body they received (not
// re-serialise it) so JSON-key ordering / whitespace doesn't drift.
func SignEnvelope(secret []byte, timestamp string, body []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return mac.Sum(nil)
}
