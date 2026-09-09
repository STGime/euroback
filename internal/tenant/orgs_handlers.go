package tenant

// HTTP handlers for the org CRUD surface. Routes:
//   POST   /platform/orgs
//   GET    /platform/orgs
//   GET    /platform/orgs/{id}                  (folds in the member list)
//   PATCH  /platform/orgs/{id}/sso              (admin-only)
//   POST   /platform/orgs/{id}/members          (admin-only)
//   DELETE /platform/orgs/{id}/members/{userId} (admin-only)
//
// All routes are platform-authenticated (developer pool). Admin
// gate is per-handler because we already need to fetch the org +
// role for authorization — a middleware would add an extra DB hit.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/go-chi/chi/v5"
)

// OrgsHandler wires HTTP handlers over the OrgsService.
type OrgsHandler struct {
	Svc *OrgsService
}

// requireCallerID is a common preamble — pulls the platform user's
// UUID from JWT claims. All routes here are platform-authenticated,
// so a missing claim is a wiring bug, not a legitimate request.
func requireCallerID(w http.ResponseWriter, r *http.Request) (string, bool) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok || claims == nil || claims.Subject == "" {
		writeJSONErr(w, http.StatusUnauthorized, "session missing")
		return "", false
	}
	return claims.Subject, true
}

// writeJSONErr — same escape-safe pattern as contact_handlers.go.
// Preserves Content-Type: application/json on error responses
// (http.Error resets it to text/plain).
func writeJSONErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}

// HandleCreateOrg — POST /platform/orgs {name}
//
// Gated on team_beta_access: orgs + SSO are a Team-tier feature and
// pricing lists them as such. Existing invited members of an org
// keep access regardless of their own tier — only NEW org creation
// requires the flag. Admins can hand SSO admin off to a
// non-Team user, but SetSSOConfig re-checks (defence in depth) so
// SSO writes always require Team access on the caller.
func (h *OrgsHandler) HandleCreateOrg() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := requireCallerID(w, r)
		if !ok {
			return
		}
		granted, err := UserHasTeamBetaAccess(r.Context(), h.Svc.pool, userID)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "tier check failed")
			return
		}
		if !granted {
			writeJSONErr(w, http.StatusForbidden, "organizations are a Team-tier feature — upgrade to create one")
			return
		}
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		org, err := h.Svc.CreateOrg(r.Context(), userID, body.Name)
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(org)
	}
}

// HandleListOrgs — GET /platform/orgs
func (h *OrgsHandler) HandleListOrgs() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := requireCallerID(w, r)
		if !ok {
			return
		}
		orgs, err := h.Svc.ListOrgsForUser(r.Context(), userID)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "list failed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"orgs":  orgs,
			"total": len(orgs),
		})
	}
}

// HandleGetOrg — GET /platform/orgs/{id}
func (h *OrgsHandler) HandleGetOrg() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := requireCallerID(w, r)
		if !ok {
			return
		}
		orgID := chi.URLParam(r, "id")
		org, role, err := h.Svc.GetOrgForMember(r.Context(), userID, orgID)
		if err != nil {
			if errors.Is(err, ErrOrgMemberMissing) {
				writeJSONErr(w, http.StatusNotFound, "organization not found or not a member")
				return
			}
			writeJSONErr(w, http.StatusInternalServerError, "lookup failed")
			return
		}
		members, err := h.Svc.ListMembers(r.Context(), orgID)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "members lookup failed")
			return
		}
		var sso *OIDCConfigPublic
		if org.OIDCConfigured {
			sso, err = h.Svc.GetOIDCConfigPublic(r.Context(), orgID)
			if err != nil && !errors.Is(err, ErrOIDCNotConfigured) {
				writeJSONErr(w, http.StatusInternalServerError, "sso config lookup failed")
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"org":     org,
			"role":    role,
			"members": members,
			"sso":     sso,
		})
	}
}

