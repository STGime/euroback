package gateway

import "net/http"

// SecurityHeadersMiddleware sets a conservative set of response headers that
// block common browser-side misuse of the API. These are global — every
// response gets them, including error and preflight responses.
//
// HSTS is only meaningful over HTTPS; when the gateway is fronted by a TLS
// terminator (expected in production), the header tells the browser to force
// HTTPS on subsequent visits. In local HTTP dev the header is harmless.
func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}
