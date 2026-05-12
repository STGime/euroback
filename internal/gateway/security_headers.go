package gateway

import "net/http"

// apiCSP is the Content-Security-Policy header sent on every gateway
// response. The gateway only emits JSON / WebSocket upgrades / 30x
// redirects, so a deny-by-default policy is safe — no inline scripts,
// no external resources, no iframes. Closes #54 for the API tier.
//
// Why the explicit `'none'` everywhere:
//   - default-src 'none' blocks scripts/styles/images/fonts/connect/etc.
//     If a future bug causes the gateway to serve attacker-controlled
//     HTML, browsers refuse to load anything from it.
//   - frame-ancestors 'none' is the modern equivalent of
//     X-Frame-Options: DENY; both are sent because not every browser
//     respects CSP's clickjacking control.
//   - base-uri 'none' blocks <base href="..."> injection that would
//     reroute relative URLs.
//   - form-action 'none' blocks <form action="..."> POST exfiltration.
//
// The Svelte console is a separate origin (console.eurobase.app) and
// needs its own, much looser CSP — that's a separate PR; CSP from the
// console's nginx is the right venue, not this middleware.
const apiCSP = "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

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
		h.Set("Content-Security-Policy", apiCSP)
		next.ServeHTTP(w, r)
	})
}
