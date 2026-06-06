package query

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestFilterShape_DropsValues is the regression guard for the review finding:
// filter VALUES (routinely personal data) must never be written into the
// access log's target_keys — only the column + operator shape.
func TestFilterShape_DropsValues(t *testing.T) {
	filters := []Filter{
		{Column: "email", Operator: "eq", Value: "john@example.com"},
		{Column: "phone", Operator: "eq", Value: "+4915123456789"},
	}

	shape := filterShape(filters)

	b, err := json.Marshal(shape)
	if err != nil {
		t.Fatalf("marshal shape: %v", err)
	}
	got := string(b)

	// No predicate value may appear anywhere in the serialized shape.
	for _, leak := range []string{"john@example.com", "+4915123456789"} {
		if strings.Contains(got, leak) {
			t.Errorf("filter value %q leaked into target_keys: %s", leak, got)
		}
	}

	// Column + operator (the legitimate "query shape") must be preserved.
	if len(shape) != 2 {
		t.Fatalf("len(shape) = %d, want 2", len(shape))
	}
	if shape[0]["column"] != "email" || shape[0]["op"] != "eq" {
		t.Errorf("shape[0] = %+v, want {column:email, op:eq}", shape[0])
	}
	// Stable lowercase keys only — no Go-exported field names.
	if strings.Contains(got, "Column") || strings.Contains(got, "Value") || strings.Contains(got, "Operator") {
		t.Errorf("unexpected exported field names in shape: %s", got)
	}
}
