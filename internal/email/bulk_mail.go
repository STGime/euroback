package email

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Bulk mail (product updates, the onboarding series) is kept apart from
// transactional mail (verification, password reset, sign-in links,
// project auth mail) in two ways:
//
//   - It can go out from its own sender address (EMAIL_FROM_ADDRESS_BULK,
//     e.g. on a subdomain registered as its own TEM domain), so how
//     recipients treat newsletters doesn't weigh on the domain that
//     carries sign-in mail. Unset, bulk mail uses the service's sender.
//   - Every message goes to one recipient and carries List-Unsubscribe
//     (RFC 2369) and, with a signed link, List-Unsubscribe-Post (RFC 8058
//     one-click; UnsubscribeHandler accepts the POST with the token in
//     the URL), so mail clients can offer "unsubscribe" instead of
//     "report spam".

// UnsubscribeMailto is the fallback unsubscribe address in
// List-Unsubscribe, for recipients without a signed link (no account).
const UnsubscribeMailto = "mailto:contact@eurobase.app?subject=Unsubscribe"

// broadcastConcurrency bounds parallel TEM requests in SendBroadcast.
const broadcastConcurrency = 8

// serviceNoticeConcurrency is lower: service notices go out from the
// platform sender, which also carries sign-in and verification mail.
const serviceNoticeConcurrency = 2

// serviceNoticeRetryDelay is the wait before the one retry of a failed
// service-notice message (a TEM 429 / 5xx is usually brief).
var serviceNoticeRetryDelay = 3 * time.Second

// ListUnsubscribeHeaders returns the List-Unsubscribe headers for one
// message. With a signed unsubscribe URL the message gets one-click
// unsubscribe; without, only the mailto fallback.
func ListUnsubscribeHeaders(unsubscribeURL string) []Header {
	if unsubscribeURL == "" {
		return []Header{{Key: "List-Unsubscribe", Value: "<" + UnsubscribeMailto + ">"}}
	}
	return []Header{
		{Key: "List-Unsubscribe", Value: "<" + unsubscribeURL + ">, <" + UnsubscribeMailto + ">"},
		{Key: "List-Unsubscribe-Post", Value: "List-Unsubscribe=One-Click"},
	}
}

// WithBulkSender makes bulk mail go out from fromEmail / fromName (same
// TEM credentials). An empty fromEmail leaves bulk mail on the service's
// own sender.
func (s *EmailService) WithBulkSender(fromEmail, fromName string) *EmailService {
	if fromEmail != "" {
		s.bulk = s.client.WithFrom(fromEmail, fromName)
	}
	return s
}

func (s *EmailService) bulkClient() *EmailClient {
	if s.bulk != nil {
		return s.bulk
	}
	return s.client
}

// SendBulkMail sends one bulk message (onboarding series, product
// update) to one recipient from the bulk sender, with List-Unsubscribe
// headers for unsubscribeURL ("" = mailto fallback only).
func (s *EmailService) SendBulkMail(ctx context.Context, to, subject, htmlBody, unsubscribeURL string) error {
	_, err := s.bulkClient().SendWithHeaders(ctx, to, subject, htmlBody, ListUnsubscribeHeaders(unsubscribeURL))
	return err
}

// BroadcastMessage is one recipient of a broadcast: the address, the
// body for that recipient (it carries their own unsubscribe link) and
// their signed unsubscribe URL ("" when they have none).
type BroadcastMessage struct {
	To             string
	HTMLBody       string
	UnsubscribeURL string
}

// SendBroadcast sends each message separately (never BCC) from the bulk
// sender, a few in parallel. Failures are collected per recipient in
// BulkResult.Errors; the error is non-nil only when nothing went out.
func (s *EmailService) SendBroadcast(ctx context.Context, msgs []BroadcastMessage, subject string) (BulkResult, error) {
	return sendEach(ctx, s.bulkClient(), msgs, subject, broadcastConcurrency, 0, func(m BroadcastMessage) []Header {
		return ListUnsubscribeHeaders(m.UnsubscribeURL)
	}, nil)
}

// SendServiceNotice sends a service notice (a change to the Terms, for
// example) to each recipient separately, from the platform's own sender
// rather than the bulk one, without List-Unsubscribe: account holders
// get it whatever their product-update preferences. It sends slower than
// a broadcast and retries a failed message once. onResult (may be nil)
// is called once per message with the provider's message id or the
// error, so the caller can record who got it.
func (s *EmailService) SendServiceNotice(ctx context.Context, msgs []BroadcastMessage, subject string, onResult func(to, messageID string, err error)) (BulkResult, error) {
	return sendEach(ctx, s.client, msgs, subject, serviceNoticeConcurrency, 1, func(BroadcastMessage) []Header { return nil }, onResult)
}

func sendEach(ctx context.Context, client *EmailClient, msgs []BroadcastMessage, subject string, concurrency, retries int, headers func(BroadcastMessage) []Header, onResult func(to, messageID string, err error)) (BulkResult, error) {
	res := BulkResult{}
	if !client.Configured() {
		return res, fmt.Errorf("email client not configured (TEM credentials missing)")
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	for _, m := range msgs {
		wg.Add(1)
		sem <- struct{}{}
		go func(m BroadcastMessage) {
			defer wg.Done()
			defer func() { <-sem }()
			id, err := client.SendWithHeaders(ctx, m.To, subject, m.HTMLBody, headers(m))
			for i := 0; err != nil && i < retries && ctx.Err() == nil; i++ {
				select {
				case <-time.After(serviceNoticeRetryDelay):
				case <-ctx.Done():
				}
				id, err = client.SendWithHeaders(ctx, m.To, subject, m.HTMLBody, headers(m))
			}
			if onResult != nil {
				onResult(m.To, id, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				res.Failed++
				res.Errors = append(res.Errors, BulkChunkError{Recipients: []string{m.To}, Error: err.Error()})
				return
			}
			res.Sent++
		}(m)
	}
	wg.Wait()

	if res.Sent == 0 && res.Failed > 0 {
		return res, fmt.Errorf("broadcast: every send failed (%d)", res.Failed)
	}
	return res, nil
}
