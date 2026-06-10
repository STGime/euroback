package gateway

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/tenant"
)

// NewCORSMiddleware builds a CORS middleware that only reflects the Origin
// header for allowlisted origins. Requests from other origins get no CORS
// response headers — browsers block them by default.
//
// Two allowlist layers:
//
//  1. **Global** — passed in here at construction time. Each entry is
//     either an exact origin ("https://console.eurobase.app") or a
//     wildcard ("https://*.eurobase.app") where `*` matches a single
//     hostname label. This covers platform origins.
//
//  2. **Per-project** — read at request time from
//     `auth.ProjectFromContext(r.Context()).AuthConfig.cors_origins`.
//     Tenant-controlled, set via PATCH /v1/tenants/{id}. Closes the
//     "browser app on a tenant's own domain can't talk to its own
//     project" gap reported by beta testers — the global allowlist
//     is for the platform, this layer is for each tenant's apps.
//
// To make per-project work, this middleware must run AFTER the
// subdomain middleware (so the ProjectContext is set when we look it
// up). For non-subdomain requests (apex, console paths), no project
// context is available at preflight time — those fall back to the
// global allowlist. Beta testers hitting `{slug}.eurobase.app` get
// per-project CORS automatically.
func NewCORSMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	check := BuildOriginChecker(allowedOrigins)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			allowed := check(r)

			if !allowed {
				// Not allowed by global or per-project. For preflight
				// respond 204 with no CORS headers so the browser
				// rejects the real request.
				//
				// One greppable line per rejection (issue #198):
				// without it the only symptom is a missing response
				// header, invisible outside browser devtools.
				projectID := ""
				if pc, ok := auth.ProjectFromContext(r.Context()); ok && pc != nil {
					projectID = pc.ProjectID
				}
				slog.Warn("CORS: origin not allowlisted, browser will block the response",
					"origin", origin, "project_id", projectID, "path", r.URL.Path)
				if r.Method == http.MethodOptions {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Vary", "Origin")

			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", strings.Join([]string{
					"Authorization",
					"Content-Type",
					"X-Project-Id",
					"X-Project-Slug",
					"apikey",
				}, ", "))
				w.Header().Set("Access-Control-Max-Age", "86400")
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// BuildOriginChecker compiles the global allowlist patterns once and
// returns a closure that decides per-request whether the request's
// Origin header is allowed. The closure layers the same two checks as
// NewCORSMiddleware (global allowlist, then per-project cors_origins
// from ProjectContext) so anything that needs an origin check —
// including the WebSocket upgrader — gets identical behaviour.
//
// Empty Origin returns false. Callers that want to permit
// non-CORS-shaped requests (e.g. server-to-server) should branch on
// that themselves rather than rely on this helper.
func BuildOriginChecker(allowedOrigins []string) func(*http.Request) bool {
	patterns := make([]originPattern, 0, len(allowedOrigins))
	for _, o := range allowedOrigins {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		patterns = append(patterns, compileOriginPattern(o))
	}
	return func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return false
		}
		if originAllowed(origin, patterns) {
			return true
		}
		if pc, ok := auth.ProjectFromContext(r.Context()); ok && pc != nil && len(pc.AuthConfig) > 0 {
			cfg := tenant.ParseAuthConfig(pc.AuthConfig)
			if cfg.IsCORSOriginAllowed(origin) {
				return true
			}
		}
		return false
	}
}

type originPattern struct {
	scheme  string
	host    string // either exact host or "*.suffix"
	isWild  bool
	suffix  string // set when isWild; leading dot included ("" for top-level wild is rejected)
}

func compileOriginPattern(raw string) originPattern {
	p := originPattern{}
	// Split scheme.
	if i := strings.Index(raw, "://"); i > 0 {
		p.scheme = raw[:i]
		p.host = raw[i+3:]
	} else {
		p.host = raw
	}
	if strings.HasPrefix(p.host, "*.") {
		p.isWild = true
		p.suffix = p.host[1:] // ".eurobase.app"
	}
	return p
}

func originAllowed(origin string, patterns []originPattern) bool {
	// Parse origin into scheme+host.
	var scheme, host string
	if i := strings.Index(origin, "://"); i > 0 {
		scheme = origin[:i]
		host = origin[i+3:]
	} else {
		return false
	}

	for _, p := range patterns {
		if p.scheme != "" && p.scheme != scheme {
			continue
		}
		if p.isWild {
			// Require one label before suffix, e.g. "foo.eurobase.app".
			if strings.HasSuffix(host, p.suffix) && len(host) > len(p.suffix) {
				label := host[:len(host)-len(p.suffix)]
				if label != "" && !strings.Contains(label, ".") {
					return true
				}
			}
			continue
		}
		if p.host == host {
			return true
		}
	}
	return false
}
