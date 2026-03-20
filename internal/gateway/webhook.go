// Package gateway provides HTTP handlers for platform-level endpoints.
package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
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

// HankoWebhookHandler returns an http.HandlerFunc that processes Hanko webhook
// events for user lifecycle management (user.created, user.deleted, user.updated).
// It validates webhook authenticity using the provided shared secret.
func HankoWebhookHandler(pool *pgxpool.Pool, webhookSecret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Read the raw body for signature verification.
		body, err := io.ReadAll(r.Body)
		if err != nil {
			slog.Error("failed to read webhook body", "error", err)
			http.Error(w, `{"error":"failed to read body"}`, http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		// Validate webhook authenticity via HMAC-SHA256 signature.
		signature := r.Header.Get("X-Hanko-Signature")
		if signature == "" {
			slog.Warn("missing webhook signature header")
			http.Error(w, `{"error":"missing signature"}`, http.StatusUnauthorized)
			return
		}

		if !verifyHMAC(body, signature, webhookSecret) {
			slog.Warn("invalid webhook signature")
			http.Error(w, `{"error":"invalid signature"}`, http.StatusUnauthorized)
			return
		}

		var payload hankoWebhookPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			slog.Error("failed to decode webhook payload", "error", err)
			http.Error(w, `{"error":"invalid payload"}`, http.StatusBadRequest)
			return
		}

		slog.Info("received Hanko webhook", "event", payload.Event)

		ctx := r.Context()

		switch payload.Event {
		case "user.created", "user.updated":
			var user hankoUserData
			if err := json.Unmarshal(payload.Data, &user); err != nil {
				slog.Error("failed to unmarshal user data", "error", err, "event", payload.Event)
				http.Error(w, `{"error":"invalid user data"}`, http.StatusBadRequest)
				return
			}

			if user.ID == "" {
				slog.Warn("webhook event missing user id", "event", payload.Event)
				http.Error(w, `{"error":"missing user id"}`, http.StatusBadRequest)
				return
			}

			if payload.Event == "user.created" && user.Email == "" {
				slog.Warn("user.created event missing email", "id", user.ID)
				http.Error(w, `{"error":"missing email for user.created"}`, http.StatusBadRequest)
				return
			}

			_, err := pool.Exec(ctx,
				`INSERT INTO platform_users (hanko_user_id, email, display_name)
				 VALUES ($1, $2, $3)
				 ON CONFLICT (hanko_user_id) DO UPDATE
				 SET email = COALESCE(NULLIF(EXCLUDED.email, ''), platform_users.email),
				     display_name = COALESCE(NULLIF(EXCLUDED.display_name, ''), platform_users.display_name)`,
				user.ID, user.Email, user.DisplayName,
			)
			if err != nil {
				slog.Error("failed to upsert platform user", "error", err, "hanko_user_id", user.ID)
				http.Error(w, `{"error":"database error"}`, http.StatusInternalServerError)
				return
			}

			slog.Info("platform user upserted", "event", payload.Event, "hanko_user_id", user.ID, "email", user.Email)

		case "user.deleted":
			var user hankoUserData
			if err := json.Unmarshal(payload.Data, &user); err != nil {
				slog.Error("failed to unmarshal user.deleted data", "error", err)
				http.Error(w, `{"error":"invalid user data"}`, http.StatusBadRequest)
				return
			}

			if user.ID == "" {
				slog.Warn("user.deleted event missing user id")
				http.Error(w, `{"error":"missing user id"}`, http.StatusBadRequest)
				return
			}

			// Soft-delete: update plan to 'deleted' and clear email for GDPR compliance.
			_, err := pool.Exec(ctx,
				`UPDATE platform_users
				 SET email = '', display_name = '', plan = 'deleted'
				 WHERE hanko_user_id = $1`,
				user.ID,
			)
			if err != nil {
				slog.Error("failed to soft-delete platform user", "error", err, "hanko_user_id", user.ID)
				http.Error(w, `{"error":"database error"}`, http.StatusInternalServerError)
				return
			}

			slog.Info("platform user marked as deleted", "hanko_user_id", user.ID)

		default:
			slog.Info("ignoring unhandled webhook event", "event", payload.Event)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}
}

// verifyHMAC checks that the given hex-encoded HMAC-SHA256 signature
// matches the expected signature for the body using the shared secret.
func verifyHMAC(body []byte, signature, secret string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expectedSig := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expectedSig), []byte(signature))
}
