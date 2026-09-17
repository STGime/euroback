// Package tenant provides HTTP handlers for project/tenant management.
package tenant

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/plans"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CreateProjectRequest is the JSON body for creating a new project (tenant).
//
// OrgID / OrgIDExplicit encode a three-state input:
//
//   - Field omitted from the request body → OrgIDExplicit=false: fall
//     back to auto-attach (attach to the caller's admin'd org if
//     they have one, else personal). Matches pre-picker behaviour.
//   - Field present as `"org_id": null` → OrgIDExplicit=true, OrgID=nil:
//     force personal, skip auto-attach. Lets an org admin create a
//     side project without it landing in the org.
//   - Field present as `"org_id": "<uuid>"` → OrgIDExplicit=true,
//     OrgID=&"<uuid>": attach to that specific org after verifying the
//     caller admins it.
//
// OrgIDExplicit is set by the handler after peeking the raw JSON body
// (json.Decoder can't distinguish absent from null via `*string`) and
// is deliberately not serialised in the JSON tag.
type CreateProjectRequest struct {
	Name          string  `json:"name"`
	Slug          string  `json:"slug,omitempty"`   // optional; derived from name if empty
	Region        string  `json:"region,omitempty"` // defaults to "fr-par"
	Plan          string  `json:"plan,omitempty"`   // defaults to "free"
	OrgID         *string `json:"org_id,omitempty"`
	OrgIDExplicit bool    `json:"-"`
}

// smsGateBlocks reports whether an auth-config save should be rejected
// with 402 for the SMS paid-plan gate (#329). Returns true ONLY on an
// off→on transition for a Free (non-paid) plan — never on
// keep-as-is or flip-off, so a churned Pro project with stale
// phone.enabled=true can still save unrelated auth-config changes.
//
// Extracted as a pure function so the table test in handler_test.go
// pins the transition matrix without a live DB.
func smsGateBlocks(plan string, currentPhoneEnabled, postedPhoneEnabled bool) bool {
	if plans.IsPaidPlan(plan) {
		return false
	}
	// Free (or fail-closed unknown) plan: block only the off→on flip.
	return !currentPhoneEnabled && postedPhoneEnabled
}

// CreateProjectResponse is the JSON response after project creation.
type CreateProjectResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	Status    string `json:"status"`
	APIURL    string `json:"api_url"`
	PublicKey string `json:"public_key,omitempty"`
	SecretKey string `json:"secret_key,omitempty"`
}

