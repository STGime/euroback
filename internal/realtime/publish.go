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
	bridge *RedisBridge
	hub    *Hub
}

// NewEventPublisher creates an EventPublisher. Both bridge and hub may be nil
// for no-op behaviour.
func NewEventPublisher(bridge *RedisBridge, hub *Hub) *EventPublisher {
	return &EventPublisher{
		bridge: bridge,
		hub:    hub,
	}
}

// PublishInsert broadcasts an INSERT event for the given table and record.
func (p *EventPublisher) PublishInsert(ctx context.Context, tenantID, table string, record map[string]interface{}) error {
	return p.publish(ctx, tenantID, table, "INSERT", record, nil)
}

// PublishUpdate broadcasts an UPDATE event with both old and new records.
func (p *EventPublisher) PublishUpdate(ctx context.Context, tenantID, table string, record, oldRecord map[string]interface{}) error {
	return p.publish(ctx, tenantID, table, "UPDATE", record, oldRecord)
}

// PublishDelete broadcasts a DELETE event for the removed record.
func (p *EventPublisher) PublishDelete(ctx context.Context, tenantID, table string, record map[string]interface{}) error {
	return p.publish(ctx, tenantID, table, "DELETE", record, nil)
}

// publish is the internal method that handles both local and Redis broadcasting.
func (p *EventPublisher) publish(ctx context.Context, tenantID, table, eventType string, record, oldRecord map[string]interface{}) error {
	if p.hub == nil {
		slog.Debug("realtime publisher: hub is nil, skipping event",
			"tenant_id", tenantID,
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
			"tenant_id", tenantID,
			"table", table,
		)
		return err
	}

	// If Redis bridge is available, publish via Redis for cross-instance delivery.
	if p.bridge != nil {
		if err := p.bridge.Publish(ctx, tenantID, table, eventType, record, oldRecord); err != nil {
			slog.Error("realtime publisher: redis publish failed, falling back to local",
				"error", err,
				"tenant_id", tenantID,
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
	p.hub.Broadcast(tenantID, event.Channel, payload)

	slog.Debug("realtime publisher: broadcast locally",
		"tenant_id", tenantID,
		"channel", event.Channel,
		"type", eventType,
	)

	return nil
}
