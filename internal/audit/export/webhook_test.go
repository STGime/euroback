package export

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// #354: the docs promise a specific canonicalisation for the HMAC
// input — timestamp + "." + raw body. Pin it so any change to the
// signing code fails this test AND the docs' verification examples
// stop matching, forcing both to update in lockstep.
func TestSignEnvelope_MatchesDocs(t *testing.T) {
	secret := []byte("test-secret-value")
	ts := "1786134214"
	body := []byte(`{"events":[],"cursor":0,"delivered_at":"2026-08-10T12:35:00Z"}`)

	got := SignEnvelope(secret, ts, body)

	// Independent recomputation matching the Python/Node examples
	// in docs/compliance/audit-export.md.
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(ts))
	m.Write([]byte{'.'})
	m.Write(body)
	want := m.Sum(nil)

	if !hmac.Equal(got, want) {
		t.Fatalf("SignEnvelope diverged from documented HMAC: got %x want %x", got, want)
	}
	// Sanity: not all zeros.
	empty := true
	for _, b := range got {
		if b != 0 {
			empty = false
			break
		}
	}
	if empty {
		t.Error("signature is all zeros")
	}
}

// #354 end-to-end: PostEnvelope produces headers the sink can verify
// with the exact algorithm the docs specify. Reversing the check on
// the sink side pins the wire format.
func TestPostEnvelope_HeadersAndBody(t *testing.T) {
	secret := []byte("shared")
	env := &Envelope{
		Events: []EventRow{
			{ID: "abc", ActorEmail: "test@eurobase.app", Action: "audit_export.test",
				CreatedAt: time.Date(2026, 8, 10, 12, 34, 56, 0, time.UTC), Seq: 42, RowHash: "deadbeef"},
		},
		Cursor:      42,
		DeliveredAt: time.Date(2026, 8, 10, 12, 35, 0, 0, time.UTC),
	}

	var (
		sawSig       string
		sawTs        string
		sawBody      []byte
		sawUserAgent string
	)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawSig = r.Header.Get("X-Eurobase-Signature")
		sawTs = r.Header.Get("X-Eurobase-Timestamp")
		sawUserAgent = r.Header.Get("User-Agent")
		sawBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Use the server's TLS client so the SSRF check on the
	// dial-time IP can be swapped for a permissive one — httptest
	// binds to 127.0.0.1 which our SSRF client would reject in
	// prod. For this test we hand-build a plain client because the
	// URL scheme + cert are what httptest gives us; the SSRF client
	// is exercised in a separate test below.
	client := srv.Client()
	d := &Deliverer{client: client}

	res := d.PostEnvelope(context.Background(), srv.URL, secret, env)
	if res.Err != nil {
		t.Fatalf("PostEnvelope error: %v", res.Err)
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", res.StatusCode)
	}
	if res.Cursor != 42 {
		t.Errorf("Cursor = %d, want 42", res.Cursor)
	}

	if !strings.HasPrefix(sawSig, "sha256=") {
		t.Fatalf("signature header missing sha256= prefix: %q", sawSig)
	}
	// Sink-side reverse check — the docs example distilled.
	ts, err := strconv.ParseInt(sawTs, 10, 64)
	if err != nil || ts == 0 {
		t.Fatalf("bad timestamp header: %q", sawTs)
	}
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(sawTs))
	m.Write([]byte{'.'})
	m.Write(sawBody)
	want := "sha256=" + hex.EncodeToString(m.Sum(nil))
	if want != sawSig {
		t.Errorf("signature mismatch:\n  got:  %s\n  want: %s", sawSig, want)
	}

	// Body must round-trip.
	var got Envelope
	if err := json.Unmarshal(sawBody, &got); err != nil {
		t.Fatalf("body not valid JSON: %v", err)
	}
	if got.Cursor != 42 || len(got.Events) != 1 {
		t.Errorf("body shape wrong: %+v", got)
	}
	if sawUserAgent == "" {
		t.Error("User-Agent header missing")
	}
}

// Empty secret → deliverer OMITS the signature headers entirely
// (#356 semantics). Sink should NOT see either header.
func TestPostEnvelope_EmptySecretOmitsHeaders(t *testing.T) {
	var sawSig, sawTs string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawSig = r.Header.Get("X-Eurobase-Signature")
		sawTs = r.Header.Get("X-Eurobase-Timestamp")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := &Deliverer{client: srv.Client()}
	env := &Envelope{Events: []EventRow{{ID: "x"}}, Cursor: 1}
	res := d.PostEnvelope(context.Background(), srv.URL, nil, env)
	if res.Err != nil {
		t.Fatalf("delivery failed: %v", res.Err)
	}
	if sawSig != "" || sawTs != "" {
		t.Errorf("empty secret must omit headers; got sig=%q ts=%q", sawSig, sawTs)
	}
}

// Non-2xx → Err populated, Cursor NOT set (caller uses this to hold
// the DB cursor for retry).
func TestPostEnvelope_Non2xxHoldsCursor(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	d := &Deliverer{client: srv.Client()}
	env := &Envelope{Events: []EventRow{{ID: "x", Seq: 42}}, Cursor: 42}
	res := d.PostEnvelope(context.Background(), srv.URL, nil, env)
	if res.Err == nil {
		t.Fatal("expected error on 503")
	}
	if res.Cursor != 0 {
		t.Errorf("Cursor must be zero on failure (caller holds DB cursor), got %d", res.Cursor)
	}
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("StatusCode = %d, want 503", res.StatusCode)
	}
}

// SSRF-safe client: attempting to resolve a hostname to a private
// IP at dial time must fail even if registration didn't catch it
// (DNS rebinding). Simulate by dialing a URL whose IP is 127.0.0.1.
func TestSSRFSafeClient_RejectsInternalResolved(t *testing.T) {
	client := NewSSRFSafeClient(ClientConfig{Timeout: 2 * time.Second, DialTimeout: 1 * time.Second})
	// 127.0.0.1 as a literal — the SSRF check runs against the
	// resolved IP, so this hits the same code path DNS rebinding
	// would trigger.
	resp, err := client.Post("https://127.0.0.1:1/nowhere", "application/json", bytes.NewReader([]byte("{}")))
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected SSRF-safe client to refuse 127.0.0.1")
	}
	if !strings.Contains(err.Error(), "loopback") && !strings.Contains(err.Error(), "SSRF") {
		t.Errorf("error should mention loopback / SSRF: got %v", err)
	}
}

// SSRF-safe client refuses redirects — a 302 to http://10.0.0.1
// would sidestep the resolved-IP check because Go's redirect
// handling happens outside DialContext.
func TestSSRFSafeClient_RefusesRedirects(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://example.com/other")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	// httptest server binds to 127.0.0.1 which the SSRF client
	// refuses at dial time — that's the whole point elsewhere, but
	// for this specific test we need to reach the server first. Use
	// the client's Transport but the server's TLS trust to test the
	// CheckRedirect behaviour specifically.
	inner := NewSSRFSafeClient(ClientConfig{})
	inner.Transport = srv.Client().Transport
	resp, err := inner.Get(srv.URL)
	if err == nil {
		_ = resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "redirects disallowed") {
		t.Errorf("expected redirects-disallowed error, got %v", err)
	}
}