// ProjectListItem represents a project in the list response.
type ProjectListItem struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Slug       string          `json:"slug"`
	Region     string          `json:"region"`
	Plan       string          `json:"plan"`
	Status     string          `json:"status"`
	APIURL     string          `json:"api_url"`
	AuthConfig json.RawMessage `json:"auth_config,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	// Team-tier org ownership (migration 000114). Nullable — a
	// non-null org_id surfaces the "Org" badge on the console
	// project card.
	OrgID   *string `json:"org_id,omitempty"`
	OrgName *string `json:"org_name,omitempty"`
}

var (
	slugRe      = regexp.MustCompile(`[^a-z0-9-]+`)
	validSlugRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

// reservedSlugs are subdomains the platform reserves for its own use
// (current or future). Closes #49: previously a tenant could grab a
// project slug like "www" or "admin" and collide with planned platform
// subdomains, OAuth callback paths, or brand-impersonation routes.
//
// Lowercase keys; check against strings.ToLower(slug). Add to this list
// if a new platform subdomain is planned.
var reservedSlugs = map[string]bool{
	"account":     true,
	"admin":       true,
	"api":         true,
	"app":         true,
	"auth":        true,
	"billing":     true,
	"blog":        true,
	"callback":    true,
	"cdn":         true,
	"console":     true,
	"dashboard":   true,
	"docs":        true,
	"eurobase":    true,
	"help":        true,
	"login":       true,
	"mail":        true,
	"marketing":   true,
	"mcp":         true,
	"oauth":       true,
	"public":      true,
	"security":    true,
	"signup":      true,
	"sso":         true,
	"static":      true,
	"status":      true,
	"superadmin":  true,
	"support":     true,
	"system":      true,
	"www":         true,
}

// slugIsReserved reports whether the slug collides with a platform-
// reserved subdomain or path.
func slugIsReserved(slug string) bool {
	return reservedSlugs[strings.ToLower(slug)]
}

// slugify converts a project name into a URL-safe slug.
//
// Reserved slugs (see slugIsReserved) get a "-app" suffix automatically
// when auto-derived. Explicitly-supplied reserved slugs are rejected at
// the handler.
func slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, " ", "-")
	s = slugRe.ReplaceAllString(s, "")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "project"
	}
	if slugIsReserved(s) {
		s = s + "-app"
	}
	return s
}

// HandleCreateProject returns an http.HandlerFunc that creates a new project
// (tenant) for the authenticated platform user.
//
// POST /v1/tenants
func HandleCreateProject(pool *pgxpool.Pool, svc *TenantService, limitsSvc ...*plans.LimitsService) http.HandlerFunc {
	_ = pool // pool is held by svc; kept in signature for consistency

	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok {
			slog.Warn("create tenant called without auth claims")
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		// Check project limit for the user's plan.
		if len(limitsSvc) > 0 && limitsSvc[0] != nil {
			if err := limitsSvc[0].CheckProjectLimit(r.Context(), claims.Subject); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
		}

		// Cap body at 16 KB. Legitimate CreateProjectRequest fits in
		// well under 1 KB (name + slug + region + plan + org_id);
		// bounding here matches the per-handler pattern used elsewhere
		// (support / team-beta-request / contact) and stops a hostile
		// client from streaming an unbounded payload into memory
		// during the raw-JSON peek. On overflow the ReadAll returns
		// http.MaxBytesError which we surface as 413.
		r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			slog.Warn("read create tenant request body failed", "error", err)
			var mbErr *http.MaxBytesError
			if errors.As(err, &mbErr) {
				http.Error(w, `{"error":"request body too large"}`, http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		var req CreateProjectRequest
		if err := json.Unmarshal(bodyBytes, &req); err != nil {
			slog.Warn("invalid create tenant request body", "error", err)
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		// Distinguish "org_id absent" from "org_id: null" — a *string in
		// Go maps both to nil, so peek the raw JSON to know which the
		// client actually sent. Present-and-null is the "force personal"
		// signal (Owner: Personal in the console picker); absent is the
		// legacy auto-attach behaviour.
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(bodyBytes, &probe); err == nil {
			if _, ok := probe["org_id"]; ok {
				req.OrgIDExplicit = true
			}
		}

		// #70 diagnostic: log the decoded plan/region so a repro of
		// "Pro selected → plan=free in DB" can be pinned to either the
		// HTTP body, the JSON decode, or somewhere downstream. Drop once
		// the bug is closed.
		slog.Info("create project: decoded body",
			"name", req.Name,
			"slug", req.Slug,
			"region", req.Region,
			"plan", req.Plan,
			"owner_id", claims.Subject,
		)

		// Validate name.
		if strings.TrimSpace(req.Name) == "" {
			http.Error(w, `{"error":"name is required"}`, http.StatusBadRequest)
			return
		}

		// Validate slug if provided; otherwise it will be derived from name.
		if req.Slug != "" {
			if !validSlugRe.MatchString(req.Slug) {
				http.Error(w, `{"error":"slug must be lowercase alphanumeric with hyphens (e.g. my-app)"}`, http.StatusBadRequest)
				return
			}
			if slugIsReserved(req.Slug) {
				http.Error(w, `{"error":"this slug is reserved for platform use — please choose a different name"}`, http.StatusBadRequest)
				return
			}
		}

		// Validate region: only fr-par is supported.
		if req.Region == "" {
			req.Region = "fr-par"
		}
		if req.Region != "fr-par" {
			http.Error(w, `{"error":"region must be fr-par"}`, http.StatusBadRequest)
			return
		}

		// Default plan.
		if req.Plan == "" {
			req.Plan = "free"
		}

		// #70 diagnostic: log right before the INSERT so we can see if
		// anything between decode and CreateProject mutated req.Plan.
		slog.Info("create project: handing to service", "plan", req.Plan, "name", req.Name)
		project, err := svc.CreateProject(r.Context(), claims.Subject, claims.Email, req)
		if err != nil {
			slog.Error("failed to create project", "error", err, "user_id", claims.Subject)
			if errors.Is(err, ErrTeamBetaRequired) {
				http.Error(w, `{"error":"team plan requires closed-beta access","code":"team_beta_required"}`, http.StatusForbidden)
				return
			}
			if errors.Is(err, ErrLegalTeamBetaRequired) {
				http.Error(w, `{"error":"legal team plan requires closed-beta access","code":"legal_team_beta_required"}`, http.StatusForbidden)
				return
			}
			if errors.Is(err, ErrOrgAttachForbidden) {
				http.Error(w, `{"error":"you must be an admin of the target organization to attach a project to it","code":"org_attach_forbidden"}`, http.StatusForbidden)
				return
			}
			if errors.Is(err, ErrOrgAttachTargetGone) {
				http.Error(w, `{"error":"target organization no longer exists; retry with a fresh org list","code":"org_attach_target_gone"}`, http.StatusConflict)
				return
			}
			if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
				http.Error(w, `{"error":"This project URL is already taken. Each project gets a unique subdomain (slug.eurobase.app), so please choose a different name or slug."}`, http.StatusConflict)
				return
			}
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		slog.Info("tenant created",
			"project_id", project.ID,
			"slug", project.Slug,
			"owner_id", claims.Subject,
		)

		resp := CreateProjectResponse{
			ID:        project.ID,
			Name:      project.Name,
			Slug:      project.Slug,
			Status:    project.Status,
			APIURL:    project.APIURL,
			PublicKey: project.PublicKey,
			SecretKey: project.SecretKey,
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(resp)
	}
}

// HandleListProjects returns an http.HandlerFunc that lists all projects
// owned by the authenticated platform user.
//
// GET /v1/tenants
func HandleListProjects(pool *pgxpool.Pool, svc *TenantService) http.HandlerFunc {
	_ = pool

	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok {
			slog.Warn("list tenants called without auth claims")
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		projects, err := svc.ListProjects(r.Context(), claims)
		if err != nil {
			slog.Error("failed to list projects", "error", err, "user_id", claims.Subject)
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		// Map to list items. AuthConfig is included so the console can render
		// the auth settings page without a second round-trip; it has already
		// been annotated (secret_set flags, no raw client_secret values) by
		// TenantService.ListProjects.
		items := make([]ProjectListItem, len(projects))
		for i, p := range projects {
			items[i] = ProjectListItem{
				ID:         p.ID,
				Name:       p.Name,
				Slug:       p.Slug,
				Region:     p.Region,
				Plan:       p.Plan,
				Status:     p.Status,
				APIURL:     p.APIURL,
				AuthConfig: p.AuthConfig,
				CreatedAt:  p.CreatedAt,
				OrgID:      p.OrgID,
				// OrgName intentionally omitted post-hotfix: resolving
				// the name requires JOIN to `organizations`, which the
				// gateway pool cannot SELECT. The console falls back to
				// showing a generic "Org" badge from org_id.
			}
		}

		slog.Debug("listed tenants", "count", len(items), "user_id", claims.Subject)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(items)
	}
}

// HandleUpdateProject handles PATCH /v1/tenants/{id} to update project settings (e.g. auth_config).
func HandleUpdateProject(pool *pgxpool.Pool, svc *TenantService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")

		claims, _, ok := RequireRole(w, r, pool, projectID, "admin")
		if !ok {
			return
		}

		var body struct {
			AuthConfig *AuthConfig `json:"auth_config"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}

		if body.AuthConfig == nil {
			http.Error(w, `{"error":"auth_config is required"}`, http.StatusBadRequest)
			return
		}

		if err := body.AuthConfig.Validate(); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		// #329: SMS auth is paid-tier-only. Reject a save that tries
		// to FLIP phone.enabled from off→on on a Free project — the
		// console grays out the toggle; this is the defense against
		// a direct API call.
		//
		// We check the transition, NOT the posted value alone: a
		// churned Pro project that persisted phone.enabled=true from
		// its Pro days keeps it in `auth_config` after the downgrade,
		// and the console re-posts it on every subsequent save of any
		// unrelated field (magic link, password length, session
		// duration). Blocking on the stable-on state would silently
		// trap those projects — they'd be unable to save ANY auth
		// setting until someone flipped phone off out-of-band, and
		// they can't do that in the UI because the toggle is
		// disabled. So the gate blocks the enable transition only;
		// the send/verify edges already block the actual SMS spend
		// regardless. See PR #418 review 🟡.
		if body.AuthConfig.IsPhoneAuthEnabled() {
			var plan string
			var currentPhoneEnabled bool
			if err := pool.QueryRow(r.Context(),
				`SELECT COALESCE(plan, 'free'),
				        COALESCE((auth_config->'providers'->'phone'->>'enabled')::bool, false)
				   FROM projects WHERE id = $1`, projectID,
			).Scan(&plan, &currentPhoneEnabled); err != nil {
				slog.Error("lookup project plan for phone-auth gate", "error", err, "project_id", projectID)
				http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
				return
			}
			if smsGateBlocks(plan, currentPhoneEnabled, true /* posted */) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusPaymentRequired)
				json.NewEncoder(w).Encode(map[string]any{
					"error": "SMS (phone) auth requires a paid plan — upgrade to Pro or higher to enable this provider",
					"code":  "paid_plan_required",
				})
				return
			}
		}

		rotated, err := svc.UpdateAuthConfig(r.Context(), projectID, claims.Subject, *body.AuthConfig)
		if err != nil {
			slog.Error("update auth config failed", "error", err, "project_id", projectID)
			if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "not owned") {
				http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
				return
			}
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		if auditSvc := audit.FromContext(r.Context()); auditSvc != nil {
			auditSvc.Log(r.Context(), projectID, claims.Subject, claims.Email,
				audit.ActionAuthConfigUpdated,
				audit.WithTarget("project", projectID),
				audit.WithIP(r.RemoteAddr))

			// Log individual secret rotations for compliance.
			for _, provider := range rotated {
				auditSvc.Log(r.Context(), projectID, claims.Subject, claims.Email,
					"oauth_secret.rotated",
					audit.WithTarget("oauth_provider", provider),
					audit.WithIP(r.RemoteAddr))
			}
		}

		project, err := svc.GetProject(r.Context(), projectID)
		if err != nil {
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(project)
	}
}

