package realtime

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 54 * time.Second

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer.
	maxMessageSize = 4096
)

// clientMessage is the JSON structure received from the WebSocket client.
type clientMessage struct {
	Action  string `json:"action"`  // "subscribe" or "unsubscribe"
	Channel string `json:"channel"` // e.g. "db:users"
}

// readPump reads messages from the WebSocket connection and handles
// subscribe/unsubscribe actions. It runs in its own goroutine per client.
func readPump(client *Client) {
	defer func() {
		client.hub.unregister <- client
		client.conn.Close()
	}()

	client.conn.SetReadLimit(maxMessageSize)
	if err := client.conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
		slog.Error("failed to set read deadline", "error", err)
		return
	}
	client.conn.SetPongHandler(func(string) error {
		return client.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		_, message, err := client.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Warn("unexpected websocket close",
					"tenant_id", client.tenantID,
					"error", err,
				)
			}
			return
		}

		var msg clientMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			slog.Debug("invalid client message",
				"tenant_id", client.tenantID,
				"error", err,
			)
			// Send error back to client.
			errResp, _ := json.Marshal(map[string]string{
				"error": "invalid message format",
			})
			select {
			case client.send <- errResp:
			default:
			}
			continue
		}

		if msg.Channel == "" {
			errResp, _ := json.Marshal(map[string]string{
				"error": "channel is required",
			})
			select {
			case client.send <- errResp:
			default:
			}
			continue
		}

		switch msg.Action {
		case "subscribe":
			client.hub.Subscribe(client, msg.Channel)
			ack, _ := json.Marshal(map[string]string{
				"action":  "subscribed",
				"channel": msg.Channel,
			})
			select {
			case client.send <- ack:
			default:
			}

		case "unsubscribe":
			client.hub.Unsubscribe(client, msg.Channel)
			ack, _ := json.Marshal(map[string]string{
				"action":  "unsubscribed",
				"channel": msg.Channel,
			})
			select {
			case client.send <- ack:
			default:
			}

		default:
			slog.Debug("unknown client action",
				"tenant_id", client.tenantID,
				"action", msg.Action,
			)
			errResp, _ := json.Marshal(map[string]string{
				"error": "unknown action: " + msg.Action,
			})
			select {
			case client.send <- errResp:
			default:
			}
		}
	}
}

// writePump pumps messages from the send channel to the WebSocket connection.
// It runs in its own goroutine per client.
func writePump(client *Client) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		client.conn.Close()
	}()

	for {
		select {
		case message, ok := <-client.send:
			if err := client.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
				slog.Error("failed to set write deadline", "error", err)
				return
			}
			if !ok {
				// Hub closed the channel.
				_ = client.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := client.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				slog.Warn("websocket write error",
					"tenant_id", client.tenantID,
					"error", err,
				)
				return
			}

		case <-ticker.C:
			if err := client.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
				slog.Error("failed to set write deadline for ping", "error", err)
				return
			}
			if err := client.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
