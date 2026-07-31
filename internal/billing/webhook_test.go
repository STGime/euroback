package billing

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/billing/mollie"
)

// The webhook state-machine's DB writes are exercised in the staging
// manual-QA loop (see docs/billing/webhook-state-machine.md) rather
// than here — the codebase's existing test convention is pool-less
// unit tests (see internal/plans/*_test.go), and CI test-go runs
// plain `go test` without a Postgres service. What we CAN cover
// deterministically without a DB:
//
//   - HTTP-level parsing (form body, missing id, disabled env)
//   - Resource-type dispatch (tr_ vs sub_ vs unknown)
//   - Idempotency shape (foreign IDs → 200 no-op)
//   - Small helpers (parseMollieDate)

func TestHandleMollieWebhook_DisabledReturns200(t *testing.T) {
	// Even when disabled, respond 200 so Mollie doesn't retry
	// into a queue backup. See handler docstring.
	c, _ := mustNewMollieClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Mollie must not be called when billing disabled")
	})
	svc := NewService(nil, c, Config{}, false)

	req := httptest.NewRequest(http.MethodPost, "/platform/billing/webhook",
		strings.NewReader("id=tr_abc"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	HandleMollieWebhook(svc).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("want 200 (disabled short-circuit), got %d", w.Code)
	}
}

func TestHandleMollieWebhook_MissingIDReturns200(t *testing.T) {
	// Malformed body — still 200 to avoid a Mollie retry storm on
	// what is almost certainly a probe or misroute.
	c, _ := mustNewMollieClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Mollie must not be called without a resource ID")
	})
	svc := NewService(nil, c, Config{}, true)

	req := httptest.NewRequest(http.MethodPost, "/platform/billing/webhook",
		strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	HandleMollieWebhook(svc).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("want 200 on missing id, got %d", w.Code)
	}
}

func TestProcessMollieWebhook_UnknownPrefixIsNoop(t *testing.T) {
	// Defence against a new Mollie resource type showing up.
	// Neither the DB nor Mollie is called — we just log + no-op.
	c, _ := mustNewMollieClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unknown prefix must not trigger a Mollie GET")
	})
	svc := NewService(nil, c, Config{}, true)

	// Should not error, should not panic on a nil pool.
	if err := svc.ProcessMollieWebhook(context.Background(), "future_xyz"); err != nil {
		t.Errorf("unknown prefix: want nil, got %v", err)
	}
}

func TestProcessMollieWebhook_ForeignPaymentIDIsNoop(t *testing.T) {
	// A tr_ prefix from a non-Eurobase actor hits Mollie's API,
	// which 404s → ErrNotFound → we treat as "not our resource"
	// and return nil without touching the DB.
	c, _ := mustNewMollieClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/payments/") {
			t.Errorf("wanted /payments/ path, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status":404,"title":"Not Found"}`))
	})
	svc := NewService(nil, c, Config{}, true)

	if err := svc.ProcessMollieWebhook(context.Background(), "tr_foreign"); err != nil {
		t.Errorf("foreign payment ID: want nil, got %v", err)
	}
}

func TestProcessMollieWebhook_MollieServerErrorPropagates(t *testing.T) {
	// A 500 from Mollie should surface as an error so the
	// handler-level slog.Error path fires (which the log-based
	// Grafana alert reads). Handler still returns 200 to Mollie.
	c, _ := mustNewMollieClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	svc := NewService(nil, c, Config{}, true)

	err := svc.ProcessMollieWebhook(context.Background(), "tr_abc")
	if err == nil {
		t.Fatal("wanted error on 500, got nil")
	}
	if !errors.Is(err, mollie.ErrServer) {
		t.Errorf("wanted ErrServer in chain, got %v", err)
	}
}

// ── parseMollieDate ───────────────────────────────────────────────

func TestParseMollieDate(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantNil bool
		want    string // ISO date if non-nil
	}{
		{"empty is nil (defensive)", "", true, ""},
		{"valid YYYY-MM-DD", "2026-08-15", false, "2026-08-15"},
		{"garbage is nil", "not a date", true, ""},
		{"partial ISO is nil", "2026-08", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseMollieDate(tt.in)
			if tt.wantNil {
				if got != nil {
					t.Errorf("want nil, got %v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("want non-nil time, got nil")
			}
			if got.Format("2006-01-02") != tt.want {
				t.Errorf("want %s, got %s", tt.want, got.Format("2006-01-02"))
			}
			// Round-trip: same day should parse back identical
			// to the incoming string. Ensures TZ handling doesn't
			// silently shift the day.
			if got.Location() != time.UTC {
				// Mollie sends dates without TZ; time.Parse
				// returns UTC. Guard against a future refactor.
				t.Errorf("want UTC, got %s", got.Location())
			}
		})
	}
}
