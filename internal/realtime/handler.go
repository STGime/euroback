package realtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/gorilla/websocket"
)

const (
	// Connection limits per tenant by plan.
	freeConnectionLimit = 100
	proConnectionLimit  = 10000
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// In production, this should validate the Origin header against
		// allowed origins for the tenant. For now, allow all origins.
		return true
	},
}

// TenantResolver resolves a user subject (Hanko ID) to a tenant ID and plan.
// It is injected by the caller so the realtime package does not depend on the
// auth or tenant packages directly.
type TenantResolver func(ctx context.Context, subject string) (tenantID, plan string, err error)

// HandleWebSocket returns an http.HandlerFunc that upgrades connections to
// WebSocket and manages client lifecycle. Auth is performed via ?token= query
// parameter because WebSocket connections cannot send custom headers.
//
// tokenValidator, if non-nil, validates the token and returns the user subject.
// When nil (dev mode), connections are accepted without authentication.
func HandleWebSocket(hub *Hub, tokenValidator func(token string) (subject string, err error), tenantResolver TenantResolver, devMode bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var tenantID, plan string

		if devMode {
			// Dev mode: accept any connection with a hardcoded tenant.
			tenantID = r.URL.Query().Get("tenant_id")
			if tenantID == "" {
				tenantID = "dev-tenant-001"
			}
			plan = "pro"
			slog.Warn("realtime dev mode: accepting unauthenticated websocket",
				"tenant_id", tenantID,
			)
		} else {
			// Production: validate the token query parameter.
			token := r.URL.Query().Get("token")
			if token == "" {
				http.Error(w, `{"error":"missing token query parameter"}`, http.StatusUnauthorized)
				return
			}

			if tokenValidator == nil {
				http.Error(w, `{"error":"auth not configured"}`, http.StatusInternalServerError)
				return
			}

			subject, err := tokenValidator(token)
			if err != nil {
				slog.Warn("realtime auth failed", "error", err)
				http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
				return
			}

			if tenantResolver == nil {
				http.Error(w, `{"error":"tenant resolver not configured"}`, http.StatusInternalServerError)
				return
			}

			var resolveErr error
			tenantID, plan, resolveErr = tenantResolver(r.Context(), subject)
			if resolveErr != nil {
				slog.Error("realtime: failed to resolve tenant",
					"error", resolveErr,
					"subject", subject,
				)
				http.Error(w, `{"error":"tenant not found"}`, http.StatusNotFound)
				return
			}
		}

		// Enforce per-tenant connection limit.
		limit := freeConnectionLimit
		if plan == "pro" || plan == "enterprise" {
			limit = proConnectionLimit
		}

		if hub.ConnectionCount(tenantID) >= limit {
			slog.Warn("realtime connection limit reached",
				"tenant_id", tenantID,
				"limit", limit,
				"plan", plan,
			)
			http.Error(w, `{"error":"connection limit reached"}`, http.StatusTooManyRequests)
			return
		}

		// Upgrade to WebSocket.
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			slog.Error("websocket upgrade failed", "error", err)
			return
		}

		client := &Client{
			hub:      hub,
			conn:     conn,
			tenantID: tenantID,
			send:     make(chan []byte, 256),
		}

		hub.register <- client

		// Send welcome message.
		welcome, _ := json.Marshal(map[string]interface{}{
			"type":      "welcome",
			"tenant_id": tenantID,
			"message":   "connected to eurobase realtime",
		})
		select {
		case client.send <- welcome:
		default:
		}

		// Start read and write pumps in separate goroutines.
		go writePump(client)
		go readPump(client)
	}
}
