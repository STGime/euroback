package realtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Closes #108. End-to-end tests for owner-column-scoped delivery:
// publisher emits a row with a user_id, the hub fans it out to clients
// based on each client's resolved identity rather than to everyone.

// ── Pure unit tests for the owner-detection helper ───────────────────────────

func TestOwnerUserIDFromRow(t *testing.T) {
	cases := []struct {
		name string
		row  map[string]interface{}
		want string
	}{
		{"nil row", nil, ""},
		{"empty row", map[string]interface{}{}, ""},
		{"unrelated columns only", map[string]interface{}{"id": "x", "title": "t"}, ""},
		{"user_id string", map[string]interface{}{"user_id": "u-1"}, "u-1"},
		{"owner_id string", map[string]interface{}{"owner_id": "o-2"}, "o-2"},
		{"created_by string", map[string]interface{}{"created_by": "c-3"}, "c-3"},
		{"uploaded_by string", map[string]interface{}{"uploaded_by": "up-4"}, "up-4"},
		// order of priority: user_id wins
		{"user_id wins over owner_id", map[string]interface{}{"owner_id": "o", "user_id": "u"}, "u"},
		// empty value treated as no owner
		{"empty string user_id", map[string]interface{}{"user_id": ""}, ""},
		{"nil user_id", map[string]interface{}{"user_id": nil}, ""},
		// bytes path (Postgres sometimes ships UUID as []byte through generic interfaces)
		{"bytes user_id", map[string]interface{}{"user_id": []byte("u-b")}, "u-b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ownerUserIDFromRow(c.row); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// fakeStringer simulates pgtype.UUID's String() method to make sure
// non-string owner values still resolve via the stringer fallback.
type fakeStringer string

func (f fakeStringer) String() string { return string(f) }

func TestOwnerUserIDFromRow_StringerFallback(t *testing.T) {
	row := map[string]interface{}{"user_id": fakeStringer("uuid-via-stringer")}
	if got := ownerUserIDFromRow(row); got != "uuid-via-stringer" {
		t.Errorf("got %q, want %q", got, "uuid-via-stringer")
	}
}

// ── shouldReceive unit tests ────────────────────────────────────────────────

func TestClient_ShouldReceive(t *testing.T) {
	cases := []struct {
		name        string
		endUserID   string
		service     bool
		ownerUserID string
		want        bool
	}{
		// No owner column → broadcast to all (preserves v0.4 behaviour
		// for tables that aren't owner-scoped: lookup tables, public
		// feeds, etc.)
		{"any client, no owner", "", false, "", true},
		{"end-user client, no owner", "u-1", false, "", true},
		{"service client, no owner", "", true, "", true},

		// Owner column present → service sees all
		{"service client, owner present", "", true, "owner-x", true},

		// Owner column present → anon (no end-user id, not service) sees nothing
		{"anon client, owner present", "", false, "owner-x", false},

		// Owner column present → end-user matches
		{"end-user matches owner", "u-1", false, "u-1", true},
		{"end-user does not match owner", "u-1", false, "u-2", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cli := &Client{endUserID: c.endUserID, service: c.service}
			if got := cli.shouldReceive(c.ownerUserID); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// ── End-to-end delivery tests via the actual hub ────────────────────────────

// rtTestClient is a thin helper around a subscribed WebSocket.
type rtTestClient struct {
	c *websocket.Conn
}

func (r *rtTestClient) readWithin(t *testing.T, d time.Duration) ([]byte, error) {
	t.Helper()
	_ = r.c.SetReadDeadline(time.Now().Add(d))
	_, msg, err := r.c.ReadMessage()
	return msg, err
}

func (r *rtTestClient) close() { _ = r.c.Close() }

// startServerForOwnerTest brings up a hub and a server with an
// Authorize callback that decodes the test token into an identity.
// Tokens used in these tests:
//
//	"svc-*"  → service=true
//	"anon-*" → anon (no service, no end-user)
//	"user:<id>" → end-user JWT with subject=<id>
//
// This keeps each test focused on the hub filter behaviour rather than
// the real router's JWT validation, which is exercised by
// handler_authz_test.go.
func startServerForOwnerTest(t *testing.T) (*Hub, string, func()) {
	t.Helper()
	hub := NewHub()
	go hub.Run()
	originOK := func(_ *http.Request) bool { return true }
	authorize := func(_ context.Context, token, qpID string) (AuthorizedClient, error) {
		ac := AuthorizedClient{ProjectID: qpID, Plan: "pro"}
		switch {
		case strings.HasPrefix(token, "svc-"):
			ac.Service = true
		case strings.HasPrefix(token, "user:"):
			ac.EndUserID = strings.TrimPrefix(token, "user:")
			// no Service flag — end-user identity
		case strings.HasPrefix(token, "anon-"):
			// no identity at all (e.g. eb_pk_* anon apikey)
		default:
			return AuthorizedClient{}, ErrUnauthorized
		}
		return ac, nil
	}
	srv := httptest.NewServer(HandleWebSocket(hub, authorize, originOK, false))
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1)
	return hub, wsURL, srv.Close
}

func dialSubscribed(t *testing.T, wsURL, token, projectID, channel string) *rtTestClient {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial(
		wsURL+"?token="+token+"&project_id="+projectID,
		http.Header{"Origin": []string{"http://test"}},
	)
	if err != nil {
		t.Fatalf("dial %s: %v", token, err)
	}
	if _, _, err := c.ReadMessage(); err != nil {
		t.Fatalf("welcome %s: %v", token, err)
	}
	if err := c.WriteJSON(map[string]string{"action": "subscribe", "channel": channel}); err != nil {
		t.Fatalf("subscribe %s: %v", token, err)
	}
	if _, _, err := c.ReadMessage(); err != nil {
		t.Fatalf("subscribe ack %s: %v", token, err)
	}
	return &rtTestClient{c: c}
}

// TestRealtime_OwnerScoped_EndUserMatch — the classic case the bug
// affected: two end-users A and B subscribe to the same table; an
// INSERT with user_id=A reaches A only, not B.
func TestRealtime_OwnerScoped_EndUserMatch(t *testing.T) {
	hub, wsURL, stop := startServerForOwnerTest(t)
	defer stop()

	a := dialSubscribed(t, wsURL, "user:A", "p-1", "db:notes")
	defer a.close()
	b := dialSubscribed(t, wsURL, "user:B", "p-1", "db:notes")
	defer b.close()

	pub := NewEventPublisher(nil, hub)
	if err := pub.PublishInsert(context.Background(), "p-1", "notes", map[string]interface{}{
		"id":      "n-1",
		"user_id": "A",
		"body":    "alice-private",
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	msg, err := a.readWithin(t, 1*time.Second)
	if err != nil {
		t.Fatalf("A did not receive own row: %v", err)
	}
	if !strings.Contains(string(msg), "alice-private") {
		t.Errorf("A received wrong row: %s", msg)
	}

	if _, err := b.readWithin(t, 250*time.Millisecond); err == nil {
		t.Fatal("B received A's row (regression of #108 — cross-user leak)")
	}
}

// TestRealtime_OwnerScoped_AnonDoesNotReceive — anon clients (eb_pk_*
// apikey, no end-user JWT) get nothing from owner-scoped rows.
func TestRealtime_OwnerScoped_AnonDoesNotReceive(t *testing.T) {
	hub, wsURL, stop := startServerForOwnerTest(t)
	defer stop()

	anon := dialSubscribed(t, wsURL, "anon-pk", "p-1", "db:notes")
	defer anon.close()

	pub := NewEventPublisher(nil, hub)
	_ = pub.PublishInsert(context.Background(), "p-1", "notes", map[string]interface{}{
		"id":      "n-1",
		"user_id": "A",
		"body":    "alice-private",
	})

	if _, err := anon.readWithin(t, 250*time.Millisecond); err == nil {
		t.Fatal("anon received an owner-scoped row (regression of #108)")
	}
}

// TestRealtime_ServiceSeesEverything — eb_sk_* / platform JWT identity
// keeps the v0.4 "everything" view. Important for the console's
// realtime tab and any server-side worker subscribing to a project.
func TestRealtime_ServiceSeesEverything(t *testing.T) {
	hub, wsURL, stop := startServerForOwnerTest(t)
	defer stop()

	svc := dialSubscribed(t, wsURL, "svc-sk", "p-1", "db:notes")
	defer svc.close()

	pub := NewEventPublisher(nil, hub)
	_ = pub.PublishInsert(context.Background(), "p-1", "notes", map[string]interface{}{
		"id":      "n-1",
		"user_id": "A",
		"body":    "row-by-A",
	})

	msg, err := svc.readWithin(t, 1*time.Second)
	if err != nil {
		t.Fatalf("service client missed row: %v", err)
	}
	if !strings.Contains(string(msg), "row-by-A") {
		t.Errorf("service got wrong payload: %s", msg)
	}
}

// TestRealtime_NoOwnerColumn_StillBroadcasts — tables without an owner
// column keep broadcasting to all subscribers (lookup tables, public
// feeds, anything where rows aren't user-scoped). Without this we'd
// silently break every realtime use case that isn't strictly
// owner-scoped.
func TestRealtime_NoOwnerColumn_StillBroadcasts(t *testing.T) {
	hub, wsURL, stop := startServerForOwnerTest(t)
	defer stop()

	end := dialSubscribed(t, wsURL, "user:A", "p-1", "db:countries")
	defer end.close()
	anon := dialSubscribed(t, wsURL, "anon-pk", "p-1", "db:countries")
	defer anon.close()

	pub := NewEventPublisher(nil, hub)
	_ = pub.PublishInsert(context.Background(), "p-1", "countries", map[string]interface{}{
		"id":   "fr",
		"name": "France",
	})

	for label, cli := range map[string]*rtTestClient{"end-user": end, "anon": anon} {
		msg, err := cli.readWithin(t, 1*time.Second)
		if err != nil {
			t.Errorf("%s did not receive non-owner-scoped row: %v", label, err)
			continue
		}
		if !strings.Contains(string(msg), "France") {
			t.Errorf("%s got wrong row: %s", label, msg)
		}
	}
}

// TestRealtime_DeleteUsesOldRecordOwner — DELETE handlers pass a
// near-empty record (just {id: ...}), so the owner has to come from
// old_record. If we missed the old_record fallback the delete would
// silently broadcast to every subscriber.
func TestRealtime_DeleteUsesOldRecordOwner(t *testing.T) {
	hub, wsURL, stop := startServerForOwnerTest(t)
	defer stop()

	a := dialSubscribed(t, wsURL, "user:A", "p-1", "db:notes")
	defer a.close()
	b := dialSubscribed(t, wsURL, "user:B", "p-1", "db:notes")
	defer b.close()

	pub := NewEventPublisher(nil, hub)

	// PublishDelete currently has only one record arg; the update
	// path (which carries an old record) is what most consumers care
	// about. Exercise PublishUpdate: new record has the owner col
	// blanked, but old_record has user_id=A.
	_ = pub.PublishUpdate(context.Background(), "p-1", "notes",
		map[string]interface{}{"id": "n-1"}, // new (no owner — simulates a column removal/cleanup)
		map[string]interface{}{"id": "n-1", "user_id": "A"}, // old
	)

	if _, err := a.readWithin(t, 1*time.Second); err != nil {
		t.Errorf("A missed UPDATE where old.user_id matched: %v", err)
	}
	if _, err := b.readWithin(t, 250*time.Millisecond); err == nil {
		t.Error("B received UPDATE that should have been A-only (old_record owner fallback regression)")
	}
}
