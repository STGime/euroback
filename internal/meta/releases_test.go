package meta

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func testService(t *testing.T, npm, gh http.HandlerFunc) (*Service, *time.Time) {
	t.Helper()
	npmSrv := httptest.NewServer(npm)
	ghSrv := httptest.NewServer(gh)
	t.Cleanup(npmSrv.Close)
	t.Cleanup(ghSrv.Close)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	s := NewService()
	s.npmURL, s.githubURL = npmSrv.URL, ghSrv.URL
	s.now = func() time.Time { return now }
	return s, &now
}

func TestLatestReadsAndCaches(t *testing.T) {
	var calls atomic.Int32
	s, now := testService(t,
		func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			_, _ = w.Write([]byte(`{"name":"@eurobase/sdk","version":"0.13.1"}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"tag_name":"cli-v0.7.1","published_at":"2026-11-20T11:10:00Z","html_url":"x"}`))
		})

	got := s.Latest(context.Background())
	if got.SDK == nil || got.SDK.Version != "0.13.1" || got.SDK.Install != "npm install @eurobase/sdk@latest" {
		t.Fatalf("sdk: %+v", got.SDK)
	}
	if got.CLI == nil || got.CLI.Version != "0.7.1" || got.CLI.PublishedAt == "" ||
		got.CLI.URL != "https://github.com/STGime/homebrew-tap/releases/tag/cli-v0.7.1" {
		t.Fatalf("cli: %+v", got.CLI)
	}

	*now = now.Add(5 * time.Minute)
	s.Latest(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("expected a cached answer within the TTL, npm called %d times", calls.Load())
	}
	*now = now.Add(cacheTTL)
	s.Latest(context.Background())
	if calls.Load() != 2 {
		t.Fatalf("expected a refresh after the TTL, npm called %d times", calls.Load())
	}
}

// A failed lookup keeps the last good value and retries sooner.
func TestLatestKeepsOldValueOnFailure(t *testing.T) {
	var fail atomic.Bool
	s, now := testService(t,
		func(w http.ResponseWriter, r *http.Request) {
			if fail.Load() {
				http.Error(w, "down", http.StatusBadGateway)
				return
			}
			_, _ = w.Write([]byte(`{"version":"0.13.1"}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"tag_name":"cli-v0.7.1"}`))
		})
	s.Latest(context.Background())
	fail.Store(true)
	*now = now.Add(cacheTTL)
	got := s.Latest(context.Background())
	if got.SDK == nil || got.SDK.Version != "0.13.1" {
		t.Fatalf("expected the old SDK version to be kept, got %+v", got.SDK)
	}
	if !s.nextCheck.Equal(now.Add(retryAfter)) {
		t.Fatalf("expected a retry after %v, next check %v", retryAfter, s.nextCheck)
	}
}

// Anything that isn't a plain version is never passed on.
func TestLatestRefusesOddVersions(t *testing.T) {
	s, _ := testService(t,
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"version":"<script>alert(1)</script>"}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"tag_name":"latest"}`))
		})
	got := s.Latest(context.Background())
	if got.SDK != nil || got.CLI != nil {
		t.Fatalf("expected no versions, got sdk=%+v cli=%+v", got.SDK, got.CLI)
	}
}

func TestValidVersion(t *testing.T) {
	for v, want := range map[string]bool{
		"0.13.1": true, "1.0.0-rc.1": true, "10.20.30": true,
		"": false, "1.2": false, "1.2.3.4": false, "v1.2.3": false, "1.2.x": false,
		"1.2.3 ": false, "1.2.3-<b>": false,
	} {
		if got := validVersion(v); got != want {
			t.Errorf("validVersion(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestHandleReleasesHeaders(t *testing.T) {
	s, _ := testService(t,
		func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"version":"0.13.1"}`)) },
		func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"tag_name":"cli-v0.7.1"}`)) })

	rec := httptest.NewRecorder()
	HandleReleases(s)(rec, httptest.NewRequest(http.MethodGet, "/v1/meta/releases", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("status %d, ACAO %q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
	var body Releases
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.SDK == nil || body.CLI == nil {
		t.Fatalf("body %s: %v", rec.Body.String(), err)
	}

	// An allowlisted origin keeps the CORS middleware's own header.
	rec = httptest.NewRecorder()
	rec.Header().Set("Access-Control-Allow-Origin", "https://console.eurobase.app")
	HandleReleases(s)(rec, httptest.NewRequest(http.MethodGet, "/v1/meta/releases", nil))
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://console.eurobase.app" {
		t.Fatalf("ACAO overwritten: %q", got)
	}
}
