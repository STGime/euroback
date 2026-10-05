package tenant

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// DeleteProject must cancel billing first and FAIL CLOSED: if the
// subscription can't be canceled, it aborts before tearing anything down,
// so a live Mollie subscription is never orphaned by the FK cascade. The
// cancel runs before any pool use, so this needs no database.
func TestDeleteProject_CancelFailsClosed(t *testing.T) {
	svc := &TenantService{}
	called := false
	svc.SetSubscriptionCanceller(func(ctx context.Context, projectID string) error {
		called = true
		if projectID != "proj-1" {
			t.Fatalf("canceller got project %q, want proj-1", projectID)
		}
		return errors.New("mollie unavailable")
	})

	err := svc.DeleteProject(context.Background(), "proj-1")
	if !called {
		t.Fatal("subscription canceller was not called")
	}
	if err == nil || !strings.Contains(err.Error(), "cancel subscription before delete") {
		t.Fatalf("want cancel-fail-closed error, got %v", err)
	}
}
