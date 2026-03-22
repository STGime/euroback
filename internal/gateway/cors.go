package gateway

import (
	"net/http"
	"strings"
)

// CORSMiddleware handles CORS preflight requests and sets the appropriate
// headers on all responses. For the MVP, it allows all origins. In production,
// this should be restricted to customer-configured allowed origins stored in
// the project's settings JSONB field.
func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Vary", "Origin")

		// Preflight request.
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
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
