package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisBridge connects the Hub to Redis pub/sub for cross-instance event
// fan-out. When multiple gateway instances are running, a change published
// by one instance is delivered to clients connected to any instance.
type RedisBridge struct {
	client *redis.Client
	hub    *Hub
}

// NewRedisBridge creates a RedisBridge, parsing the Redis URL and verifying
// connectivity.
func NewRedisBridge(redisURL string, hub *Hub) (*RedisBridge, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}

	client := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	slog.Info("realtime redis bridge connected", "addr", opts.Addr)

	return &RedisBridge{
		client: client,
		hub:    hub,
	}, nil
}

// Subscribe listens on the Redis pub/sub pattern "rt:tenant:*" and routes
// incoming messages to the correct Hub clients. This method blocks until
// the context is cancelled.
func (b *RedisBridge) Subscribe(ctx context.Context) {
	pubsub := b.client.PSubscribe(ctx, "rt:tenant:*")
	defer pubsub.Close()

	slog.Info("realtime redis subscription started", "pattern", "rt:tenant:*")

	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			slog.Info("realtime redis subscription stopped")
			return
		case msg, ok := <-ch:
			if !ok {
				slog.Warn("realtime redis channel closed")
				return
			}
			b.handleMessage(msg)
		}
	}
}

// handleMessage parses a Redis pub/sub message and broadcasts it to Hub
// clients. The Redis channel format is "rt:tenant:{tenantID}:{table}".
func (b *RedisBridge) handleMessage(msg *redis.Message) {
	// Parse channel: "rt:tenant:{tenantID}:{table}"
	// Strip the "rt:" prefix, then split into parts.
	stripped := strings.TrimPrefix(msg.Channel, "rt:")
	parts := strings.SplitN(stripped, ":", 3)
	if len(parts) < 3 {
		slog.Warn("invalid realtime redis channel format",
			"channel", msg.Channel,
		)
		return
	}

	tenantID := parts[1]
	table := parts[2]
	channel := "db:" + table

	slog.Debug("received redis realtime event",
		"tenant_id", tenantID,
		"table", table,
		"channel", channel,
	)

	b.hub.Broadcast(tenantID, channel, []byte(msg.Payload))
}

// Publish publishes an event to Redis so that all gateway instances can
// broadcast it to their connected clients.
func (b *RedisBridge) Publish(ctx context.Context, tenantID, table, eventType string, record, oldRecord map[string]interface{}) error {
	event := Event{
		Channel:   "db:" + table,
		Type:      eventType,
		Record:    record,
		OldRecord: oldRecord,
		Timestamp: time.Now().UTC(),
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal realtime event: %w", err)
	}

	redisChannel := fmt.Sprintf("rt:tenant:%s:%s", tenantID, table)

	if err := b.client.Publish(ctx, redisChannel, payload).Err(); err != nil {
		return fmt.Errorf("publish to redis: %w", err)
	}

	slog.Debug("published realtime event to redis",
		"channel", redisChannel,
		"type", eventType,
	)

	return nil
}

// Close closes the underlying Redis client.
func (b *RedisBridge) Close() error {
	return b.client.Close()
}
