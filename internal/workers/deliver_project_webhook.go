package workers

// River worker for #845: delivers one project event to one customer
// webhook (public.webhooks). Enqueued by internal/events.Emitter on the
// gateway.
//
// Request (the contract in the webhooks docs, chapter 11):
//
//	POST <url>
//	Content-Type: application/json
//	X-Eurobase-Signature: hex(HMAC-SHA256(webhook secret, raw body))
//	X-Eurobase-Event: <type>         e.g. db.insert
//	X-Eurobase-Delivery: <event id>  same on every retry of this event
//	body: events.Envelope            {"id","type","created_at","project_id","table","data"}
//
// A 2xx answer is a success. Anything else (or no answer) is retried by
// River with backoff, up to DeliverProjectWebhookArgs' MaxAttempts. Each
// event keeps one row in webhook_deliveries (status, response snippet,
// attempts), which the console shows.
//
// The URL is customer-typed: it's dialled through netguard, which
// connects only to public addresses (checked after resolving, at dial
// time), never follows redirects and never uses a proxy.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eurobase/euroback/internal/events"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/eurobase/euroback/internal/netguard"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

const (
	webhookDeliveryTimeout = 15 * time.Second
	// webhookResponseSnippet caps the response body kept per delivery.
	webhookResponseSnippet = 1024
)

// DeliverProjectWebhookWorker handles jobs.DeliverProjectWebhookArgs.
type DeliverProjectWebhookWorker struct {
	river.WorkerDefaults[jobs.DeliverProjectWebhookArgs]
	Pool *pgxpool.Pool
	// Client sends the request; NewWebhookDeliveryClient in production.
	Client *http.Client
}

// Timeout bounds one attempt (River's default is a minute).
func (w *DeliverProjectWebhookWorker) Timeout(*river.Job[jobs.DeliverProjectWebhookArgs]) time.Duration {
	return webhookDeliveryTimeout + 10*time.Second
}

func (w *DeliverProjectWebhookWorker) Work(ctx context.Context, job *river.Job[jobs.DeliverProjectWebhookArgs]) error {
	a := job.Args
	var (
		endpoint, secret string
		enabled          *bool
	)
	err := w.Pool.QueryRow(ctx,
		`SELECT url, secret, enabled FROM public.webhooks WHERE id = $1::uuid`, a.WebhookID,
	).Scan(&endpoint, &secret, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // webhook deleted since the event: nothing to deliver
	}
	if err != nil {
		return fmt.Errorf("load webhook %s: %w", a.WebhookID, err)
	}
	if enabled != nil && !*enabled {
		return nil // disabled since the event
	}

	status, response, sendErr := w.send(ctx, strings.TrimSpace(endpoint), secret, a)
	success := sendErr == nil && status >= 200 && status < 300
	if sendErr != nil {
		response = sendErr.Error()
	}
	if err := w.record(ctx, a, job.Attempt, status, response, success); err != nil {
		// The delivery happened (or not) either way; don't retry a
		// success just because bookkeeping failed.
		slog.Error("webhook delivery: record attempt failed", "webhook_id", a.WebhookID, "event_id", a.EventID, "error", err)
	}
	if success {
		return nil
	}
	var permanent *permanentDeliveryError
	if errors.As(sendErr, &permanent) {
		return river.JobCancel(sendErr)
	}
	if sendErr != nil {
		return fmt.Errorf("deliver to webhook %s: %w", a.WebhookID, sendErr)
	}
	return fmt.Errorf("deliver to webhook %s: endpoint answered %d", a.WebhookID, status)
}

// permanentDeliveryError: retrying can't help (the URL itself is refused).
type permanentDeliveryError struct{ msg string }

func (e *permanentDeliveryError) Error() string { return e.msg }

func (w *DeliverProjectWebhookWorker) send(ctx context.Context, endpoint, secret string, a jobs.DeliverProjectWebhookArgs) (int, string, error) {
	if err := events.CheckWebhookURL(endpoint); err != nil {
		return 0, "", &permanentDeliveryError{msg: err.Error()}
	}
	body := []byte(a.Body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, "", &permanentDeliveryError{msg: "invalid webhook URL"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Eurobase-Webhooks/1.0")
	req.Header.Set("X-Eurobase-Signature", events.SignWebhookBody(secret, body))
	req.Header.Set("X-Eurobase-Event", a.EventType)
	req.Header.Set("X-Eurobase-Delivery", a.EventID)

	client := w.Client
	if client == nil {
		client = NewWebhookDeliveryClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, netguard.ErrNotPublic) {
			return 0, "", fmt.Errorf("the webhook host doesn't resolve to a public address")
		}
		var uerr *url.Error
		if errors.As(err, &uerr) && uerr.Timeout() {
			return 0, "", fmt.Errorf("no response within %s", webhookDeliveryTimeout)
		}
		return 0, "", fmt.Errorf("request failed: %s", redactURLError(err))
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, webhookResponseSnippet))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, validUTF8(snippet), nil
}

// record keeps one webhook_deliveries row per (webhook, event), updated
// on every attempt.
func (w *DeliverProjectWebhookWorker) record(ctx context.Context, a jobs.DeliverProjectWebhookArgs, attempt, status int, response string, success bool) error {
	var code *int
	if status > 0 {
		code = &status
	}
	_, err := w.Pool.Exec(ctx,
		`INSERT INTO public.webhook_deliveries
		        (webhook_id, event_id, event, payload, status_code, response, attempts, success, updated_at)
		 VALUES ($1::uuid, $2::uuid, $3, $4::jsonb, $5, $6, $7, $8, now())
		 ON CONFLICT (webhook_id, event_id) WHERE event_id IS NOT NULL DO UPDATE
		    SET status_code = EXCLUDED.status_code,
		        response    = EXCLUDED.response,
		        attempts    = GREATEST(public.webhook_deliveries.attempts, EXCLUDED.attempts),
		        success     = EXCLUDED.success,
		        updated_at  = now()`,
		a.WebhookID, a.EventID, a.EventType, string(a.Body), code, response, attempt, success)
	return err
}

// NewWebhookDeliveryClient returns the client deliveries use: connects
// only to public addresses (netguard), no redirects, no proxy.
func NewWebhookDeliveryClient() *http.Client {
	d := &netguard.Dialer{Timeout: 5 * time.Second}
	return &http.Client{
		Timeout: webhookDeliveryTimeout,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				return d.DialContext(ctx, host, port)
			},
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: webhookDeliveryTimeout,
			MaxIdleConns:          50,
			IdleConnTimeout:       60 * time.Second,
			ForceAttemptHTTP2:     true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// redactURLError drops the URL from a transport error (it's in the row
// already) and keeps the cause.
func redactURLError(err error) string {
	var uerr *url.Error
	if errors.As(err, &uerr) && uerr.Err != nil {
		return uerr.Err.Error()
	}
	return err.Error()
}

func validUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return strings.ToValidUTF8(string(b), "�")
}
