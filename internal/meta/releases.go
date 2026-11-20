// Package meta serves public facts about the platform's client tools — today
// the latest released versions of the JS SDK and the CLI, so the console,
// the marketing site and the CLI itself can tell people to upgrade.
//
// The versions are read from where releases are published (the npm
// registry for @eurobase/sdk, the public homebrew-tap repository's GitHub
// releases for the CLI) by the gateway, cached, and served from
// GET /v1/meta/releases. Browsers only ever talk to the gateway; the
// lookups carry no visitor data: they read public package metadata, server
// to server, so npm and GitHub don't process anything on our behalf (not
// sub-processors). If one of them is unreachable, the last known versions
// are served.
package meta

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Release is one tool's latest published version.
type Release struct {
	Version     string `json:"version"`
	PublishedAt string `json:"published_at,omitempty"`
	URL         string `json:"url"`
	Install     string `json:"install"`
}

// Releases is the response of GET /v1/meta/releases. A tool whose version
// couldn't be read yet is null.
type Releases struct {
	SDK       *Release  `json:"sdk"`
	CLI       *Release  `json:"cli"`
	CheckedAt time.Time `json:"checked_at"`
}

const (
	sdkPackage = "@eurobase/sdk"
	// The CLI's binaries are published as releases of the public tap
	// repository (the source repository is private to the release flow).
	cliRepo = "STGime/homebrew-tap"

	defaultNPMURL    = "https://registry.npmjs.org/@eurobase/sdk/latest"
	defaultGitHubURL = "https://api.github.com/repos/STGime/homebrew-tap/releases/latest"

	cacheTTL   = 15 * time.Minute
	retryAfter = 2 * time.Minute // after a failed lookup, keep the old value this long
	maxBody    = 256 << 10
)

// Service reads and caches the latest versions.
type Service struct {
	client    *http.Client
	npmURL    string
	githubURL string
	now       func() time.Time

	mu         sync.Mutex
	cached     Releases
	nextCheck  time.Time
	refreshing chan struct{} // closed when the running refresh is done; nil when none runs
}

// NewService returns a Service reading the public npm registry and GitHub.
func NewService() *Service {
	return &Service{
		client:    &http.Client{Timeout: 5 * time.Second},
		npmURL:    defaultNPMURL,
		githubURL: defaultGitHubURL,
		now:       time.Now,
	}
}

// Latest returns the cached versions. When they're older than cacheTTL, a
// refresh starts in the background and the cached values are returned
// right away; only a caller that finds nothing cached yet (a fresh gateway)
// waits for the refresh, at most until its own ctx ends.
func (s *Service) Latest(ctx context.Context) Releases {
	s.mu.Lock()
	if s.now().Before(s.nextCheck) {
		c := s.cached
		s.mu.Unlock()
		return c
	}
	done := s.refreshing
	if done == nil {
		done = make(chan struct{})
		s.refreshing = done
		go s.refresh(done)
	}
	empty := s.cached.SDK == nil && s.cached.CLI == nil
	c := s.cached
	s.mu.Unlock()
	if !empty {
		return c
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cached
}

// refresh reads both sources in parallel, independent of any request
// (a caller that gives up doesn't cancel it). A failed lookup keeps the
// previous value for that tool and retries after retryAfter.
func (s *Service) refresh(done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var (
		wg             sync.WaitGroup
		sdk, cli       *Release
		sdkErr, cliErr error
	)
	wg.Add(2)
	go func() { defer wg.Done(); sdk, sdkErr = s.fetchSDK(ctx) }()
	go func() { defer wg.Done(); cli, cliErr = s.fetchCLI(ctx) }()
	wg.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if sdkErr == nil {
		s.cached.SDK = sdk
	} else {
		slog.Warn("meta: reading the latest SDK version failed", "error", sdkErr)
	}
	if cliErr == nil {
		s.cached.CLI = cli
	} else {
		slog.Warn("meta: reading the latest CLI version failed", "error", cliErr)
	}
	s.cached.CheckedAt = now.UTC()
	if sdkErr == nil && cliErr == nil {
		s.nextCheck = now.Add(cacheTTL)
	} else {
		s.nextCheck = now.Add(retryAfter)
	}
	s.refreshing = nil
	close(done)
}

func (s *Service) getJSON(ctx context.Context, url string, accept string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "eurobase-gateway")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(v)
}

func (s *Service) fetchSDK(ctx context.Context) (*Release, error) {
	var doc struct {
		Version string `json:"version"`
	}
	if err := s.getJSON(ctx, s.npmURL, "application/json", &doc); err != nil {
		return nil, err
	}
	if !validVersion(doc.Version) {
		return nil, fmt.Errorf("npm: unexpected version %q", doc.Version)
	}
	return &Release{
		Version: doc.Version,
		URL:     "https://www.npmjs.com/package/" + sdkPackage + "/v/" + doc.Version,
		Install: "npm install " + sdkPackage + "@latest",
	}, nil
}

func (s *Service) fetchCLI(ctx context.Context) (*Release, error) {
	var doc struct {
		TagName     string `json:"tag_name"`
		PublishedAt string `json:"published_at"`
		HTMLURL     string `json:"html_url"`
	}
	if err := s.getJSON(ctx, s.githubURL, "application/vnd.github+json", &doc); err != nil {
		return nil, err
	}
	// CLI releases are tagged cli-vX.Y.Z; the docs build download URLs
	// from that pattern, so nothing else is accepted.
	v, ok := strings.CutPrefix(doc.TagName, "cli-v")
	if !ok || !validVersion(v) {
		return nil, fmt.Errorf("github: unexpected tag %q", doc.TagName)
	}
	published := ""
	if t, err := time.Parse(time.RFC3339, doc.PublishedAt); err == nil {
		published = t.UTC().Format(time.RFC3339)
	}
	return &Release{
		Version:     v,
		PublishedAt: published,
		URL:         "https://github.com/" + cliRepo + "/releases/tag/" + doc.TagName,
		Install:     "brew upgrade stgime/tap/eurobase",
	}, nil
}

// validVersion accepts plain semantic versions (1.2.3, 1.2.3-rc.1) so
// nothing unexpected from an upstream response is echoed to clients.
func validVersion(v string) bool {
	if v == "" || len(v) > 32 {
		return false
	}
	core, pre, hasPre := strings.Cut(v, "-")
	if hasPre {
		for _, seg := range strings.Split(pre, ".") {
			if seg == "" || seg[0] == '-' {
				return false
			}
		}
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	for _, c := range v {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '.' || c == '-') {
			return false
		}
	}
	return true
}

// HandleReleases serves GET /v1/meta/releases: public, no API key, readable
// from any web page (the marketing site, the console, docs).
func HandleReleases(s *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		rel := s.Latest(ctx)
		h := w.Header()
		// Public data: any origin may read it. The CORS middleware sets an
		// origin-specific header for allowlisted origins; leave that as is.
		if h.Get("Access-Control-Allow-Origin") == "" {
			h.Set("Access-Control-Allow-Origin", "*")
		}
		h.Set("Content-Type", "application/json")
		h.Set("Cache-Control", "public, max-age=300")
		_ = json.NewEncoder(w).Encode(rel)
	}
}
