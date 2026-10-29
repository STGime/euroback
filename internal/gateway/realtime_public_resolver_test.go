package gateway

import (
	"context"
	"testing"

	"github.com/eurobase/euroback/internal/query"
)

// The resolver must fail closed (not public) whenever it can't positively
// resolve the table — no schema on the context, or no pool to read the
// catalog with — so it can only ever widen delivery for a confirmed-public
// table. (The positive classification is covered by
// query.TestTableIsPublicReadable_RealtimeClass.)
func TestRealtimePublicResolver_FailsClosed(t *testing.T) {
	r := newRealtimePublicResolver(nil) // no shared pool

	if r(context.Background(), "proj", "todos") {
		t.Error("no schema on context → should be not-public")
	}
	// Schema present but no pool anywhere → still not-public.
	ctx := query.ContextWithSchema(context.Background(), "tenant_x")
	if r(ctx, "proj", "todos") {
		t.Error("no pool → should be not-public")
	}
	// Empty table name → not-public.
	if r(query.ContextWithSchema(context.Background(), "tenant_x"), "proj", "") {
		t.Error("empty table → should be not-public")
	}
}
