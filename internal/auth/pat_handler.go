package auth

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// HandleListPATs returns the caller's personal access tokens.
//
// GET /platform/auth/account/tokens
func HandleListPATs(svc *PATService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if refuseDelegatedSession(w, r, "list or manage tokens") {
			return
		}
		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			writeJSONError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		tokens, err := svc.List(r.Context(), claims.Subject)
		if err != nil {
			slog.Warn("list pats failed", "error", err, "user_id", claims.Subject)
			writeJSONError(w, "failed to list tokens", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tokens)
	}
}

// HandleCreatePAT issues a new PAT and returns the plaintext token once.
// PATs cannot be created using a PAT — only a fresh JWT (i.e. the
// authenticated console session). This prevents lateral escalation:
// even if a PAT leaks, an attacker can't mint additional ones.
//
// POST /platform/auth/account/tokens
//
//	{"name":"my laptop","expires_at":"2027-01-01T00:00:00Z"}  (expires_at optional)
func HandleCreatePAT(svc *PATService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			writeJSONError(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// Block PAT-authenticated callers from minting more PATs. The
		// middleware sets IsSuperadmin=false for PAT claims, but doesn't
		// otherwise distinguish the auth source — so we re-check the
		// Authorization header directly.
		if IsPATSession(r) {
			writeJSONError(w, "personal access tokens cannot create other tokens; sign in to the console first", http.StatusForbidden)
			return
		}
		if refuseDelegatedSession(w, r, "create tokens") {
			return
		}

		// New tokens are for one project with one role (#702); no
		// expiry — revoke in the console when no longer needed.
		var req struct {
			Name      string `json:"name"`
			ProjectID string `json:"project_id"`
			Role      string `json:"role"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		result, err := svc.Create(r.Context(), CreateInput{
			Claims:    claims,
			Name:      req.Name,
			ProjectID: req.ProjectID,
			Role:      req.Role,
		})
		if err != nil {
			slog.Warn("create pat failed", "error", err, "user_id", claims.Subject)
			switch {
			case errors.Is(err, ErrPATProjectNotFound):
				writeJSONError(w, err.Error(), http.StatusNotFound)
			case errors.Is(err, ErrPATRoleTooHigh):
				writeJSONError(w, err.Error(), http.StatusForbidden)
			default:
				writeJSONError(w, err.Error(), http.StatusBadRequest)
			}
			return
		}

		slog.Info("pat created", "user_id", claims.Subject, "token_id", result.PAT.ID, "name", result.PAT.Name, "project_id", req.ProjectID, "role", req.Role)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token": result.PlaintextToken, // shown once
			"pat":   result.PAT,
		})
	}
}

// HandleRevokePAT deletes one of the caller's PATs.
//
// DELETE /platform/auth/account/tokens/{id}
func HandleRevokePAT(svc *PATService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if refuseDelegatedSession(w, r, "revoke tokens") {
			return
		}
		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			writeJSONError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		tokenID := chi.URLParam(r, "id")
		if tokenID == "" {
			writeJSONError(w, "missing token id", http.StatusBadRequest)
			return
		}
		err := svc.Revoke(r.Context(), claims.Subject, tokenID)
		if err != nil {
			if errors.Is(err, ErrPATNotFound) {
				writeJSONError(w, "token not found", http.StatusNotFound)
				return
			}
			slog.Warn("revoke pat failed", "error", err, "user_id", claims.Subject, "token_id", tokenID)
			writeJSONError(w, "failed to revoke token", http.StatusInternalServerError)
			return
		}
		slog.Info("pat revoked", "user_id", claims.Subject, "token_id", tokenID)
		w.WriteHeader(http.StatusNoContent)
	}
}

// refusePAT answers 403 and returns true when the request authenticated
// with a personal access token. Account-level actions (delete the account,
// change the password, manage tokens and passkeys) need a console session:
// a token is handed to tools (an MCP connector, CI), and whoever holds it
// must not be able to take over or destroy the account.
func refusePAT(w http.ResponseWriter, r *http.Request, what string) bool {
	if !IsPATSession(r) {
		return false
	}
	writeJSONError(w, "personal access tokens cannot "+what+"; sign in to the console", http.StatusForbidden)
	return true
}

// refuseDelegatedSession refuses what refusePAT refuses, and also a session
// from an organization's SSO: the org's admin runs that identity provider,
// so such a session must not act for the person — manage their tokens,
// delete or re-secure their account, or accept invitations in their name.
// Those need the user's own sign-in (password or passkey).
func refuseDelegatedSession(w http.ResponseWriter, r *http.Request, what string) bool {
	if refusePAT(w, r, what) {
		return true
	}
	if c, ok := ClaimsFromContext(r.Context()); ok && c != nil && c.LoginVia == LoginViaSSO {
		writeJSONError(w, "a session from an organization's single sign-on cannot "+what+"; sign in with your password or passkey", http.StatusForbidden)
		return true
	}
	return false
}

// RequirePersonalSession refuses personal access tokens and SSO sessions
// (see refuseDelegatedSession): for things that belong to the person —
// accepting or declining invitations, billing, creating an organization.
func RequirePersonalSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refuseDelegatedSession(w, r, "do this") {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// IsPATSession reports whether the request authenticated with a personal
// access token: the claims say so (set where the token was validated), or
// — belt and braces — the Authorization header carries one.
func IsPATSession(r *http.Request) bool {
	if c, ok := ClaimsFromContext(r.Context()); ok && c != nil && c.LoginVia == LoginViaPAT {
		return true
	}
	return isPATAuth(r)
}

// RequireConsoleSession refuses personal access tokens (403). For routes
// that change who can access what — org settings and membership, project
// membership — which stay in the console: a token is handed to tools, and
// whoever holds it must not be able to grant themselves or others access.
func RequireConsoleSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refusePAT(w, r, "change organization or member settings") {
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isPATAuth(r *http.Request) bool {
	const bearer = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(bearer) {
		return false
	}
	return len(h) > len(bearer)+len(PATPrefix) && h[len(bearer):len(bearer)+len(PATPrefix)] == PATPrefix
}
