// Package query provides the query engine that translates REST API calls
// into parameterized PostgreSQL queries for the Eurobase BaaS data API.
package query

import (
	"net/http"
	"strconv"
	"strings"
)

// Relation represents a related table to embed via LEFT JOIN.
type Relation struct {
	Table   string   // related table name, e.g. "customer"
	Columns []string // columns to select from the related table ("*" for all)
}

// QueryParams holds parsed query parameters from the HTTP request.
type QueryParams struct {
	Select    []string      // columns to return
	Filters   []Filter      // WHERE clauses
	OrderBy   []OrderClause // ORDER BY clauses
	Limit     int           // default 20, max 1000
	Offset    int           // pagination offset
	Aggregate string        // aggregate function, e.g. "count", "sum:price"
	Relations []Relation    // related tables to embed via LEFT JOIN
}

// Filter represents a single WHERE clause condition.
type Filter struct {
	Column   string
	Operator string // eq, neq, gt, gte, lt, lte, like, ilike, is, in, fts
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
	"fts":   "@@",
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
	"select":    true,
	"order":     true,
	"limit":     true,
	"offset":    true,
	"aggregate": true,
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

	// Parse aggregate.
	if agg := q.Get("aggregate"); agg != "" {
		params.Aggregate = agg
	}

	// Parse select columns and relations.
	if sel := q.Get("select"); sel != "" {
		cols := splitSelectParam(sel)
		for _, c := range cols {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			// Check for relation syntax: "table(col1,col2)" or "table(*)".
			if rel := parseRelation(c); rel != nil {
				params.Relations = append(params.Relations, *rel)
			} else {
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

// splitSelectParam splits a select parameter string by commas, but respects
// parentheses so that "id,total,customer(name,email)" splits correctly into
// ["id", "total", "customer(name,email)"].
func splitSelectParam(sel string) []string {
	var parts []string
	depth := 0
	start := 0
	for i, ch := range sel {
		switch ch {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, sel[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, sel[start:])
	return parts
}

// parseRelation checks if a select entry has relation syntax like "customer(name,email)"
// and returns a Relation if so, or nil otherwise.
func parseRelation(entry string) *Relation {
	lparen := strings.Index(entry, "(")
	if lparen < 0 {
		return nil
	}
	rparen := strings.LastIndex(entry, ")")
	if rparen < lparen {
		return nil
	}

	table := strings.TrimSpace(entry[:lparen])
	if table == "" {
		return nil
	}

	colStr := entry[lparen+1 : rparen]
	var cols []string
	for _, c := range strings.Split(colStr, ",") {
		c = strings.TrimSpace(c)
		if c != "" {
			cols = append(cols, c)
		}
	}
	if len(cols) == 0 {
		return nil
	}

	return &Relation{
		Table:   table,
		Columns: cols,
	}
}
