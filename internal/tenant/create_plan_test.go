package tenant

import (
	"net/http"
	"testing"
)

// POST /v1/tenants never creates a paid project: Pro goes through
// checkout, unknown plans are refused.
func TestCreatePlanAllowed(t *testing.T) {
	for plan, want := range map[string]int{
		"free": 0, "team": 0, "legal_team": 0,
		"pro": http.StatusPaymentRequired, "PRO": http.StatusBadRequest,
		"enterprise": http.StatusBadRequest, "x": http.StatusBadRequest,
	} {
		if got, _ := createPlanAllowed(plan); got != want {
			t.Errorf("createPlanAllowed(%q) = %d, want %d", plan, got, want)
		}
	}
}
