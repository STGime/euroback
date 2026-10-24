package enduser

import (
	"context"
	"time"

	"github.com/eurobase/euroback/internal/events"
)

// emitUserEvent hands an end-user account change to the project's
// webhooks (#845). Called after the change has committed. The payload is
// the account's identity, never credentials.
func emitUserEvent(ctx context.Context, projectID, eventType string, u User, provider string) {
	data := map[string]any{
		"id":         u.ID,
		"email":      u.Email,
		"phone":      u.Phone,
		"provider":   provider,
		"created_at": u.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if u.CreatedAt.IsZero() {
		delete(data, "created_at")
	}
	events.Emit(ctx, events.Event{ProjectID: projectID, Type: eventType, Data: data})
}
