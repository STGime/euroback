package breach

import (
	"strings"
	"testing"
	"time"
)

func TestBuildUserWhere_EmptyQuery(t *testing.T) {
	w, args := buildUserWhere(SubjectQuery{})
	if w != "" {
		t.Errorf("expected empty WHERE, got %q", w)
	}
	if len(args) != 0 {
		t.Errorf("expected no args, got %d", len(args))
	}
}

func TestBuildUserWhere_AllFilters(t *testing.T) {
	from := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC)
	upFrom := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	upUntil := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	q := SubjectQuery{
		CreatedFrom:  &from,
		CreatedUntil: &until,
		UpdatedFrom:  &upFrom,
		UpdatedUntil: &upUntil,
		UserIDs:      []string{"id-1", "id-2"},
	}
	w, args := buildUserWhere(q)

	// All five predicates should be present, positional args numbered 1..5
	// in declaration order.
	for _, want := range []string{
		"created_at >= $1",
		"created_at <= $2",
		"updated_at >= $3",
		"updated_at <= $4",
		"id::text = ANY($5)",
	} {
		if !strings.Contains(w, want) {
			t.Errorf("WHERE missing %q\ngot: %s", want, w)
		}
	}
	if len(args) != 5 {
		t.Fatalf("expected 5 args, got %d", len(args))
	}
	// Times are normalised to UTC by the builder so callers in other zones
	// don't change the query plan.
	if got := args[0].(time.Time); got.Location() != time.UTC {
		t.Errorf("arg 0 not UTC: %v", got)
	}
	if got, ok := args[4].([]string); !ok || len(got) != 2 {
		t.Errorf("arg 4 not []string of len 2: %T %v", args[4], args[4])
	}
}

func TestBuildUserWhere_OnlyUserIDs(t *testing.T) {
	w, args := buildUserWhere(SubjectQuery{UserIDs: []string{"a"}})
	if w != " WHERE id::text = ANY($1)" {
		t.Errorf("unexpected WHERE: %q", w)
	}
	if len(args) != 1 {
		t.Errorf("expected 1 arg, got %d", len(args))
	}
}
