// Package meta serves public facts about the platform's client tools — today
// the latest released versions of the JS SDK and the CLI, so the console,
// the marketing site and the CLI itself can tell people to upgrade.
//
// The versions are read from where releases are published (the npm
// registry for @eurobase/sdk, the public homebrew-tap repository's GitHub
// releases for the CLI) by the gateway, cached, and served from
// GET /v1/meta/releases. Browsers only ever talk to the gateway; the
// lookups carry no visitor data.
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

	mu        sync.Mutex
	cached    Releases
	nextCheck time.Time
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

// Latest returns the cached versions, refreshing them when they're older
// than cacheTTL. A failed lookup keeps the previous value for that tool.
func (s *Service) Latest(ctx context.Context) Releases {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if now.Before(s.nextCheck) {
		return s.cached
	}

	ok := true
	if r, err := s.fetchSDK(ctx); err == nil {
		s.cached.SDK = r
	} else {
		ok = false
		slog.Warn("meta: reading the latest SDK version failed", "error", err)
	}
	if r, err := s.fetchCLI(ctx); err == nil {
		s.cached.CLI = r
	} else {
		ok = false
		slog.Warn("meta: reading the latest CLI version failed", "error", err)
	}
	s.cached.CheckedAt = now.UTC()
	if ok {
		s.nextCheck = now.Add(cacheTTL)
	} else {
		s.nextCheck = now.Add(retryAfter)
	}
	return s.cached
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
	v := strings.TrimPrefix(strings.TrimPrefix(doc.TagName, "cli-"), "v")
	if !validVersion(v) {
		return nil, fmt.Errorf("github: unexpected tag %q", doc.TagName)
	}
	return &Release{
		Version:     v,
		PublishedAt: doc.PublishedAt,
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
	core, _, _ := strings.Cut(v, "-")
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
