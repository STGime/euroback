package gateway

import (
	"net/http"

	"github.com/eurobase/euroback/internal/auth"
)

// MaintenanceModeMiddleware returns HTTP 503 for SDK requests
// targeting a project whose `projects.maintenance_mode` column is
// true. Used during Free/Pro → Team tier upgrade cutover (~30 seconds
// window) to freeze writes on the old cluster while the router flips
// to the dedicated instance, and by ops for emergency freezes.
//
// Runs AFTER the API-key or subdomain middleware that resolves the
// ProjectContext — the maintenance flag is populated by the same
// SELECT that resolves the project, so there is no extra DB hit per
// request. If ProjectContext is missing (unauthenticated route),
// this middleware no-ops.
//
// Returns a stable JSON body:
//
//	{"error":"maintenance","message":"Project is being upgraded. Retry in ~30 seconds."}
//
// with `Retry-After: 60` (conservative — real cutover is ~30 s but a
// stale cache in front of the gateway may extend the window).
//
// SDK clients that follow the standard Retry-After hint back off
// automatically; hand-rolled clients see a clean 503 and can retry.
func MaintenanceModeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok || pc == nil {
			next.ServeHTTP(w, r)
			return
		}
		if !pc.MaintenanceMode {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"maintenance","message":"Project is being upgraded. Retry in ~30 seconds."}`))
	})
}
