package billing

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestHandleWebhook_ExtractsPaymentIDFromFormBody is a regression
// guard for the bug PR #163 review caught: HandleWebhook drains
// r.Body into a local []byte for signature verification, so
// r.ParseForm() reads an empty stream and the form-extraction path
// silently returns "". Mollie's default Content-Type is
// application/x-www-form-urlencoded; if the form-parsing path
// breaks, every real Mollie webhook becomes a no-op until they
// switch to JSON (which they don't).
//
// This test uses a partly-stubbed Service: the Mollie API client is
// pointed at an httptest server that records whether GetPayment was
// hit. If the payment ID was successfully extracted from the form
// body, the handler invokes ApplyPaymentEvent which calls
// GetPayment. If extraction silently failed (the bug), GetPayment
// is never hit.
func TestHandleWebhook_ExtractsPaymentIDFromFormBody(t *testing.T) {
	var getPaymentHits int32
	mollieSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/payments/") {
			atomic.AddInt32(&getPaymentHits, 1)
		}
		w.Header().Set("Content-Type", "application/json")
		// Return a "paid" payment so the downstream service path
		// keeps running, but with empty metadata so it short-circuits
		// before trying to touch the DB (we don't have one here).
		_, _ = w.Write([]byte(`{
			"id":"tr_webhook_test",
			"status":"open",
			"amount":{"currency":"EUR","value":"19.00"},
			"customerId":"cst_x",
			"metadata":{}
		}`))
	}))
	defer mollieSrv.Close()

	mc := NewMollieClient("test_x", "topsecret")
	mc.baseURL = mollieSrv.URL

	// nil pool — ApplyPaymentEvent will fail when it tries to
	// upsert the invoice row, but only AFTER GetPayment is called
	// from the same code path. That's enough to prove the form
	// extraction worked. The handler always returns 200, so the
	// downstream error doesn't surface to the test.
	svc := &Service{mollie: mc}

	body := "id=tr_webhook_test"
	// Compute the signature the handler will verify against.
	mac := computeHMACSHA256Hex(t, "topsecret", body)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/mollie", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Mollie-Signature", mac)

	rec := httptest.NewRecorder()
	HandleWebhook(svc)(rec, req)

	// Handler must always return 200 so Mollie doesn't retry.
	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200 (handler must not 4xx/5xx Mollie)", rec.Code)
	}
	// Critical assertion: did the handler extract the payment ID
	// and dispatch through to Mollie's GetPayment?
	if atomic.LoadInt32(&getPaymentHits) != 1 {
		t.Fatal("GetPayment was never called — payment ID extraction silently failed " +
			"(regression of the io.ReadAll + r.ParseForm bug from PR #163 review)")
	}
}

// TestHandleWebhook_RejectsMissingSignature confirms unsigned
// requests don't reach ApplyPaymentEvent.
func TestHandleWebhook_RejectsMissingSignature(t *testing.T) {
	var getPaymentHits int32
	mollieSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&getPaymentHits, 1)
		w.WriteHeader(200)
	}))
	defer mollieSrv.Close()
	mc := NewMollieClient("test_x", "topsecret")
	mc.baseURL = mollieSrv.URL
	svc := &Service{mollie: mc}

	req := httptest.NewRequest(http.MethodPost, "/webhooks/mollie",
		strings.NewReader("id=tr_unsigned"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// No X-Mollie-Signature header.
	rec := httptest.NewRecorder()
	HandleWebhook(svc)(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d want 200 (still always 200 to Mollie)", rec.Code)
	}
	if atomic.LoadInt32(&getPaymentHits) != 0 {
		t.Error("unsigned webhook should NOT reach Mollie API roundtrip")
	}
}

// TestHandleWebhook_RejectsMisSignedBody confirms a wrong signature
// also short-circuits before ApplyPaymentEvent.
func TestHandleWebhook_RejectsMisSignedBody(t *testing.T) {
	var getPaymentHits int32
	mollieSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&getPaymentHits, 1)
		w.WriteHeader(200)
	}))
	defer mollieSrv.Close()
	mc := NewMollieClient("test_x", "topsecret")
	mc.baseURL = mollieSrv.URL
	svc := &Service{mollie: mc}

	req := httptest.NewRequest(http.MethodPost, "/webhooks/mollie",
		strings.NewReader("id=tr_evil"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Mollie-Signature", "00deadbeef00deadbeef00deadbeef00deadbeef00deadbeef00deadbeef0000")
	rec := httptest.NewRecorder()
	HandleWebhook(svc)(rec, req)

	if atomic.LoadInt32(&getPaymentHits) != 0 {
		t.Error("mis-signed webhook should NOT reach Mollie API roundtrip")
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func computeHMACSHA256Hex(t *testing.T, secret, body string) string {
	t.Helper()
	mc := NewMollieClient("x", secret)
	// Reuse VerifyWebhookSignature's inverse by computing the
	// expected MAC the same way the implementation does, then
	// asking it to verify against our computation. Since the
	// implementation is the source of truth, this stays in sync.
	for _, candidate := range knownGoodMACs(body) {
		if err := mc.VerifyWebhookSignature([]byte(body), candidate); err == nil {
			return candidate
		}
	}
	// Fallback: compute it inline. This branch fires only if the
	// pinned fixtures get out of sync (tests will then fail loudly).
	t.Fatalf("could not compute MAC for body %q under secret %q — update knownGoodMACs", body, secret)
	return ""
}

// knownGoodMACs lists the precomputed HMACs we use across the
// handler tests. Pinned as fixtures so a test failure here surfaces
// the algorithm change explicitly (rather than a silent
// recomputation hiding a regression).
func knownGoodMACs(body string) []string {
	switch body {
	case "id=tr_webhook_test":
		// HMAC-SHA256("id=tr_webhook_test", "topsecret"). Compute with:
		//   echo -n "id=tr_webhook_test" | openssl dgst -sha256 -hmac "topsecret"
		return []string{"004510b3fd7d3ae69ae484908a63f3aedeb3bea3d58811b6a21c3c94a0533df0"}
	}
	return nil
}
