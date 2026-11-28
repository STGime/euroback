package tenant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/email"
	"github.com/eurobase/euroback/internal/mailaddr"
)

// stubNoticeEmailer records the messages; addresses in fail get an error.
type stubNoticeEmailer struct {
	msgs []email.BroadcastMessage
	fail map[string]bool
}

func (s *stubNoticeEmailer) SendServiceNotice(_ context.Context, msgs []email.BroadcastMessage, _ string, onResult func(to, id string, err error)) (email.BulkResult, error) {
	s.msgs = append([]email.BroadcastMessage(nil), msgs...)
	res := email.BulkResult{}
	for _, m := range msgs {
		var err error
		if s.fail[m.To] {
			err = fmt.Errorf("provider error")
			res.Failed++
		} else {
			res.Sent++
		}
		if onResult != nil {
			onResult(m.To, "msg-"+m.To, err)
		}
	}
	return res, nil
}

// A service notice reaches every verified, active account, including
// one that turned off product updates; it skips unverified, suspended
// and reserved addresses, and refuses a stale recipient count.
func TestAdminSendServiceNotice_Audience(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	tag := fmt.Sprintf("sn%d", time.Now().UnixNano())
	addr := func(name string) string { return name + "-" + tag + "@notice.eurobase.local" }
	accounts := []struct {
		email                       string
		verified, suspended, optOut bool
	}{
		{addr("ok"), true, false, false},
		{addr("optedout"), true, false, true},
		{addr("unverified"), false, false, false},
		{addr("suspended"), true, true, false},
		{"reserved-" + tag + "@example.com", true, false, false},
		{addr("suppressed"), true, false, false},
		{addr("bounceblocked"), true, false, false},
	}
	subject := "Terms " + tag
	t.Cleanup(func() {
		for _, a := range accounts {
			_, _ = pool.Exec(context.Background(), `DELETE FROM public.mailing_preferences WHERE user_id IN (SELECT id FROM public.platform_users WHERE email = $1)`, a.email)
			_, _ = pool.Exec(context.Background(), `DELETE FROM public.platform_users WHERE email = $1`, a.email)
			_, _ = pool.Exec(context.Background(), `DELETE FROM public.email_suppressions WHERE email = $1`, a.email)
			_, _ = pool.Exec(context.Background(), `DELETE FROM public.email_bounce_blocks WHERE address_hash = $1`, mailaddr.Hash(a.email))
		}
		_, _ = pool.Exec(context.Background(), `DELETE FROM public.service_notice_deliveries WHERE notice_key = $1`, noticeKey(subject))
	})
	for _, a := range accounts {
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO public.platform_users (email, email_confirmed_at, suspended_at)
			VALUES ($1, CASE WHEN $2 THEN now() END, CASE WHEN $3 THEN now() END) RETURNING id`,
			a.email, a.verified, a.suspended).Scan(&id); err != nil {
			t.Fatalf("insert %s: %v", a.email, err)
		}
		if a.optOut {
			if _, err := pool.Exec(ctx, `INSERT INTO public.mailing_preferences (user_id, category, opted_out_at) VALUES ($1, 'all', now())`, id); err != nil {
				t.Fatalf("opt out: %v", err)
			}
		}
	}

	if _, err := pool.Exec(ctx, `INSERT INTO public.email_suppressions (email, reason) VALUES ($1, 'test') ON CONFLICT DO NOTHING`, addr("suppressed")); err != nil {
		t.Fatalf("suppress: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.email_bounce_blocks (address_hash, kind, blocked_until) VALUES ($1, 'mailbox_full', now() + interval '1 day')`, mailaddr.Hash(addr("bounceblocked"))); err != nil {
		t.Fatalf("bounce block: %v", err)
	}

	count := func() int {
		rec := httptest.NewRecorder()
		AdminServiceNoticeAudience(pool)(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		var aud struct{ Recipients int }
		if err := json.NewDecoder(rec.Body).Decode(&aud); err != nil || aud.Recipients == 0 {
			t.Fatalf("audience: %d %v (%s)", aud.Recipients, err, rec.Body)
		}
		return aud.Recipients
	}
	sendOnce := func(m *stubNoticeEmailer, expected int) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"subject":%q,"body_html":"<html><body><p>Hi</p></body></html>","expected_recipients":%d}`, subject, expected)
		rec := httptest.NewRecorder()
		AdminSendServiceNotice(pool, m)(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body)))
		return rec
	}
	// Other tests may add accounts meanwhile: retry once with the
	// count the 409 reports.
	send := func(m *stubNoticeEmailer) *httptest.ResponseRecorder {
		rec := sendOnce(m, count())
		if rec.Code == http.StatusConflict {
			var c struct{ Recipients int }
			_ = json.Unmarshal(rec.Body.Bytes(), &c)
			if c.Recipients > 0 {
				rec = sendOnce(m, c.Recipients)
			}
		}
		return rec
	}

	if rec := sendOnce(&stubNoticeEmailer{}, count()+1000); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"recipients"`) {
		t.Fatalf("stale count: status %d (%s), want 409 with the count", rec.Code, rec.Body)
	}
	// First send: the "ok" address fails at the provider.
	m := &stubNoticeEmailer{fail: map[string]bool{addr("ok"): true}}
	if rec := send(m); rec.Code != http.StatusAccepted {
		t.Fatalf("send: status %d (%s)", rec.Code, rec.Body)
	}
	var got []string
	for _, msg := range m.msgs {
		got = append(got, msg.To)
		if !strings.Contains(msg.HTMLBody, "service notice") || msg.UnsubscribeURL != "" {
			t.Fatalf("message to %s lacks the service footer or has an unsubscribe link", msg.To)
		}
	}
	for _, a := range accounts {
		want := a.verified && !a.suspended && !strings.HasSuffix(a.email, "@example.com") &&
			!strings.Contains(a.email, "suppressed") && !strings.Contains(a.email, "bounceblocked")
		if slices.Contains(got, a.email) != want {
			t.Errorf("%s: sent=%v, want %v", a.email, !want, want)
		}
	}
	var status, msgID string
	if err := pool.QueryRow(ctx, `SELECT status, COALESCE(message_id, '') FROM public.service_notice_deliveries WHERE notice_key = $1 AND email = $2`,
		noticeKey(subject), addr("optedout")).Scan(&status, &msgID); err != nil || status != "sent" || msgID == "" {
		t.Fatalf("delivery record for optedout: %q %q %v", status, msgID, err)
	}

	// Sending the same notice again only retries the address that failed.
	m2 := &stubNoticeEmailer{}
	if rec := send(m2); rec.Code != http.StatusAccepted {
		t.Fatalf("resend: status %d (%s)", rec.Code, rec.Body)
	}
	if len(m2.msgs) != 1 || m2.msgs[0].To != addr("ok") {
		var to []string
		for _, msg := range m2.msgs {
			to = append(to, msg.To)
		}
		if !slices.Contains(to, addr("ok")) || slices.Contains(to, addr("optedout")) {
			t.Fatalf("resend went to %v; want the failed address and not the ones already sent", to)
		}
	}
	// A third send has nobody left among our accounts.
	m3 := &stubNoticeEmailer{}
	_ = send(m3)
	for _, msg := range m3.msgs {
		if msg.To == addr("ok") || msg.To == addr("optedout") {
			t.Fatalf("third send mailed %s again", msg.To)
		}
	}
}
