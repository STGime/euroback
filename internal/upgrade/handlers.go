package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/go-chi/chi/v5"
)

// Handlers mounts the admin HTTP endpoints:
//
//	POST   /platform/admin/upgrades              — start an upgrade
//	GET    /platform/admin/upgrades              — list recent
//	GET    /platform/admin/upgrades/{id}         — get one
//	POST   /platform/admin/upgrades/{id}/abort   — mark failed early
//	POST   /platform/admin/upgrades/{id}/confirm — early confirm (skip 7-day wait)
//
// Superadmin only. The caller in gateway/router.go wraps the group
// under superadminMiddleware(pool) — no per-handler role check needed.
type Handlers struct {
	svc *Service
}

func NewHandlers(svc *Service) *Handlers { return &Handlers{svc: svc} }

// Mount attaches all handlers under the given chi router. The router
// caller decides where — typically inside /platform/admin.
func (h *Handlers) Mount(r chi.Router) {
	r.Post("/upgrades", h.handleRequest)
	r.Get("/upgrades", h.handleList)
	r.Get("/upgrades/{id}", h.handleGet)
	r.Post("/upgrades/{id}/abort", h.handleAbort)
	r.Post("/upgrades/{id}/confirm", h.handleConfirm)
}

type requestBody struct {
	ProjectID string `json:"project_id"`
	ToPlan    string `json:"to_plan"`
}

func (h *Handlers) handleRequest(w http.ResponseWriter, r *http.Request) {
	var body requestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.ProjectID == "" || body.ToPlan == "" {
		writeJSONError(w, http.StatusBadRequest, "project_id and to_plan required")
		return
	}
	adminID := currentAdminID(r.Context())
	id, err := h.svc.RequestUpgrade(r.Context(), body.ProjectID, adminID, body.ToPlan)
	if err != nil {
		writeErrByType(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"id": id, "state": string(StateRequested)})
}

func (h *Handlers) handleList(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.ListUpgrades(r.Context(), 100)
	if err != nil {
		slog.Error("list upgrades failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "list failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"upgrades": list})
}

func (h *Handlers) handleGet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	up, err := h.svc.GetUpgrade(r.Context(), id)
	if err != nil {
		writeErrByType(w, err)
		return
	}
	writeJSON(w, http.StatusOK, up)
}

func (h *Handlers) handleAbort(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.AbortUpgrade(r.Context(), id); err != nil {
		writeErrByType(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "state": string(StateFailed)})
}

func (h *Handlers) handleConfirm(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.ConfirmUpgrade(r.Context(), id); err != nil {
		writeErrByType(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "state": string(StateConfirmed)})
}

// currentAdminID pulls the admin user ID from the auth claims. Empty
// string when no claims (dev mode); the FK on project_upgrades
// triggered_by is nullable so "" becomes NULL.
func currentAdminID(ctx context.Context) string {
	if claims, ok := auth.ClaimsFromContext(ctx); ok && claims != nil {
		return claims.Subject
	}
	return ""
}

// writeErrByType maps our exported Err* sentinels to HTTP status
// codes. Anything else is a wrapped internal error → 500.
func writeErrByType(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrProjectNotFound):
		writeJSONError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrUpgradeNotFound):
		writeJSONError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrUnsupportedPlan):
		writeJSONError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrAlreadyTeam):
		writeJSONError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrUpgradeInFlight):
		writeJSONError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrTeamBetaAccessMissing),
		errors.Is(err, ErrLegalTeamBetaAccessMissing):
		writeJSONError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrUpgradeTerminal):
		writeJSONError(w, http.StatusConflict, err.Error())
	default:
		slog.Error("upgrade admin handler internal error", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
