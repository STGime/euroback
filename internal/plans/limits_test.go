package plans

import (
	"encoding/json"
	"testing"
)

// TestPlanLimits_JSONRoundtrip pins the JSON wire shape for the
// PlanLimits struct, including the new DSARConsoleUI field added in
// #251. The console reads this struct by name, so a typo in the
// `json:` tag would silently mis-render the soft-gate state without
// the Go side noticing.
func TestPlanLimits_JSONRoundtrip(t *testing.T) {
	in := PlanLimits{
		Plan:              "pro",
		DSARConsoleUI:     true,
		EdgeFunctionLimit: 25,
		CustomTemplates:   true,
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// The JSON must contain dsar_console_ui as a top-level key with
	// the literal `true`. If a future refactor renames the tag, this
	// test catches it before the console silently misses the gate.
	wireFragments := []string{
		`"plan":"pro"`,
		`"dsar_console_ui":true`,
		`"edge_function_limit":25`,
		`"custom_templates":true`,
	}
	s := string(b)
	for _, frag := range wireFragments {
		if !contains(s, frag) {
			t.Errorf("missing %q in JSON: %s", frag, s)
		}
	}

	var out PlanLimits
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !out.DSARConsoleUI {
		t.Error("DSARConsoleUI did not survive JSON roundtrip")
	}
	if out.Plan != "pro" {
		t.Errorf("Plan = %q, want pro", out.Plan)
	}
}

// TestPlanLimits_FreeDefault confirms the zero value of DSARConsoleUI
// is `false` (matches the migration's DEFAULT and the "surprise-gate a
// new tier rather than surprise-ungate one" reasoning).
func TestPlanLimits_FreeDefault(t *testing.T) {
	var l PlanLimits
	if l.DSARConsoleUI {
		t.Error("zero-value DSARConsoleUI must be false (safer default)")
	}
}

// TestIsPaidPlan pins the paid-tier set behind the #329 SMS gate.
// Fail-closed is the whole point: an unknown plan value (schema
// drift, typo, future tier added without updating this helper)
// must be treated as unpaid so a hypothetical Free-tier project
// with a corrupted `plan` column can't accidentally unlock SMS.
func TestIsPaidPlan(t *testing.T) {
	paid := []string{"pro", "team", "legal_team"}
	unpaid := []string{"free", "", "PRO", "trial", "enterprise", "unknown"}
	for _, p := range paid {
		if !IsPaidPlan(p) {
			t.Errorf("IsPaidPlan(%q) = false, want true", p)
		}
	}
	for _, p := range unpaid {
		if IsPaidPlan(p) {
			t.Errorf("IsPaidPlan(%q) = true, want false (fail-closed)", p)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
