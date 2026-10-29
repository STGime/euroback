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
//
// endUserID + service let the hub decide per-event whether a given
// client is entitled to receive a row (fail closed; see shouldReceive).
// The two fields are populated by HandleWebSocket from the Authorize
// callback:
//   - service=true → eb_sk_* apikey or platform JWT. The client sees
//     every row; matches the "service role" semantics RLS already uses
//     on the REST side.
//   - endUserID != "" → end-user JWT. The client sees only rows whose
//     ownership column (user_id/owner_id/created_by/uploaded_by) equals
//     this value.
//   - both empty → eb_pk_* apikey, no signed-in user. The client sees
//     nothing from owner-scoped or owner-less tables; an anon subscriber
//     can never be proven to be allowed a row, so nothing is delivered.
type Client struct {
	hub           *Hub
	conn          *websocket.Conn
	tenantID      string
	endUserID     string
	service       bool
	denied        map[string]bool // tables whose db: channels are refused
	subscriptions []string        // channels like "db:users", "db:users:uuid", "storage:bucket"
	send          chan []byte
	mu            sync.Mutex // protects subscriptions
}

// shouldReceive reports whether this client is entitled to a row.
//
// public is true when the table is public-readable under RLS — every
// reader, anonymous included, can SELECT every row (RLS disabled, or a
// permissive `USING (true)` SELECT policy with nothing restrictive
// narrowing it; see query.TableIsPublicReadable, #861). Such a row goes
// to everyone, matching REST.
//
// Otherwise the owner filter applies (fail closed, #860): ownerUserID is
// the row's recognised owner-column value (user_id / owner_id /
// created_by / uploaded_by), or "" when the row has no recognised owner
// column. Realtime must never deliver a row the subscriber could not read
// via REST, and off the public path we can only prove that for the
// service role (RLS bypass) and for an end-user who is the row's
// recognised owner. Everything else — an owner-less row we can't
// attribute, an anonymous subscriber, or a row owned by someone else — is
// withheld. (A table that is owner-scoped by a column realtime doesn't
// recognise, or authenticated-read, is not yet classified as public and
// so stays owner-filtered/withheld — tracked in #861.)
func (c *Client) shouldReceive(public bool, ownerUserID string) bool {
	if public || c.service {
		return true
	}
	return ownerUserID != "" && c.endUserID != "" && c.endUserID == ownerUserID
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
	key         string // "tenantID:channel"
	event       []byte // JSON-encoded Event
	ownerUserID string // "" if the row has no recognised owner column; otherwise the value of user_id/owner_id/created_by/uploaded_by
	// public is true when the table is public-readable under RLS (#861):
	// the row goes to every subscriber, owner column notwithstanding.
	public bool
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
				if !client.shouldReceive(msg.public, msg.ownerUserID) {
					// Not provably visible to this client (not the
					// owner, anon, or an owner-less row we can't
					// attribute). Silent skip — equivalent to a
					// SELECT that returned no rows under RLS.
					continue
				}
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

// Broadcast sends a JSON-encoded event to clients subscribed to the
// key. When public is true the table is public-readable under RLS and the
// event goes to every subscriber. Otherwise ownerUserID filters delivery:
// non-empty delivers to that owner (and the service role); "" (no
// recognised owner column) delivers only to service clients, not
// end-users or anon (fail closed — see shouldReceive).
func (h *Hub) Broadcast(tenantID, channel string, data []byte, ownerUserID string, public bool) {
	h.broadcast <- &broadcastMsg{
		key:         tenantID + ":" + channel,
		event:       data,
		ownerUserID: ownerUserID,
		public:      public,
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
