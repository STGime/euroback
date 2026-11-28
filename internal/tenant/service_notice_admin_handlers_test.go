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
)

type stubNoticeEmailer struct{ msgs []email.BroadcastMessage }

func (s *stubNoticeEmailer) SendServiceNotice(_ context.Context, msgs []email.BroadcastMessage, _ string) (email.BulkResult, error) {
	s.msgs = append([]email.BroadcastMessage(nil), msgs...)
	return email.BulkResult{Sent: len(msgs)}, nil
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
	}
	t.Cleanup(func() {
		for _, a := range accounts {
			_, _ = pool.Exec(context.Background(), `DELETE FROM public.mailing_preferences WHERE user_id IN (SELECT id FROM public.platform_users WHERE email = $1)`, a.email)
			_, _ = pool.Exec(context.Background(), `DELETE FROM public.platform_users WHERE email = $1`, a.email)
		}
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

	rec := httptest.NewRecorder()
	AdminServiceNoticeAudience(pool)(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	var aud struct{ Recipients int }
	if err := json.NewDecoder(rec.Body).Decode(&aud); err != nil || aud.Recipients == 0 {
		t.Fatalf("audience: %d %v (%s)", aud.Recipients, err, rec.Body)
	}

	send := func(expected int) (*httptest.ResponseRecorder, *stubNoticeEmailer) {
		m := &stubNoticeEmailer{}
		body := fmt.Sprintf(`{"subject":"Terms","body_html":"<html><body><p>Hi</p></body></html>","expected_recipients":%d}`, expected)
		rec := httptest.NewRecorder()
		AdminSendServiceNotice(pool, m)(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body)))
		return rec, m
	}
	if rec, _ := send(aud.Recipients + 1); rec.Code != http.StatusConflict {
		t.Fatalf("stale count: status %d, want 409", rec.Code)
	}
	rec, m := send(aud.Recipients)
	if rec.Code != http.StatusAccepted {
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
		want := a.verified && !a.suspended && !strings.HasSuffix(a.email, "@example.com")
		if slices.Contains(got, a.email) != want {
			t.Errorf("%s: sent=%v, want %v", a.email, !want, want)
		}
	}
}
