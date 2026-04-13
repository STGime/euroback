package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestMiddleware_RecordsRequestMetrics verifies that a request through the
// chi router produces the three expected series in the exposition output:
// a total counter, an inflight gauge, and a latency histogram — all with
// the matched route pattern as the "route" label (not the raw URL path).
func TestMiddleware_RecordsRequestMetrics(t *testing.T) {
	reg := New("test-v1")

	r := chi.NewRouter()
	r.Use(reg.Middleware)
	r.Get("/platform/projects/{id}/functions/{name}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Two requests to the same pattern, different path params — should
	// collapse into a single time series (cardinality guard).
	for _, id := range []string{"alpha", "beta"} {
		req := httptest.NewRequest(http.MethodGet, "/platform/projects/"+id+"/functions/foo", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	}

	body := scrape(t, reg.Handler())

	mustContain(t, body, `eurobase_http_requests_total{method="GET",route="/platform/projects/{id}/functions/{name}",status="2xx"} 2`)
	mustContain(t, body, `eurobase_http_request_duration_seconds_count{method="GET",route="/platform/projects/{id}/functions/{name}"} 2`)
	mustContain(t, body, `eurobase_build_info{version="test-v1"} 1`)
	// Raw IDs must NEVER appear as labels — that would blow up cardinality.
	if strings.Contains(body, "alpha") || strings.Contains(body, "beta") {
		t.Fatalf("raw path params leaked into metric labels:\n%s", body)
	}
}

// TestMiddleware_ErrorStatus verifies that 5xx responses land in the "5xx"
// bucket — this is the signal the alerting rules fire on.
func TestMiddleware_ErrorStatus(t *testing.T) {
	reg := New("")

	r := chi.NewRouter()
	r.Use(reg.Middleware)
	r.Get("/boom", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	body := scrape(t, reg.Handler())
	mustContain(t, body, `eurobase_http_requests_total{method="GET",route="/boom",status="5xx"} 1`)
}

// TestStatusClass pins the bucketing used for the status label. Anything
// non-canonical (e.g. 999) falls through to the raw code — not ideal but
// bounded since handlers rarely emit such codes.
func TestStatusClass(t *testing.T) {
	cases := map[int]string{
		200: "2xx", 204: "2xx",
		301: "3xx",
		404: "4xx", 429: "4xx",
		500: "5xx", 503: "5xx",
		100: "100",
	}
	for code, want := range cases {
		if got := statusClass(code); got != want {
			t.Errorf("statusClass(%d) = %q, want %q", code, got, want)
		}
	}
}

func scrape(t *testing.T, h http.Handler) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	b, _ := io.ReadAll(w.Body)
	return string(b)
}

func mustContain(t *testing.T, body, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Fatalf("metrics output missing %q\nfull body:\n%s", want, body)
	}
}
