// Package query provides the query engine that translates REST API calls
// into parameterized PostgreSQL queries for the Eurobase BaaS data API.
package query

import (
	"net/http"
	"strconv"
	"strings"
)

// QueryParams holds parsed query parameters from the HTTP request.
type QueryParams struct {
	Select  []string      // columns to return
	Filters []Filter      // WHERE clauses
	OrderBy []OrderClause // ORDER BY clauses
	Limit   int           // default 20, max 1000
	Offset  int           // pagination offset
}

// Filter represents a single WHERE clause condition.
type Filter struct {
	Column   string
	Operator string // eq, neq, gt, gte, lt, lte, like, ilike, is, in
	Value    string
}

// OrderClause represents a single ORDER BY clause.
type OrderClause struct {
	Column     string
	Descending bool
}

// supportedOperators maps URL operator names to SQL operators.
var supportedOperators = map[string]string{
	"eq":    "=",
	"neq":   "!=",
	"gt":    ">",
	"gte":   ">=",
	"lt":    "<",
	"lte":   "<=",
	"like":  "LIKE",
	"ilike": "ILIKE",
	"is":    "IS",
	"in":    "IN",
}

// IsValidOperator checks if the given operator name is supported.
func IsValidOperator(op string) bool {
	_, ok := supportedOperators[op]
	return ok
}

// SQLOperator returns the SQL operator for the given name.
func SQLOperator(op string) string {
	return supportedOperators[op]
}

// reservedParams are query parameter keys that are not treated as filters.
var reservedParams = map[string]bool{
	"select": true,
	"order":  true,
	"limit":  true,
	"offset": true,
}

// ParseQueryParams parses URL query parameters in PostgREST/Supabase style.
//
// Examples:
//
//	?select=id,name,email        -> column selection
//	?order=created_at.desc       -> sorting
//	?limit=20&offset=40          -> pagination
//	?name=eq.Stefan              -> exact match
//	?age=gt.25                   -> greater than
//	?status=in.(active,pending)  -> IN clause
func ParseQueryParams(r *http.Request) QueryParams {
	q := r.URL.Query()

	params := QueryParams{
		Limit:  20,
		Offset: 0,
	}

	// Parse select columns.
	if sel := q.Get("select"); sel != "" {
		cols := strings.Split(sel, ",")
		for _, c := range cols {
			c = strings.TrimSpace(c)
			if c != "" {
				params.Select = append(params.Select, c)
			}
		}
	}

	// Parse order clauses.
	if order := q.Get("order"); order != "" {
		parts := strings.Split(order, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			oc := OrderClause{}
			dotIdx := strings.LastIndex(p, ".")
			if dotIdx > 0 {
				col := p[:dotIdx]
				dir := p[dotIdx+1:]
				oc.Column = col
				oc.Descending = strings.EqualFold(dir, "desc")
			} else {
				oc.Column = p
			}
			params.OrderBy = append(params.OrderBy, oc)
		}
	}

	// Parse limit.
	if lim := q.Get("limit"); lim != "" {
		if n, err := strconv.Atoi(lim); err == nil && n > 0 {
			if n > 1000 {
				n = 1000
			}
			params.Limit = n
		}
	}

	// Parse offset.
	if off := q.Get("offset"); off != "" {
		if n, err := strconv.Atoi(off); err == nil && n >= 0 {
			params.Offset = n
		}
	}

	// Parse filters: any query param not in reservedParams is a filter.
	for key, values := range q {
		if reservedParams[key] {
			continue
		}
		if len(values) == 0 {
			continue
		}
		val := values[0]
		f := parseFilter(key, val)
		if f != nil {
			params.Filters = append(params.Filters, *f)
		}
	}

	return params
}

// parseFilter parses a single filter value like "eq.Stefan" or "in.(active,pending)".
func parseFilter(column, value string) *Filter {
	dotIdx := strings.Index(value, ".")
	if dotIdx < 0 {
		// No operator prefix — treat as exact match.
		return &Filter{
			Column:   column,
			Operator: "eq",
			Value:    value,
		}
	}

	op := value[:dotIdx]
	val := value[dotIdx+1:]

	if !IsValidOperator(op) {
		// Unknown operator — treat entire value as eq match.
		return &Filter{
			Column:   column,
			Operator: "eq",
			Value:    value,
		}
	}

	// For "in" operator, strip surrounding parentheses.
	if op == "in" {
		val = strings.TrimPrefix(val, "(")
		val = strings.TrimSuffix(val, ")")
	}

	return &Filter{
		Column:   column,
		Operator: op,
		Value:    val,
	}
}
