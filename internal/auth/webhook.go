package auth

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

// hankoWebhookPayload represents the incoming Hanko webhook event structure.
type hankoWebhookPayload struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// hankoUserData represents user data sent by Hanko in webhook events.
type hankoUserData struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

// HandleHankoWebhook returns an http.HandlerFunc that processes Hanko webhook
// events for user lifecycle management (user.created, user.updated).
func HandleHankoWebhook(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var payload hankoWebhookPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			slog.Error("failed to decode webhook payload", "error", err)
			http.Error(w, `{"error":"invalid payload"}`, http.StatusBadRequest)
			return
		}

		slog.Info("received Hanko webhook", "event", payload.Event)

		switch payload.Event {
		case "user.created":
			handleUserCreated(w, r, pool, payload.Data)
		case "user.updated":
			handleUserUpdated(w, r, pool, payload.Data)
		default:
			slog.Info("ignoring unhandled webhook event", "event", payload.Event)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ignored"}`))
		}
	}
}

func handleUserCreated(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, data json.RawMessage) {
	var user hankoUserData
	if err := json.Unmarshal(data, &user); err != nil {
		slog.Error("failed to unmarshal user.created data", "error", err)
		http.Error(w, `{"error":"invalid user data"}`, http.StatusBadRequest)
		return
	}

	if user.ID == "" || user.Email == "" {
		slog.Warn("user.created event missing required fields", "id", user.ID, "email", user.Email)
		http.Error(w, `{"error":"missing user id or email"}`, http.StatusBadRequest)
		return
	}

	_, err := pool.Exec(r.Context(),
		`INSERT INTO platform_users (hanko_user_id, email, display_name)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (hanko_user_id) DO UPDATE
		 SET email = EXCLUDED.email, display_name = EXCLUDED.display_name`,
		user.ID, user.Email, user.DisplayName,
	)
	if err != nil {
		slog.Error("failed to upsert platform user", "error", err, "hanko_user_id", user.ID)
		http.Error(w, `{"error":"database error"}`, http.StatusInternalServerError)
		return
	}

	slog.Info("platform user created", "hanko_user_id", user.ID, "email", user.Email)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

func handleUserUpdated(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, data json.RawMessage) {
	var user hankoUserData
	if err := json.Unmarshal(data, &user); err != nil {
		slog.Error("failed to unmarshal user.updated data", "error", err)
		http.Error(w, `{"error":"invalid user data"}`, http.StatusBadRequest)
		return
	}

	if user.ID == "" {
		slog.Warn("user.updated event missing user id")
		http.Error(w, `{"error":"missing user id"}`, http.StatusBadRequest)
		return
	}

	_, err := pool.Exec(r.Context(),
		`UPDATE platform_users SET email = COALESCE(NULLIF($2, ''), email),
		 display_name = COALESCE(NULLIF($3, ''), display_name)
		 WHERE hanko_user_id = $1`,
		user.ID, user.Email, user.DisplayName,
	)
	if err != nil {
		slog.Error("failed to update platform user", "error", err, "hanko_user_id", user.ID)
		http.Error(w, `{"error":"database error"}`, http.StatusInternalServerError)
		return
	}

	slog.Info("platform user updated", "hanko_user_id", user.ID)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}
