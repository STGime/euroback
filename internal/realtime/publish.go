package realtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

// EventPublisher provides convenience methods for publishing database change
// events to connected WebSocket clients. When the RedisBridge is nil (Redis
// unavailable), events are broadcast only to clients on the local gateway
// instance. When RedisBridge is set, events are published via Redis pub/sub
// for cross-instance delivery.
type EventPublisher struct {
	bridge      *RedisBridge
	hub         *Hub
	publicTable PublicTableResolver
}

// PublicTableResolver reports whether a table is public-readable under RLS
// (query.TableIsPublicReadable, #861): every reader, anon included, can
// SELECT every row, so an owner-less realtime event may go to all
// subscribers. It is called once per event (the gateway backs it with a
// cache). A nil resolver, or a false result, means "not public" — the
// owner filter applies (#860's fail-closed default).
type PublicTableResolver func(ctx context.Context, projectID, table string) bool

// NewEventPublisher creates an EventPublisher. Both bridge and hub may be nil
// for no-op behaviour.
func NewEventPublisher(bridge *RedisBridge, hub *Hub) *EventPublisher {
	return &EventPublisher{
		bridge: bridge,
		hub:    hub,
	}
}

// WithPublicResolver sets the resolver used to decide whether a table's
// owner-less rows may be broadcast to everyone (#861). Without it the
// publisher never classifies a table public, so owner-less rows reach
// only the service role (#860).
func (p *EventPublisher) WithPublicResolver(r PublicTableResolver) *EventPublisher {
	p.publicTable = r
	return p
}

// PublishInsert broadcasts an INSERT event for the given table and record.
func (p *EventPublisher) PublishInsert(ctx context.Context, projectID, table string, record map[string]interface{}) error {
	return p.publish(ctx, projectID, table, "INSERT", record, nil)
}

// PublishUpdate broadcasts an UPDATE event with both old and new records.
func (p *EventPublisher) PublishUpdate(ctx context.Context, projectID, table string, record, oldRecord map[string]interface{}) error {
	return p.publish(ctx, projectID, table, "UPDATE", record, oldRecord)
}

// PublishDelete broadcasts a DELETE event for the removed record.
func (p *EventPublisher) PublishDelete(ctx context.Context, projectID, table string, record map[string]interface{}) error {
	return p.publish(ctx, projectID, table, "DELETE", record, nil)
}

// publish is the internal method that handles both local and Redis broadcasting.
func (p *EventPublisher) publish(ctx context.Context, projectID, table, eventType string, record, oldRecord map[string]interface{}) error {
	if p.hub == nil {
		slog.Debug("realtime publisher: hub is nil, skipping event",
			"project_id", projectID,
			"table", table,
			"type", eventType,
		)
		return nil
	}

	event := Event{
		Channel:   "db:" + table,
		Type:      eventType,
		Record:    record,
		OldRecord: oldRecord,
		Timestamp: time.Now().UTC(),
	}

	payload, err := json.Marshal(event)
	if err != nil {
		slog.Error("realtime publisher: failed to marshal event",
			"error", err,
			"project_id", projectID,
			"table", table,
		)
		return err
	}

	// Closes #108. Detect the row's owner once on the publisher side
	// (same row for every subscriber), then carry it on the broadcast
	// envelope. The hub filters per-subscriber based on it. DELETE
	// events get the owner from oldRecord because record is just
	// {id: ...} on the DELETE path (see HandleTableDelete in
	// internal/query/handler.go).
	ownerUserID := ownerUserIDFromRow(record)
	if ownerUserID == "" && oldRecord != nil {
		ownerUserID = ownerUserIDFromRow(oldRecord)
	}

	// #861: is the table public-readable under RLS? If so the hub delivers
	// the row to every subscriber; otherwise the owner filter applies.
	// Resolved once here (same answer for every subscriber) and carried on
	// the broadcast envelope.
	public := false
	if p.publicTable != nil {
		public = p.publicTable(ctx, projectID, table)
	}

	// If Redis bridge is available, publish via Redis for cross-instance delivery.
	if p.bridge != nil {
		if err := p.bridge.Publish(ctx, projectID, table, eventType, record, oldRecord, ownerUserID, public); err != nil {
			slog.Error("realtime publisher: redis publish failed, falling back to local",
				"error", err,
				"project_id", projectID,
				"table", table,
			)
			// Fall through to local broadcast.
		} else {
			// Redis will deliver the event back to this instance via Subscribe,
			// so we don't need to broadcast locally.
			return nil
		}
	}

	// Local-only broadcast (no Redis, or Redis publish failed).
	p.hub.Broadcast(projectID, event.Channel, payload, ownerUserID, public)

	slog.Debug("realtime publisher: broadcast locally",
		"project_id", projectID,
		"channel", event.Channel,
		"type", eventType,
	)

	return nil
}

// ownerColumns are the column names that, if present on a row, identify
// the end-user that owns it. Order matters when more than one is
// present: first match wins. The same four names are what the
// `owner_access` RLS preset looks for at table-creation time (see
// internal/query/ddl_handler.go), so realtime filtering and REST RLS
// agree on which column scopes a row.
var ownerColumns = []string{"user_id", "owner_id", "created_by", "uploaded_by"}

// ownerUserIDFromRow returns the row's owner-column value, or "" if
// the row has no recognised owner column. Used by the hub to decide
// whether to deliver a realtime event to a given subscriber.
//
// Matches the most common RLS pattern (the owner_access preset) without
// re-running policy SQL on every event, which would be O(events ×
// subscribers) Postgres queries at fan-out time. When it returns ""
// (a table scoped by a column it doesn't recognise — tenant_id, org_id,
// a custom column — or a table with no owner concept) the hub does NOT
// fall back to broadcasting: it withholds the row from end-users and
// anon subscribers and delivers only to the service role (shouldReceive
// fails closed). Correctly delivering genuinely public owner-less tables
// to end-users again is a policy-aware follow-up.
func ownerUserIDFromRow(record map[string]interface{}) string {
	if record == nil {
		return ""
	}
	for _, col := range ownerColumns {
		v, ok := record[col]
		if !ok || v == nil {
			continue
		}
		switch x := v.(type) {
		case string:
			if x != "" {
				return x
			}
		case []byte:
			if len(x) > 0 {
				return string(x)
			}
		default:
			// UUIDs sometimes come back as types that implement
			// fmt.Stringer (pgtype.UUID); covered by the
			// default-branch test in publish_test.go.
			if s, ok := v.(interface{ String() string }); ok {
				if str := s.String(); str != "" {
					return str
				}
			}
		}
	}
	return ""
}