// HandleSetProjectOrg handles PATCH /platform/projects/{id}/org.
// Body: { "org_id": "<uuid>" }  attaches the project to that org.
// Body: { "org_id": null }      detaches (project becomes personal).
//
// Caller must be the project owner (existing PATCH pattern) and,
// when attaching, a member of the target org. Used by the console
// to migrate personal projects into an org after the fact (0a
// auto-attaches on create, this covers everything created before
// the org existed).
func HandleSetProjectOrg(pool *pgxpool.Pool, svc *TenantService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")

		claims, _, ok := RequireRole(w, r, pool, projectID, "owner")
		if !ok {
			return
		}

		// Distinguish "org_id absent from body" from "org_id: null":
		// absent = 400 (nothing to do), null = detach. json.Decode
		// into a *string doesn't tell us which, so we decode raw.
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		orgRaw, present := raw["org_id"]
		if !present {
			http.Error(w, `{"error":"org_id field is required (use null to detach)"}`, http.StatusBadRequest)
			return
		}
		var orgID *string
		if string(orgRaw) != "null" {
			var s string
			if err := json.Unmarshal(orgRaw, &s); err != nil {
				http.Error(w, `{"error":"org_id must be a UUID string or null"}`, http.StatusBadRequest)
				return
			}
			orgID = &s
		}

		if err := svc.SetProjectOrg(r.Context(), projectID, claims.Subject, orgID); err != nil {
			if errors.Is(err, ErrProjectNotFound) {
				http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
				return
			}
			if errors.Is(err, ErrOrgAttachForbidden) {
				http.Error(w, `{"error":"caller is not a member of the target organization"}`, http.StatusForbidden)
				return
			}
			if errors.Is(err, ErrOrgAttachTargetGone) {
				http.Error(w, `{"error":"target organization no longer exists; retry with a fresh org list"}`, http.StatusConflict)
				return
			}
			slog.Error("set project org failed", "error", err, "project_id", projectID)
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		if auditSvc := audit.FromContext(r.Context()); auditSvc != nil {
			action := "project.org_attached"
			if orgID == nil {
				action = "project.org_detached"
			}
			auditSvc.Log(r.Context(), projectID, claims.Subject, claims.Email,
				action,
				audit.WithTarget("project", projectID),
				audit.WithIP(r.RemoteAddr))
		}

		project, err := svc.GetProject(r.Context(), projectID)
		if err != nil {
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(project)
	}
}

// HandleDeleteProject deletes a project and its tenant schema.
//
// Two callers are authorised: the direct project owner (via
// project_members.role='owner') AND, for org-owned projects, any admin
// of the project's org (org_members.role='admin' on projects.org_id).
// The org-admin path was added after the first Team-org customer
// (qtune) hit the pre-org-plumbing gap: admin A creates the project,
// admin B (also org admin) is stuck asking A to delete it — no way for
// B to clean up if A leaves. GitHub / Google Workspace / Confluence all
// let org admins delete resources they didn't personally create; this
// mirrors that expectation. Same slug-confirm dialog still gates the
// action (client-side); audit log captures the actor either way.
//
// DELETE /v1/tenants/{id}
func HandleDeleteProject(pool *pgxpool.Pool, svc *TenantService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")

		claims, hasAuth := auth.ClaimsFromContext(r.Context())
		if !hasAuth || claims == nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		canDelete, err := svc.CanDeleteProject(r.Context(), projectID, claims.Subject)
		if err != nil {
			slog.Error("check delete permission", "error", err, "project_id", projectID)
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}
		if !canDelete {
			http.Error(w, `{"error":"forbidden: requires project owner role or org admin"}`, http.StatusForbidden)
			return
		}

		// Audit BEFORE deletion so the project_id FK still exists.
		if auditSvc := audit.FromContext(r.Context()); auditSvc != nil {
			auditSvc.Log(r.Context(), projectID, claims.Subject, claims.Email,
				audit.ActionProjectDeleted,
				audit.WithTarget("project", projectID),
				audit.WithIP(r.RemoteAddr))
		}

		if err := svc.DeleteProject(r.Context(), projectID); err != nil {
			slog.Error("delete project failed", "error", err, "project_id", projectID)
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
