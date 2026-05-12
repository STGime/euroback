package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gorilla/websocket"
)

const (
	// Connection limits per project by plan.
	freeConnectionLimit = 100
	proConnectionLimit  = 10000
)

// ErrUnauthorized is returned by an Authorize callback when the token
// is missing or invalid. The realtime handler maps it to HTTP 401.
var ErrUnauthorized = errors.New("realtime: unauthorized")

// ErrForbidden is returned when the token is valid but the caller is
// not a member of the requested project. Maps to HTTP 403.
var ErrForbidden = errors.New("realtime: forbidden")

// Authorize validates the caller's token for the requested project and
// returns the project's plan plus the resolved project UUID. The
// resolved value is what the hub keys connections on — it lets an
// apikey token derive the project server-side so the caller can omit
// the project_id query param.
//
// requestedProjectID is the value of the `?project_id=` query param,
// or "" if absent. Implementations may either return that value back
// (after validating membership), or substitute one derived from the
// token (apikey path). Returning a different projectID than what the
// caller requested when a request was made is treated as ErrForbidden
// upstream.
//
// Return ErrUnauthorized for a bad token, ErrForbidden for a valid
// token without access; any other error maps to 500.
type Authorize func(ctx context.Context, token, requestedProjectID string) (projectID, plan string, err error)

// HandleWebSocket returns an http.HandlerFunc that upgrades connections to
// WebSocket on /v1/realtime and manages client lifecycle. Closes #62 —
// the connection is now keyed on the requested project_id (UUID),
// matching what the SDK publisher emits, so events actually reach
// subscribers.
//
// Query parameters:
//
//	token       — JWT (platform or end-user). Required outside dev mode.
//	project_id  — project UUID the client wants to subscribe to. Required.
//
// devMode skips auth and accepts any project_id (or none — falls back
// to a fixed dev project). Production fences in cmd/gateway/main.go
// fail closed if dev mode leaks out.
//
// originChecker rejects upgrades from disallowed origins; nil in dev
// mode accepts any origin. Closes #47.
func HandleWebSocket(hub *Hub, authorize Authorize, originChecker func(*http.Request) bool, devMode bool) http.HandlerFunc {
	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			if devMode {
				return true
			}
			if originChecker == nil {
				return false
			}
			return originChecker(r)
		},
	}

	return func(w http.ResponseWriter, r *http.Request) {
		requestedProjectID := r.URL.Query().Get("project_id")
		var projectID, plan string

		if devMode {
			projectID = requestedProjectID
			if projectID == "" {
				projectID = "dev-project-001"
			}
			plan = "pro"
			slog.Warn("realtime dev mode: accepting unauthenticated websocket",
				"project_id", projectID,
			)
		} else {
			token := r.URL.Query().Get("token")
			if token == "" {
				http.Error(w, `{"error":"missing token query parameter"}`, http.StatusUnauthorized)
				return
			}
			if authorize == nil {
				http.Error(w, `{"error":"auth not configured"}`, http.StatusInternalServerError)
				return
			}

			var err error
			projectID, plan, err = authorize(r.Context(), token, requestedProjectID)
			if err != nil {
				switch {
				case errors.Is(err, ErrUnauthorized):
					slog.Warn("realtime auth failed", "project_id", requestedProjectID)
					http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
				case errors.Is(err, ErrForbidden):
					slog.Warn("realtime forbidden", "project_id", requestedProjectID)
					http.Error(w, `{"error":"not a member of this project"}`, http.StatusForbidden)
				default:
					slog.Error("realtime authorize failed", "error", err, "project_id", requestedProjectID)
					http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
				}
				return
			}
			if projectID == "" {
				slog.Error("realtime authorize returned empty project_id", "requested", requestedProjectID)
				http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
				return
			}
		}

		// Enforce per-project connection limit.
		limit := freeConnectionLimit
		if plan == "pro" || plan == "enterprise" {
			limit = proConnectionLimit
		}
		if hub.ConnectionCount(projectID) >= limit {
			slog.Warn("realtime connection limit reached",
				"project_id", projectID,
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
			tenantID: projectID, // hub still uses "tenantID" internally; semantically it's projectID now
			send:     make(chan []byte, 256),
		}

		hub.register <- client

		welcome, _ := json.Marshal(map[string]interface{}{
			"type":       "welcome",
			"project_id": projectID,
			"message":    "connected to eurobase realtime",
		})
		select {
		case client.send <- welcome:
		default:
		}

		go writePump(client)
		go readPump(client)
	}
}
