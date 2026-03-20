package query

import (
	"net/http/httptest"
	"testing"
)

func TestParseSelectParam(t *testing.T) {
	t.Helper()

	req := httptest.NewRequest("GET", "/users?select=id,name,email", nil)
	params := ParseQueryParams(req)

	expected := []string{"id", "name", "email"}
	if len(params.Select) != len(expected) {
		t.Fatalf("expected %d select columns, got %d", len(expected), len(params.Select))
	}
	for i, col := range expected {
		if params.Select[i] != col {
			t.Errorf("select[%d]: expected %q, got %q", i, col, params.Select[i])
		}
	}
}

func TestParseOrderParam(t *testing.T) {
	req := httptest.NewRequest("GET", "/users?order=created_at.desc", nil)
	params := ParseQueryParams(req)

	if len(params.OrderBy) != 1 {
		t.Fatalf("expected 1 order clause, got %d", len(params.OrderBy))
	}
	oc := params.OrderBy[0]
	if oc.Column != "created_at" {
		t.Errorf("expected order column %q, got %q", "created_at", oc.Column)
	}
	if !oc.Descending {
		t.Error("expected Descending=true, got false")
	}
}

func TestParseOrderParamAsc(t *testing.T) {
	req := httptest.NewRequest("GET", "/users?order=name.asc", nil)
	params := ParseQueryParams(req)

	if len(params.OrderBy) != 1 {
		t.Fatalf("expected 1 order clause, got %d", len(params.OrderBy))
	}
	oc := params.OrderBy[0]
	if oc.Column != "name" {
		t.Errorf("expected order column %q, got %q", "name", oc.Column)
	}
	if oc.Descending {
		t.Error("expected Descending=false, got true")
	}
}

func TestParseLimitOffset(t *testing.T) {
	req := httptest.NewRequest("GET", "/users?limit=50&offset=10", nil)
	params := ParseQueryParams(req)

	if params.Limit != 50 {
		t.Errorf("expected Limit=50, got %d", params.Limit)
	}
	if params.Offset != 10 {
		t.Errorf("expected Offset=10, got %d", params.Offset)
	}
}

func TestParseLimitMax(t *testing.T) {
	req := httptest.NewRequest("GET", "/users?limit=5000", nil)
	params := ParseQueryParams(req)

	if params.Limit != 1000 {
		t.Errorf("expected Limit capped at 1000, got %d", params.Limit)
	}
}

func TestParseLimitDefault(t *testing.T) {
	req := httptest.NewRequest("GET", "/users", nil)
	params := ParseQueryParams(req)

	if params.Limit != 20 {
		t.Errorf("expected default Limit=20, got %d", params.Limit)
	}
	if params.Offset != 0 {
		t.Errorf("expected default Offset=0, got %d", params.Offset)
	}
}

func TestParseFilterEq(t *testing.T) {
	req := httptest.NewRequest("GET", "/users?name=eq.Stefan", nil)
	params := ParseQueryParams(req)

	if len(params.Filters) != 1 {
		t.Fatalf("expected 1 filter, got %d", len(params.Filters))
	}
	f := params.Filters[0]
	if f.Column != "name" {
		t.Errorf("expected filter column %q, got %q", "name", f.Column)
	}
	if f.Operator != "eq" {
		t.Errorf("expected filter operator %q, got %q", "eq", f.Operator)
	}
	if f.Value != "Stefan" {
		t.Errorf("expected filter value %q, got %q", "Stefan", f.Value)
	}
}

func TestParseFilterIn(t *testing.T) {
	req := httptest.NewRequest("GET", "/users?status=in.(active,pending)", nil)
	params := ParseQueryParams(req)

	if len(params.Filters) != 1 {
		t.Fatalf("expected 1 filter, got %d", len(params.Filters))
	}
	f := params.Filters[0]
	if f.Column != "status" {
		t.Errorf("expected filter column %q, got %q", "status", f.Column)
	}
	if f.Operator != "in" {
		t.Errorf("expected filter operator %q, got %q", "in", f.Operator)
	}
	if f.Value != "active,pending" {
		t.Errorf("expected filter value %q, got %q", "active,pending", f.Value)
	}
}

func TestParseFilterGt(t *testing.T) {
	req := httptest.NewRequest("GET", "/users?age=gt.25", nil)
	params := ParseQueryParams(req)

	if len(params.Filters) != 1 {
		t.Fatalf("expected 1 filter, got %d", len(params.Filters))
	}
	f := params.Filters[0]
	if f.Column != "age" {
		t.Errorf("expected filter column %q, got %q", "age", f.Column)
	}
	if f.Operator != "gt" {
		t.Errorf("expected filter operator %q, got %q", "gt", f.Operator)
	}
	if f.Value != "25" {
		t.Errorf("expected filter value %q, got %q", "25", f.Value)
	}
}

func TestParseFilterLte(t *testing.T) {
	req := httptest.NewRequest("GET", "/users?score=lte.100", nil)
	params := ParseQueryParams(req)

	if len(params.Filters) != 1 {
		t.Fatalf("expected 1 filter, got %d", len(params.Filters))
	}
	f := params.Filters[0]
	if f.Column != "score" {
		t.Errorf("expected filter column %q, got %q", "score", f.Column)
	}
	if f.Operator != "lte" {
		t.Errorf("expected filter operator %q, got %q", "lte", f.Operator)
	}
	if f.Value != "100" {
		t.Errorf("expected filter value %q, got %q", "100", f.Value)
	}
}

func TestParseMultipleFilters(t *testing.T) {
	req := httptest.NewRequest("GET", "/users?name=eq.Stefan&age=gt.25", nil)
	params := ParseQueryParams(req)

	if len(params.Filters) != 2 {
		t.Fatalf("expected 2 filters, got %d", len(params.Filters))
	}

	// Filters may be in any order due to map iteration.
	filterMap := make(map[string]Filter)
	for _, f := range params.Filters {
		filterMap[f.Column] = f
	}

	nameFilter, ok := filterMap["name"]
	if !ok {
		t.Fatal("expected filter on column 'name'")
	}
	if nameFilter.Operator != "eq" || nameFilter.Value != "Stefan" {
		t.Errorf("unexpected name filter: %+v", nameFilter)
	}

	ageFilter, ok := filterMap["age"]
	if !ok {
		t.Fatal("expected filter on column 'age'")
	}
	if ageFilter.Operator != "gt" || ageFilter.Value != "25" {
		t.Errorf("unexpected age filter: %+v", ageFilter)
	}
}

func TestParseNoFilterForReservedParams(t *testing.T) {
	req := httptest.NewRequest("GET", "/users?select=id&order=name.asc&limit=10&offset=5", nil)
	params := ParseQueryParams(req)

	if len(params.Filters) != 0 {
		t.Errorf("expected 0 filters for reserved params, got %d", len(params.Filters))
	}
}
