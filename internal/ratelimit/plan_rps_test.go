package ratelimit

import (
	"context"
	"errors"
	"testing"
)

func TestLimitFor(t *testing.T) {
	ctx := context.Background()
	fromDB := func(rps int, err error) PlanRPS {
		return func(context.Context, string) (int, error) { return rps, err }
	}
	cases := []struct {
		name   string
		plan   string
		lookup PlanRPS
		want   int
	}{
		{"plan_limits value wins", "team", fromDB(5000, nil), 5000},
		{"plan_limits can differ from fallback", "pro", fromDB(1500, nil), 1500},
		{"no lookup: team uses its own fallback, not free", "team", nil, 5000},
		{"no lookup: legal_team", "legal_team", nil, 5000},
		{"lookup error falls back", "pro", fromDB(0, errors.New("db down")), 1000},
		{"zero from lookup falls back", "pro", fromDB(0, nil), 1000},
		{"unknown plan gets free", "enterprise", nil, 100},
		{"empty plan gets free", "", nil, 100},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := limitFor(ctx, c.plan, c.lookup); got != c.want {
				t.Fatalf("limitFor(%q) = %d, want %d", c.plan, got, c.want)
			}
		})
	}
}
