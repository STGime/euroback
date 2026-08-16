package billing

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/go-chi/chi/v5/middleware"
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
				// Echo the chi request ID on the 500 body so a
				// user reporting "checkout failed with error X"
				// gives support a token that correlates to a
				// single ingress log line + slog line.
				reqID := middleware.GetReqID(r.Context())
				slog.Error("billing: checkout failed",
					"request_id", reqID,
					"user_id", claims.Subject,
					"project_id", req.ProjectID,
					"plan_code", req.PlanCode,
					"error", err,
				)
				writeJSONErrorWithRequestID(w, http.StatusInternalServerError, "internal_error", "checkout failed", reqID)
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

// newProjectCheckoutRequest is the JSON body of
// POST /platform/billing/checkout/new-project. All fields required.
// name/slug/region are validated + persisted on pending_projects;
// the actual project is created by the webhook once Mollie confirms
// first payment. See issue #406 for the payment-first flow.
type newProjectCheckoutRequest struct {
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Region   string `json:"region"`
	PlanCode string `json:"plan_code"`
}

// newProjectCheckoutResponse mirrors NewProjectCheckoutResult with
// JSON-friendly field names.
type newProjectCheckoutResponse struct {
	PendingProjectID string `json:"pending_project_id"`
	CheckoutURL      string `json:"checkout_url"`
}

// HandleNewProjectCheckout is POST /platform/billing/checkout/new-project.
// Called by the console when a user clicks "Create Project" with a
// paid plan selected. Starts a Mollie checkout WITHOUT creating the
// project — the webhook creates the project after payment lands.
//
// Error → status mapping:
//
//	ErrBillingDisabled  → 503
//	ErrInvalidPlan      → 400 (also: missing fields, plan != 'pro')
//	ErrPlanNotPriced    → 400 (plan_limits.price_cents is NULL)
//	everything else     → 500 + slog.Error with the wrapped chain
//
// No ownership check (no project to own yet). No ErrAlreadySubscribed
// branch — pending_projects has an in-flight guard inside the service
// that transparently returns the existing checkout URL rather than
// erroring, so a double-click is a no-op instead of a 409.
func HandleNewProjectCheckout(svc *Service) http.HandlerFunc {
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

		var req newProjectCheckoutRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_body", "request body must be JSON")
			return
		}
		if req.Name == "" {
			writeJSONError(w, http.StatusBadRequest, "missing_name", "name is required")
			return
		}
		if req.Slug == "" {
			writeJSONError(w, http.StatusBadRequest, "missing_slug", "slug is required")
			return
		}
		if req.Region == "" {
			writeJSONError(w, http.StatusBadRequest, "missing_region", "region is required")
			return
		}
		if req.PlanCode == "" {
			writeJSONError(w, http.StatusBadRequest, "missing_plan_code", "plan_code is required")
			return
		}

		res, err := svc.NewProjectCheckout(r.Context(), claims.Subject, NewProjectCheckoutRequest{
			Name:   req.Name,
			Slug:   req.Slug,
			Region: req.Region,
			Plan:   strings.ToLower(req.PlanCode),
		})
		if err != nil {
			switch {
			case errors.Is(err, ErrBillingDisabled):
				writeJSONError(w, http.StatusServiceUnavailable, "billing_disabled", "billing is not enabled in this environment")
			case errors.Is(err, ErrInvalidPlan):
				writeJSONError(w, http.StatusBadRequest, "invalid_plan", err.Error())
			case errors.Is(err, ErrPlanNotPriced):
				writeJSONError(w, http.StatusBadRequest, "plan_not_priced", "this plan is not available for direct checkout")
			case errors.Is(err, ErrSlugTaken):
				writeJSONError(w, http.StatusConflict, "slug_taken", "this project name is already in use — please choose another")
			case errors.Is(err, ErrPendingCheckoutInFlight):
				writeJSONError(w, http.StatusConflict, "pending_checkout_in_flight", "another checkout is already in progress for your account — please complete it first or wait a few minutes")
			default:
				reqID := middleware.GetReqID(r.Context())
				slog.Error("billing: new-project checkout failed",
					"request_id", reqID,
					"user_id", claims.Subject,
					"plan_code", req.PlanCode,
					"error", err,
				)
				writeJSONErrorWithRequestID(w, http.StatusInternalServerError, "internal_error", "checkout failed", reqID)
			}
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(newProjectCheckoutResponse{
			PendingProjectID: res.PendingProjectID,
			CheckoutURL:      res.CheckoutURL,
		})
	}
}

// configResponse is the JSON body of GET /platform/billing/config.
// The console reads this once on billing-page mount to decide
// whether to render the "test mode — no card is charged" banner.
type configResponse struct {
	Enabled bool   `json:"enabled"`
	Mode    string `json:"mode"`
}

// HandleGetConfig is GET /platform/billing/config. Returns the
// current billing feature-flag state and Mollie environment so the
// console can render a test-mode banner on billing surfaces. Kept
// deliberately shallow — no secrets, no per-user state — so it's
// cheap to hit on every billing-page mount.
//
// Unlike the other billing handlers, this one does NOT 503 when
// billing is disabled — the console needs to know that fact to
// decide whether to show the upgrade button at all. Returns
// {"enabled": false, "mode": ""} when off.
func HandleGetConfig(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(configResponse{
			Enabled: svc.Enabled(),
			Mode:    svc.Mode(),
		})
	}
}

// writeJSONError emits the platform's standard error envelope.
// Non-500 responses omit the request ID (the user-facing error is
// already actionable — "invalid_plan", "already_subscribed"); 500
// responses use writeJSONErrorWithRequestID below so support can
// correlate.
func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   code,
		"message": message,
	})
}

// writeJSONErrorWithRequestID emits the standard envelope plus a
// request_id field. Called from the 500 branch so a user pasting
// their error message into support gives us a token that indexes
// straight into slog + the ingress log.
func writeJSONErrorWithRequestID(w http.ResponseWriter, status int, code, message, requestID string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]string{
		"error":   code,
		"message": message,
	}
	if requestID != "" {
		body["request_id"] = requestID
	}
	_ = json.NewEncoder(w).Encode(body)
}
