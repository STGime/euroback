// Package realtime provides a WebSocket engine that pushes database change
// events to connected clients. Cross-instance fan-out uses Redis pub/sub;
// when Redis is unavailable, events are delivered only to clients connected
// to the local gateway instance (graceful degradation).
package realtime

import (
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Client represents a single WebSocket connection.
type Client struct {
	hub           *Hub
	conn          *websocket.Conn
	tenantID      string
	subscriptions []string // channels like "db:users", "db:users:uuid", "storage:bucket"
	send          chan []byte
	mu            sync.Mutex // protects subscriptions
}

// Event represents a database change event broadcast to clients.
type Event struct {
	Channel   string                 `json:"channel"`              // e.g. "db:users"
	Type      string                 `json:"type"`                 // INSERT, UPDATE, DELETE
	Record    map[string]interface{} `json:"record"`               // new/current row
	OldRecord map[string]interface{} `json:"old_record,omitempty"` // previous row (UPDATE/DELETE)
	Timestamp time.Time              `json:"timestamp"`
}

// Hub manages WebSocket connections per tenant and broadcasts events to
// matching subscribers. It is goroutine-safe.
type Hub struct {
	// clients maps "tenant:channel" keys to the set of subscribed clients.
	clients map[string]map[*Client]bool

	// tenantConns tracks per-tenant connection counts for limit enforcement.
	tenantConns map[string]int

	register   chan *Client
	unregister chan *Client
	broadcast  chan *broadcastMsg

	mu sync.RWMutex
}

// broadcastMsg carries an event together with the target key so the Run loop
// can route it to the correct set of clients.
type broadcastMsg struct {
	key   string // "tenantID:channel"
	event []byte // JSON-encoded Event
}

// NewHub creates a Hub ready to be started with Run().
func NewHub() *Hub {
	return &Hub{
		clients:     make(map[string]map[*Client]bool),
		tenantConns: make(map[string]int),
		register:    make(chan *Client),
		unregister:  make(chan *Client),
		broadcast:   make(chan *broadcastMsg, 256),
	}
}

// Run processes register, unregister, and broadcast messages. It should be
// started in its own goroutine.
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.tenantConns[client.tenantID]++
			h.mu.Unlock()
			slog.Debug("client registered",
				"tenant_id", client.tenantID,
				"connections", h.tenantConns[client.tenantID],
			)

		case client := <-h.unregister:
			h.removeClient(client)

		case msg := <-h.broadcast:
			h.mu.RLock()
			clients := h.clients[msg.key]
			h.mu.RUnlock()

			for client := range clients {
				select {
				case client.send <- msg.event:
				default:
					// Client send buffer full — drop it.
					slog.Warn("dropping slow client",
						"tenant_id", client.tenantID,
						"key", msg.key,
					)
					h.removeClient(client)
				}
			}
		}
	}
}

// Subscribe adds a client to the given channel.
func (h *Hub) Subscribe(client *Client, channel string) {
	key := client.tenantID + ":" + channel

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.clients[key] == nil {
		h.clients[key] = make(map[*Client]bool)
	}
	h.clients[key][client] = true

	client.mu.Lock()
	client.subscriptions = append(client.subscriptions, channel)
	client.mu.Unlock()

	slog.Debug("client subscribed", "tenant_id", client.tenantID, "channel", channel)
}

// Unsubscribe removes a client from the given channel.
func (h *Hub) Unsubscribe(client *Client, channel string) {
	key := client.tenantID + ":" + channel

	h.mu.Lock()
	defer h.mu.Unlock()

	if clients, ok := h.clients[key]; ok {
		delete(clients, client)
		if len(clients) == 0 {
			delete(h.clients, key)
		}
	}

	client.mu.Lock()
	for i, ch := range client.subscriptions {
		if ch == channel {
			client.subscriptions = append(client.subscriptions[:i], client.subscriptions[i+1:]...)
			break
		}
	}
	client.mu.Unlock()

	slog.Debug("client unsubscribed", "tenant_id", client.tenantID, "channel", channel)
}

// Broadcast sends a JSON-encoded event to all clients subscribed to the key.
func (h *Hub) Broadcast(tenantID, channel string, data []byte) {
	h.broadcast <- &broadcastMsg{
		key:   tenantID + ":" + channel,
		event: data,
	}
}

// ConnectionCount returns the number of active connections for a tenant.
func (h *Hub) ConnectionCount(tenantID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.tenantConns[tenantID]
}

// removeClient unsubscribes the client from all channels and closes its
// send channel. Safe to call multiple times.
func (h *Hub) removeClient(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	client.mu.Lock()
	subs := make([]string, len(client.subscriptions))
	copy(subs, client.subscriptions)
	client.subscriptions = nil
	client.mu.Unlock()

	for _, channel := range subs {
		key := client.tenantID + ":" + channel
		if clients, ok := h.clients[key]; ok {
			delete(clients, client)
			if len(clients) == 0 {
				delete(h.clients, key)
			}
		}
	}

	h.tenantConns[client.tenantID]--
	if h.tenantConns[client.tenantID] <= 0 {
		delete(h.tenantConns, client.tenantID)
	}

	// Close send channel (writePump will exit).
	select {
	case _, ok := <-client.send:
		if ok {
			close(client.send)
		}
	default:
		close(client.send)
	}

	slog.Debug("client unregistered", "tenant_id", client.tenantID)
}
