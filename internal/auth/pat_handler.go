package auth

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// HandleListPATs returns the caller's personal access tokens.
//
// GET /platform/auth/account/tokens
func HandleListPATs(svc *PATService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		if isPATAuth(r) {
			writeJSONError(w, "personal access tokens cannot create other tokens; sign in to the console first", http.StatusForbidden)
			return
		}

		var req struct {
			Name      string     `json:"name"`
			ExpiresAt *time.Time `json:"expires_at"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		result, err := svc.Create(r.Context(), CreateInput{
			UserID:    claims.Subject,
			Name:      req.Name,
			ExpiresAt: req.ExpiresAt,
		})
		if err != nil {
			slog.Warn("create pat failed", "error", err, "user_id", claims.Subject)
			writeJSONError(w, err.Error(), http.StatusBadRequest)
			return
		}

		slog.Info("pat created", "user_id", claims.Subject, "token_id", result.PAT.ID, "name", result.PAT.Name)

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

func isPATAuth(r *http.Request) bool {
	const bearer = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(bearer) {
		return false
	}
	return len(h) > len(bearer)+len(PATPrefix) && h[len(bearer):len(bearer)+len(PATPrefix)] == PATPrefix
}
