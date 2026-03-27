// Package webhook provides HTTP handlers for customer-configurable webhook management.
package webhook

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/eurobase/euroback/internal/plans"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Webhook represents a customer-configured webhook endpoint.
type Webhook struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	URL         string    `json:"url"`
	Events      []string  `json:"events"`
	Secret      string    `json:"secret,omitempty"`
	Enabled     bool      `json:"enabled"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

// WebhookDelivery represents a single delivery attempt.
type WebhookDelivery struct {
	ID         string    `json:"id"`
	WebhookID  string    `json:"webhook_id"`
	Event      string    `json:"event"`
	Payload    any       `json:"payload"`
	StatusCode *int      `json:"status_code"`
	Response   *string   `json:"response"`
	Attempts   int       `json:"attempts"`
	Success    bool      `json:"success"`
	CreatedAt  time.Time `json:"created_at"`
}

type createWebhookRequest struct {
	URL         string   `json:"url"`
	Events      []string `json:"events"`
	Description string   `json:"description"`
}

// Routes returns a chi.Router for webhook CRUD operations.
// Mounted at /platform/projects/{id}/webhooks
func Routes(pool *pgxpool.Pool, limitsSvc ...*plans.LimitsService) chi.Router {
	var svc *plans.LimitsService
	if len(limitsSvc) > 0 {
		svc = limitsSvc[0]
	}
	r := chi.NewRouter()
	r.Get("/", handleList(pool))
	r.Post("/", handleCreate(pool, svc))
	r.Delete("/{webhookId}", handleDelete(pool))
	r.Patch("/{webhookId}", handleUpdate(pool))
	r.Get("/{webhookId}/deliveries", handleDeliveries(pool))
	return r
}

func handleList(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		rows, err := pool.Query(r.Context(),
			`SELECT id, project_id, url, events, enabled, description, created_at
			 FROM webhooks WHERE project_id = $1 ORDER BY created_at DESC`, projectID)
		if err != nil {
			slog.Error("list webhooks failed", "error", err)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		webhooks := make([]Webhook, 0)
		for rows.Next() {
			var wh Webhook
			if err := rows.Scan(&wh.ID, &wh.ProjectID, &wh.URL, &wh.Events, &wh.Enabled, &wh.Description, &wh.CreatedAt); err != nil {
				slog.Error("scan webhook failed", "error", err)
				jsonError(w, "internal server error", http.StatusInternalServerError)
				return
			}
			webhooks = append(webhooks, wh)
		}

		jsonResponse(w, webhooks, http.StatusOK)
	}
}

func handleCreate(pool *pgxpool.Pool, limitsSvc *plans.LimitsService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")

		// Check webhook limit for the project's plan.
		if limitsSvc != nil {
			if err := limitsSvc.CheckWebhookLimit(r.Context(), projectID); err != nil {
				jsonError(w, err.Error(), http.StatusForbidden)
				return
			}
		}

		var req createWebhookRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.URL == "" {
			jsonError(w, "url is required", http.StatusBadRequest)
			return
		}
		if len(req.Events) == 0 {
			jsonError(w, "at least one event is required", http.StatusBadRequest)
			return
		}

		// Generate a signing secret.
		secret, err := generateSecret()
		if err != nil {
			slog.Error("generate webhook secret failed", "error", err)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}

		var wh Webhook
		err = pool.QueryRow(r.Context(),
			`INSERT INTO webhooks (project_id, url, events, secret, description)
			 VALUES ($1, $2, $3, $4, $5)
			 RETURNING id, project_id, url, events, secret, enabled, description, created_at`,
			projectID, req.URL, req.Events, secret, req.Description,
		).Scan(&wh.ID, &wh.ProjectID, &wh.URL, &wh.Events, &wh.Secret, &wh.Enabled, &wh.Description, &wh.CreatedAt)
		if err != nil {
			slog.Error("create webhook failed", "error", err)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}

		slog.Info("webhook created", "webhook_id", wh.ID, "project_id", projectID, "url", req.URL)
		jsonResponse(w, wh, http.StatusCreated)
	}
}

func handleDelete(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		webhookID := chi.URLParam(r, "webhookId")

		tag, err := pool.Exec(r.Context(),
			`DELETE FROM webhooks WHERE id = $1 AND project_id = $2`, webhookID, projectID)
		if err != nil {
			slog.Error("delete webhook failed", "error", err)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if tag.RowsAffected() == 0 {
			jsonError(w, "webhook not found", http.StatusNotFound)
			return
		}

		slog.Info("webhook deleted", "webhook_id", webhookID, "project_id", projectID)
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleUpdate(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		webhookID := chi.URLParam(r, "webhookId")

		var body struct {
			URL         *string  `json:"url"`
			Events      []string `json:"events"`
			Enabled     *bool    `json:"enabled"`
			Description *string  `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		var wh Webhook
		err := pool.QueryRow(r.Context(),
			`UPDATE webhooks SET
				url = COALESCE($3, url),
				events = COALESCE($4, events),
				enabled = COALESCE($5, enabled),
				description = COALESCE($6, description)
			 WHERE id = $1 AND project_id = $2
			 RETURNING id, project_id, url, events, enabled, description, created_at`,
			webhookID, projectID, body.URL, body.Events, body.Enabled, body.Description,
		).Scan(&wh.ID, &wh.ProjectID, &wh.URL, &wh.Events, &wh.Enabled, &wh.Description, &wh.CreatedAt)
		if err != nil {
			slog.Error("update webhook failed", "error", err)
			jsonError(w, "webhook not found", http.StatusNotFound)
			return
		}

		jsonResponse(w, wh, http.StatusOK)
	}
}

func handleDeliveries(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		webhookID := chi.URLParam(r, "webhookId")

		rows, err := pool.Query(r.Context(),
			`SELECT id, webhook_id, event, payload, status_code, response, attempts, success, created_at
			 FROM webhook_deliveries WHERE webhook_id = $1 ORDER BY created_at DESC LIMIT 50`, webhookID)
		if err != nil {
			slog.Error("list deliveries failed", "error", err)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		deliveries := make([]WebhookDelivery, 0)
		for rows.Next() {
			var d WebhookDelivery
			if err := rows.Scan(&d.ID, &d.WebhookID, &d.Event, &d.Payload, &d.StatusCode, &d.Response, &d.Attempts, &d.Success, &d.CreatedAt); err != nil {
				slog.Error("scan delivery failed", "error", err)
				jsonError(w, "internal server error", http.StatusInternalServerError)
				return
			}
			deliveries = append(deliveries, d)
		}

		jsonResponse(w, deliveries, http.StatusOK)
	}
}

func jsonResponse(w http.ResponseWriter, data any, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func generateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "whsec_" + hex.EncodeToString(b), nil
}