// HandleSetSSOConfig — PATCH /platform/orgs/{id}/sso  (admin-only, Team-tier)
//
// Defence-in-depth Team-tier gate: the create-org path already
// requires team_beta_access, but the admin role can be handed off
// to a non-Team user via InviteMember + role=admin. Re-check on
// every SSO write so downgrading / handing off never silently
// unlocks the SSO surface for non-Team callers.
func (h *OrgsHandler) HandleSetSSOConfig() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.Svc.SSOConfigWritable() {
			writeJSONErr(w, http.StatusServiceUnavailable, "SSO config storage not configured (PLATFORM_ENCRYPTION_KEY missing)")
			return
		}
		userID, ok := requireCallerID(w, r)
		if !ok {
			return
		}
		granted, err := UserHasTeamBetaAccess(r.Context(), h.Svc.pool, userID)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "tier check failed")
			return
		}
		if !granted {
			writeJSONErr(w, http.StatusForbidden, "SSO configuration is a Team-tier feature — upgrade to configure")
			return
		}
		orgID := chi.URLParam(r, "id")
		_, role, err := h.Svc.GetOrgForMember(r.Context(), userID, orgID)
		if err != nil {
			if errors.Is(err, ErrOrgMemberMissing) {
				writeJSONErr(w, http.StatusNotFound, "organization not found or not a member")
				return
			}
			writeJSONErr(w, http.StatusInternalServerError, "auth check failed")
			return
		}
		if role != RoleOrgAdmin {
			writeJSONErr(w, http.StatusForbidden, ErrOrgAdminOnly.Error())
			return
		}
		var body struct {
			Provider     string `json:"provider"`
			Issuer       string `json:"issuer"`
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
			RedirectURL  string `json:"redirect_url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		cfg := OIDCConfig{
			Provider:     body.Provider,
			Issuer:       body.Issuer,
			ClientID:     body.ClientID,
			ClientSecret: body.ClientSecret,
			RedirectURL:  body.RedirectURL,
		}
		if err := h.Svc.SetSSOConfig(r.Context(), orgID, cfg); err != nil {
			writeJSONErr(w, http.StatusBadRequest, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "configured"})
	}
}

// HandleInviteMember — POST /platform/orgs/{id}/members {email,role}
func (h *OrgsHandler) HandleInviteMember() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := requireCallerID(w, r)
		if !ok {
			return
		}
		orgID := chi.URLParam(r, "id")
		_, role, err := h.Svc.GetOrgForMember(r.Context(), userID, orgID)
		if err != nil {
			if errors.Is(err, ErrOrgMemberMissing) {
				writeJSONErr(w, http.StatusNotFound, "organization not found or not a member")
				return
			}
			writeJSONErr(w, http.StatusInternalServerError, "auth check failed")
			return
		}
		if role != RoleOrgAdmin {
			writeJSONErr(w, http.StatusForbidden, ErrOrgAdminOnly.Error())
			return
		}
		var body struct {
			Email string `json:"email"`
			Role  string `json:"role"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		if body.Role == "" {
			body.Role = RoleOrgMember
		}
		m, err := h.Svc.InviteMember(r.Context(), orgID, body.Email, body.Role)
		if err != nil {
			switch {
			case errors.Is(err, ErrUserNotFound):
				writeJSONErr(w, http.StatusNotFound, "no platform user with that email — user must sign up first")
			case errors.Is(err, ErrMemberExists):
				writeJSONErr(w, http.StatusConflict, "user is already a member")
			default:
				writeJSONErr(w, http.StatusBadRequest, err.Error())
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(m)
	}
}

// HandleRemoveMember — DELETE /platform/orgs/{id}/members/{userId}
func (h *OrgsHandler) HandleRemoveMember() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := requireCallerID(w, r)
		if !ok {
			return
		}
		orgID := chi.URLParam(r, "id")
		targetID := chi.URLParam(r, "userId")
		_, role, err := h.Svc.GetOrgForMember(r.Context(), userID, orgID)
		if err != nil {
			if errors.Is(err, ErrOrgMemberMissing) {
				writeJSONErr(w, http.StatusNotFound, "organization not found or not a member")
				return
			}
			writeJSONErr(w, http.StatusInternalServerError, "auth check failed")
			return
		}
		if role != RoleOrgAdmin {
			writeJSONErr(w, http.StatusForbidden, ErrOrgAdminOnly.Error())
			return
		}
		if err := h.Svc.RemoveMember(r.Context(), orgID, targetID); err != nil {
			writeJSONErr(w, http.StatusBadRequest, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
