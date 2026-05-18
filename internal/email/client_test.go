package email

import (
	"context"
	"encoding/json"
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

// TestSendBulk_ChunksAndContinuesOnError wires a fake TEM server that
// fails one specific chunk and succeeds the rest, asserts the
// per-chunk failure didn't abort the loop, and confirms the
// BulkResult tallies recipient counts (not chunk counts).
func TestSendBulk_ChunksAndContinuesOnError(t *testing.T) {
	// 25 recipients with chunk size 9 → chunks of 9, 9, 7.
	// We'll fail chunk #2 (the second 9) and let chunks #1 and #3 succeed.
	addrs := makeAddrs(25)
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
			// Simulate TEM 403 quota error on chunk #2.
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
		t.Fatalf("unexpected error from SendBulk (some chunks should have succeeded): %v", err)
	}

	wantSent := 9 + 7   // chunk 1 + chunk 3
	wantFailed := 9     // chunk 2
	if res.Sent != wantSent {
		t.Errorf("Sent: got %d, want %d", res.Sent, wantSent)
	}
	if res.Failed != wantFailed {
		t.Errorf("Failed: got %d, want %d", res.Failed, wantFailed)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("Errors: got %d entries, want 1", len(res.Errors))
	}
	if len(res.Errors[0].Recipients) != 9 {
		t.Errorf("Errors[0].Recipients: got %d, want 9", len(res.Errors[0].Recipients))
	}
	if hits != 3 {
		t.Errorf("expected 3 TEM POSTs, got %d (regression: SendBulk aborted on chunk failure)", hits)
	}
	if len(seen) != 3 {
		t.Fatalf("expected 3 captured payloads, got %d", len(seen))
	}
	// Order property: chunk N contains recipients[(N-1)*9 : N*9].
	for i, chunk := range seen {
		startIdx := i * 9
		endIdx := startIdx + 9
		if endIdx > len(addrs) {
			endIdx = len(addrs)
		}
		wantChunk := addrs[startIdx:endIdx]
		if !reflect.DeepEqual(chunk, wantChunk) {
			t.Errorf("chunk %d: got %v, want %v", i, chunk, wantChunk)
		}
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
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = string(rune('a'+i)) + "@test.local"
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
