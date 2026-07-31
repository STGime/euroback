package billing

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/eurobase/euroback/internal/auth"
)

// checkoutRequest is the JSON body of POST /platform/billing/checkout.
// Both fields are required. planCode is validated inside the service
// so the same allow-list applies whether the caller is the console
// or a future CLI.
type checkoutRequest struct {
	ProjectID string `json:"project_id"`
	PlanCode  string `json:"plan_code"`
}

// checkoutResponse mirrors CheckoutResult with JSON-friendly field
// names. Kept separate from the service type so an internal rename
// doesn't leak into the console contract.
type checkoutResponse struct {
	SubscriptionID string `json:"subscription_id"`
	CheckoutURL    string `json:"checkout_url"`
}

// HandleCreateCheckout is POST /platform/billing/checkout. Auth-only
// (relies on the outer platform auth middleware to have populated
// claims); the service does the ownership check on the project.
//
// Error → status mapping:
//
//   ErrBillingDisabled     → 503 (billing not enabled in this env)
//   ErrProjectNotFound     → 404 (not-found = not-owned; deliberate)
//   ErrAlreadySubscribed   → 409 (also fired by the unique index race)
//   ErrInvalidPlan         → 400
//   everything else        → 500 + slog.Error with the wrapped chain
//
// The 503 branch fires *before* JSON parsing so an accidental
// enablement-check inversion doesn't leak internal error details in
// a locked-down environment.
func HandleCreateCheckout(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !svc.Enabled() {
			writeJSONError(w, http.StatusServiceUnavailable, "billing_disabled", "billing is not enabled in this environment")
			return
		}

		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok || claims == nil {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}

		var req checkoutRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_body", "request body must be JSON")
			return
		}
		if req.ProjectID == "" {
			writeJSONError(w, http.StatusBadRequest, "missing_project_id", "project_id is required")
			return
		}
		if req.PlanCode == "" {
			writeJSONError(w, http.StatusBadRequest, "missing_plan_code", "plan_code is required")
			return
		}

		res, err := svc.CreateCheckout(r.Context(), claims.Subject, req.ProjectID, strings.ToLower(req.PlanCode))
		if err != nil {
			switch {
			case errors.Is(err, ErrBillingDisabled):
				writeJSONError(w, http.StatusServiceUnavailable, "billing_disabled", "billing is not enabled in this environment")
			case errors.Is(err, ErrProjectNotFound):
				writeJSONError(w, http.StatusNotFound, "project_not_found", "project not found")
			case errors.Is(err, ErrAlreadySubscribed):
				writeJSONError(w, http.StatusConflict, "already_subscribed", "project already has an active or pending subscription")
			case errors.Is(err, ErrInvalidPlan):
				writeJSONError(w, http.StatusBadRequest, "invalid_plan", err.Error())
			default:
				slog.Error("billing: checkout failed",
					"user_id", claims.Subject,
					"project_id", req.ProjectID,
					"plan_code", req.PlanCode,
					"error", err,
				)
				writeJSONError(w, http.StatusInternalServerError, "internal_error", "checkout failed")
			}
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(checkoutResponse{
			SubscriptionID: res.SubscriptionID,
			CheckoutURL:    res.CheckoutURL,
		})
	}
}

// writeJSONError emits the platform's standard error envelope. Kept
// package-private since it's tuned to billing's needs (no request-ID
// echo, no i18n) — other packages have their own writers with the
// specifics they care about.
func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   code,
		"message": message,
	})
}
