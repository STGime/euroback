package email

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

// Closes #35. Verifies the chunker splits at the right boundaries,
// the env override picks up, and SendBulk delivers per-chunk
// continue-on-error semantics.

func TestChunkRecipients(t *testing.T) {
	cases := []struct {
		name      string
		in        []string
		size      int
		wantLens  []int // length of each chunk (we don't pin contents — order test below covers that)
	}{
		{"empty input", nil, 9, nil},
		{"empty input, size 0 falls back to default", nil, 0, nil},
		{"single under cap", []string{"a"}, 9, []int{1}},
		{"exactly cap", makeAddrs(9), 9, []int{9}},
		{"one over cap", makeAddrs(10), 9, []int{9, 1}},
		{"two over cap", makeAddrs(11), 9, []int{9, 2}},
		{"twenty, cap 9", makeAddrs(20), 9, []int{9, 9, 2}},
		{"five, cap 2", makeAddrs(5), 2, []int{2, 2, 1}},
		// cap 0 should NOT mean "everyone in one chunk" — that's exactly the
		// pre-#35 bug. The chunker falls back to the default so an env
		// misconfig can't silently disable batching.
		{"size 0 falls back to default", makeAddrs(15), 0, []int{9, 6}},
		{"negative size also falls back", makeAddrs(15), -1, []int{9, 6}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := chunkRecipients(tc.in, tc.size)
			if len(got) != len(tc.wantLens) {
				t.Fatalf("chunk count: got %d, want %d (chunks=%v)", len(got), len(tc.wantLens), got)
			}
			for i, c := range got {
				if len(c) != tc.wantLens[i] {
					t.Errorf("chunk[%d] size: got %d, want %d", i, len(c), tc.wantLens[i])
				}
			}
		})
	}
}

// TestChunkRecipients_PreservesOrder is its own test because the
// audit trail depends on chunk N containing the same addresses every
// time. If someone rewrites this in terms of a hash map iteration in
// future, the test catches it.
func TestChunkRecipients_PreservesOrder(t *testing.T) {
	in := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	got := chunkRecipients(in, 4)
	want := [][]string{
		{"a", "b", "c", "d"},
		{"e", "f", "g", "h"},
		{"i", "j"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMaxBCCPerMessage(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want int
	}{
		{"unset", "", defaultMaxBCC},
		{"explicit 20", "20", 20},
		{"non-numeric falls back", "abc", defaultMaxBCC},
		{"zero falls back", "0", defaultMaxBCC},
		{"negative falls back", "-3", defaultMaxBCC},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TEM_MAX_RECIPIENTS_PER_MESSAGE", tc.env)
			if got := maxBCCPerMessage(); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

// TestSendBulk_TransientChunkErrorRecoversViaSplitRetry pins the
// split-and-retry recovery path introduced 2026-08-18. Scenario: the
// FIRST attempt at chunk #2 fails (e.g. a transient TEM 5xx or a
// rate-limit blip that clears immediately), but subsequent smaller
// requests succeed. Pre-fix this class of failure lost all 9
// recipients in the chunk. Post-fix the split-retry re-runs the
// two halves, both succeed, all 25 recipients are delivered.
//
// If a future refactor reverts to fail-fast-per-chunk behavior, this
// test fails loudly. The retry semantics are load-bearing for the
// "1 bad probe address doesn't kill 9 real customers" invariant.
func TestSendBulk_TransientChunkErrorRecoversViaSplitRetry(t *testing.T) {
	addrs := makeAddrs(25) // chunks of 9, 9, 7 (at chunk size 9)
	var seen [][]string
	var hits int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body temRequest
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var rcpts []string
		for _, a := range body.Bcc {
			rcpts = append(rcpts, a.Email)
		}
		seen = append(seen, rcpts)
		n := atomic.AddInt32(&hits, 1)
		if n == 2 {
			// Fail the FIRST attempt at chunk #2 only. Subsequent
			// requests (the split halves + chunk #3) succeed.
			http.Error(w, `{"details":[{"resource":"TemEmailsMaxRecipients"}]}`, http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"emails":[]}`))
	}))
	defer srv.Close()

	c := newClientPointingAt(srv.URL)
	res, err := c.SendBulk(context.Background(), addrs, "subj", "<p>hi</p>")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// All 25 should now land — chunk #2's transient failure is
	// recovered by the split-retry.
	if res.Sent != 25 {
		t.Errorf("Sent: got %d, want 25 (split-retry should recover the transient chunk failure)", res.Sent)
	}
	if res.Failed != 0 {
		t.Errorf("Failed: got %d, want 0", res.Failed)
	}
	if len(res.Errors) != 0 {
		t.Errorf("Errors: got %d entries, want 0", len(res.Errors))
	}
	// API call count: 1 (chunk 1 success) + 1 (chunk 2 first-attempt
	// fail) + 2 (chunk 2 split halves succeed) + 1 (chunk 3 success)
	// = 5 hits.
	if hits != 5 {
		t.Errorf("expected 5 TEM POSTs (1+1+2+1), got %d", hits)
	}
}

// TestSendBulk_BadAddressIsolatedByRetry — the motivating scenario
// from the 2026-08-18 support incident. One specific recipient is
// format-invalid (TEM rejects the whole chunk it's in). Pre-fix all
// 9 recipients in the batch were reported as failed. Post-fix the
// split-retry isolates the bad one; the other 8 succeed.
//
// Fake TEM: fails any request whose BCC list contains
// "bad@example.invalid". Succeeds all others.
func TestSendBulk_BadAddressIsolatedByRetry(t *testing.T) {
	// 9 recipients: 8 valid + 1 bad in the middle.
	addrs := []string{
		"u0@example.com", "u1@example.com", "u2@example.com",
		"u3@example.com", "bad@example.invalid", "u5@example.com",
		"u6@example.com", "u7@example.com", "u8@example.com",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body temRequest
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, a := range body.Bcc {
			if a.Email == "bad@example.invalid" {
				http.Error(w, `{"type":"invalid_arguments","message":"Invalid email recipient address: \"\""}`, http.StatusBadRequest)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"emails":[]}`))
	}))
	defer srv.Close()

	c := newClientPointingAt(srv.URL)
	res, err := c.SendBulk(context.Background(), addrs, "subj", "<p>hi</p>")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Sent != 8 {
		t.Errorf("Sent: got %d, want 8 (only the bad address should fail)", res.Sent)
	}
	if res.Failed != 1 {
		t.Errorf("Failed: got %d, want 1", res.Failed)
	}
	if len(res.Errors) != 1 || len(res.Errors[0].Recipients) != 1 || res.Errors[0].Recipients[0] != "bad@example.invalid" {
		t.Errorf("Errors: got %+v, want single-entry Recipients=[bad@example.invalid]", res.Errors)
	}
}

