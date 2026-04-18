package gateway

import (
	"net/http"
	"strings"
)

// NewCORSMiddleware builds a CORS middleware that only reflects the Origin
// header for allowlisted origins. Requests from other origins get no CORS
// response headers — browsers block them by default.
//
// Each allowed entry is either an exact origin ("https://console.eurobase.app")
// or a wildcard ("https://*.eurobase.app") where the `*` matches a single
// hostname label. Scheme must match exactly; `*` is scheme-scoped.
//
// With an empty allowlist, no cross-origin request is permitted.
func NewCORSMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	patterns := make([]originPattern, 0, len(allowedOrigins))
	for _, o := range allowedOrigins {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		patterns = append(patterns, compileOriginPattern(o))
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" || !originAllowed(origin, patterns) {
				// Not a cross-origin request, or origin is not allowed.
				// For preflight from a disallowed origin, respond 204 with no
				// CORS headers so the browser rejects the real request.
				if r.Method == http.MethodOptions && origin != "" {
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
