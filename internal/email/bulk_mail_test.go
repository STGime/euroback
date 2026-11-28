package email

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestListUnsubscribeHeaders(t *testing.T) {
	got := ListUnsubscribeHeaders("https://api.example.test/platform/mailing/unsubscribe?token=t")
	want := []Header{
		{Key: "List-Unsubscribe", Value: "<https://api.example.test/platform/mailing/unsubscribe?token=t>, <" + UnsubscribeMailto + ">"},
		{Key: "List-Unsubscribe-Post", Value: "List-Unsubscribe=One-Click"},
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("with link: got %+v, want %+v", got, want)
	}
	// Without a signed link there is nothing to POST to: mailto only,
	// and no one-click header (RFC 8058 requires an HTTPS URI).
	got = ListUnsubscribeHeaders("")
	if len(got) != 1 || got[0].Key != "List-Unsubscribe" || got[0].Value != "<"+UnsubscribeMailto+">" {
		t.Fatalf("without link: got %+v", got)
	}
}

// temCapture records the TEM requests a test server receives.
type temCapture struct {
	mu   sync.Mutex
	reqs []temRequest
	fail map[string]bool // recipients answered with 400
}

func (c *temCapture) handler(w http.ResponseWriter, r *http.Request) {
	var req temRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	fail := len(req.To) == 1 && c.fail[req.To[0].Email]
	c.mu.Unlock()
	if fail {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"emails":[{"id":"e1"}]}`))
}

func TestSendBroadcast_OnePerRecipientFromBulkSender(t *testing.T) {
	cap := &temCapture{fail: map[string]bool{"bad@test.local": true}}
	srv := httptest.NewServer(http.HandlerFunc(cap.handler))
	defer srv.Close()

	svc := NewEmailService(newClientPointingAt(srv.URL), nil, "")
	svc.WithBulkSender("hello@news.example.test", "Eurobase news")

	msgs := []BroadcastMessage{
		{To: "a@test.local", HTMLBody: "<p>a</p>", UnsubscribeURL: "https://u.test/a"},
		{To: "b@test.local", HTMLBody: "<p>b</p>"},
		{To: "bad@test.local", HTMLBody: "<p>x</p>"},
	}
	res, err := svc.SendBroadcast(context.Background(), msgs, "Update")
	if err != nil {
		t.Fatalf("SendBroadcast: %v", err)
	}
	if res.Sent != 2 || res.Failed != 1 || len(res.Errors) != 1 || res.Errors[0].Recipients[0] != "bad@test.local" {
		t.Fatalf("result: %+v", res)
	}

	byTo := map[string]temRequest{}
	for _, r := range cap.reqs {
		if len(r.To) != 1 || len(r.Bcc) != 0 {
			t.Fatalf("each message must have exactly one To and no BCC: %+v", r)
		}
		if r.From.Email != "hello@news.example.test" || r.From.Name != "Eurobase news" {
			t.Errorf("from = %+v, want the bulk sender", r.From)
		}
		byTo[r.To[0].Email] = r
	}
	if got := byTo["a@test.local"]; got.HTML != "<p>a</p>" || len(got.AdditionalHeaders) != 2 {
		t.Errorf("a: body %q headers %+v", got.HTML, got.AdditionalHeaders)
	}
	if got := byTo["b@test.local"]; len(got.AdditionalHeaders) != 1 {
		t.Errorf("b (no account): headers %+v, want mailto only", got.AdditionalHeaders)
	}
}

func TestSendBroadcast_AllFail(t *testing.T) {
	cap := &temCapture{fail: map[string]bool{"x@test.local": true}}
	srv := httptest.NewServer(http.HandlerFunc(cap.handler))
	defer srv.Close()
	svc := NewEmailService(newClientPointingAt(srv.URL), nil, "")
	res, err := svc.SendBroadcast(context.Background(), []BroadcastMessage{{To: "x@test.local", HTMLBody: "b"}}, "s")
	if err == nil || res.Failed != 1 {
		t.Fatalf("want error when nothing went out; res %+v err %v", res, err)
	}
}

func TestSendBulkMail_DefaultsToServiceSender(t *testing.T) {
	cap := &temCapture{}
	srv := httptest.NewServer(http.HandlerFunc(cap.handler))
	defer srv.Close()
	svc := NewEmailService(newClientPointingAt(srv.URL), nil, "")
	svc.WithBulkSender("", "") // unset: bulk mail keeps the service's sender

	if err := svc.SendBulkMail(context.Background(), "u@test.local", "Welcome", "<p>hi</p>", "https://u.test/x"); err != nil {
		t.Fatalf("SendBulkMail: %v", err)
	}
	if len(cap.reqs) != 1 || cap.reqs[0].From.Email != "from@example.com" {
		t.Fatalf("requests: %+v", cap.reqs)
	}
	if h := cap.reqs[0].AdditionalHeaders; len(h) != 2 || h[1].Key != "List-Unsubscribe-Post" {
		t.Fatalf("headers: %+v", h)
	}
}

// Transactional sends carry no extra headers (additional_headers omitted).
func TestSendWithID_NoAdditionalHeaders(t *testing.T) {
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if _, err := newClientPointingAt(srv.URL).SendWithID(context.Background(), "u@test.local", "s", "b"); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["additional_headers"]; ok {
		t.Fatalf("transactional mail must not carry additional_headers: %v", raw)
	}
}

// A service notice goes out from the platform sender (not the bulk one),
// without List-Unsubscribe, retries a failed message once and reports
// every result.
func TestSendServiceNotice_PlatformSenderNoUnsubscribeRetry(t *testing.T) {
	serviceNoticeRetryDelay = 0
	cap := &temCapture{fail: map[string]bool{"bad@test.local": true}}
	srv := httptest.NewServer(http.HandlerFunc(cap.handler))
	defer srv.Close()
	svc := NewEmailService(newClientPointingAt(srv.URL), nil, "")
	svc.WithBulkSender("hello@news.example.test", "Eurobase news")

	var mu sync.Mutex
	results := map[string]string{}
	res, err := svc.SendServiceNotice(context.Background(), []BroadcastMessage{
		{To: "a@test.local", HTMLBody: "<p>a</p>"},
		{To: "bad@test.local", HTMLBody: "<p>x</p>"},
	}, "Terms", func(to, id string, err error) {
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			results[to] = "error"
		} else {
			results[to] = id
		}
	})
	if err != nil || res.Sent != 1 || res.Failed != 1 {
		t.Fatalf("res %+v err %v", res, err)
	}
	if results["a@test.local"] != "e1" || results["bad@test.local"] != "error" {
		t.Fatalf("results %v", results)
	}
	attempts := map[string]int{}
	for _, r := range cap.reqs {
		attempts[r.To[0].Email]++
		if r.From.Email != "from@example.com" {
			t.Errorf("from = %+v, want the platform sender", r.From)
		}
		if len(r.AdditionalHeaders) != 0 {
			t.Errorf("headers %+v, want none (no List-Unsubscribe)", r.AdditionalHeaders)
		}
	}
	if attempts["bad@test.local"] != 2 || attempts["a@test.local"] != 1 {
		t.Fatalf("attempts %v, want one retry for the failure only", attempts)
	}
}