// TestSendBulk_AllChunksFail returns an error so the handler can 502.
// We're not OK with silently logging "0 sent" — that would let a
// totally-broken TEM look like a partial success to the operator.
func TestSendBulk_AllChunksFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `down`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newClientPointingAt(srv.URL)
	res, err := c.SendBulk(context.Background(), makeAddrs(11), "subj", "<p>x</p>")
	if err == nil {
		t.Fatal("expected error when every chunk fails, got nil")
	}
	if res.Sent != 0 {
		t.Errorf("Sent: got %d, want 0", res.Sent)
	}
	if res.Failed != 11 {
		t.Errorf("Failed: got %d, want 11", res.Failed)
	}
}

func TestSendBulk_SingleChunkUnderCap(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newClientPointingAt(srv.URL)
	res, err := c.SendBulk(context.Background(), makeAddrs(5), "s", "b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Sent != 5 || res.Failed != 0 || len(res.Errors) != 0 {
		t.Errorf("partial-result shape: %+v", res)
	}
	if hits != 1 {
		t.Errorf("expected 1 TEM POST, got %d", hits)
	}
}

func TestSendBulk_Unconfigured(t *testing.T) {
	// No authToken → Configured() == false. We don't want to silently
	// no-op an admin broadcast; bubble the error so the caller knows
	// nothing went out.
	c := NewEmailClient("", "fr-par", "p", "from@example.com", "Eurobase")
	_, err := c.SendBulk(context.Background(), []string{"a@example.com"}, "s", "b")
	if err == nil {
		t.Fatal("expected error from unconfigured client, got nil")
	}
}

func TestSendBulk_EmptyRecipients(t *testing.T) {
	c := newClientPointingAt("http://unused")
	_, err := c.SendBulk(context.Background(), nil, "s", "b")
	if err == nil {
		t.Fatal("expected error for empty recipients, got nil")
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func makeAddrs(n int) []string {
	// Use a printf-formed local-part so the addresses stay valid no
	// matter how large n grows. Earlier version used
	// string(rune('a'+i)) which produced non-letter runes ({, |, etc.)
	// at i>=26.
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = fmt.Sprintf("user%d@test.local", i)
	}
	return out
}

// newClientPointingAt returns an EmailClient whose HTTP requests land
// at the given test server. We override the URL by hijacking the
// region field (the path the client builds includes the region in the
// URL prefix — see SendBulk). For tests we patch httpClient.Transport
// to a redirect.
func newClientPointingAt(base string) *EmailClient {
	c := NewEmailClient("token", "fr-par", "proj-x", "from@example.com", "Eurobase")
	c.httpClient = &http.Client{
		Transport: redirectingTransport{base: base},
	}
	return c
}

// redirectingTransport rewrites the request URL so client_test's
// httptest server gets the request rather than api.scaleway.com.
// Tests assert on body/headers, not on the URL host.
type redirectingTransport struct{ base string }

func (rt redirectingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	u, _ := http.NewRequest("POST", rt.base+"/tem", nil)
	r2.URL = u.URL
	r2.Host = u.URL.Host
	return http.DefaultTransport.RoundTrip(r2)
}
