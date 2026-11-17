package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdminSuspendProject suspends a project (superadmin only): status
// 'active' → 'suspended'. Everything that serves a project requires
// 'active' (API keys, the project subdomain, the console's project
// routes, cron, function triggers), so the project stops answering. Its
// data, users and files are kept; AdminUnsuspendProject reverses it.
func AdminSuspendProject(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		body.Reason = strings.TrimSpace(body.Reason)
		if rr := []rune(body.Reason); len(rr) > 500 {
			body.Reason = string(rr[:500])
		}
		setProjectStatus(w, r, pool, "active", "suspended", audit.ActionProjectSuspended, body.Reason)
	}
}

// AdminUnsuspendProject reactivates a suspended project (superadmin only).
func AdminUnsuspendProject(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setProjectStatus(w, r, pool, "suspended", "active", audit.ActionProjectUnsuspended, "")
	}
}

// ErrProjectSuspendedJSON is the body for any request to a suspended
// project's console / API routes.
const ErrProjectSuspendedJSON = `{"error":"this project is suspended; contact support","code":"project_suspended"}`

// ProjectSuspended reports whether the project is suspended. A failed
// lookup reports false: the route's own checks still apply.
func ProjectSuspended(ctx context.Context, pool *pgxpool.Pool, projectID string) bool {
	var suspended bool
	if err := pool.QueryRow(ctx,
		`SELECT status = 'suspended' FROM public.projects WHERE id::text = $1`, projectID).Scan(&suspended); err != nil {
		return false
	}
	return suspended
}

// RefuseSuspendedProject answers 403 project_suspended for a route whose
// {id} is a suspended project.
func RefuseSuspendedProject(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ProjectSuspended(r.Context(), pool, chi.URLParam(r, "id")) {
				http.Error(w, ErrProjectSuspendedJSON, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// setProjectStatus moves a project from one status to the other; any
// other current status (provisioning, deleting, …) is refused with 409.
func setProjectStatus(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, from, to, action, reason string) {
	projectID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, err := uuid.Parse(projectID); err != nil {
		http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
		return
	}
	var slug string
	err := pool.QueryRow(r.Context(),
		`UPDATE public.projects SET status = $3
		  WHERE id = $1::uuid AND status = $2
		 RETURNING slug`, projectID, from, to).Scan(&slug)
	if errors.Is(err, pgx.ErrNoRows) {
		var current string
		if err := pool.QueryRow(r.Context(),
			`SELECT status FROM public.projects WHERE id = $1::uuid`, projectID).Scan(&current); err != nil {
			http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
			return
		}
		body, _ := json.Marshal(map[string]string{
			"error": "project is " + current + ", not " + from, "code": "wrong_status", "status": current,
		})
		http.Error(w, string(body), http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, `{"error":"update failed"}`, http.StatusInternalServerError)
		return
	}
	if svc := audit.FromContext(r.Context()); svc != nil {
		actorID, actorEmail := audit.ActorFromContext(r.Context())
		meta := map[string]any{"slug": slug}
		if reason != "" {
			meta["reason"] = reason
		}
		svc.Log(r.Context(), projectID, actorID, actorEmail, action,
			audit.WithTarget("project", projectID),
			audit.WithMetadata(meta),
			audit.WithIP(r.RemoteAddr))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"id": projectID, "status": to})
}
