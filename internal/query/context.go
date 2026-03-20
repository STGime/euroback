package query

import "context"

// schemaContextKey is a type-safe key for the tenant schema in request context.
type schemaContextKey struct{}

// ContextWithSchema stores the tenant schema name in the context.
func ContextWithSchema(ctx context.Context, schema string) context.Context {
	return context.WithValue(ctx, schemaContextKey{}, schema)
}

// SchemaFromContext extracts the tenant schema name from the context.
// Returns empty string if not present.
func SchemaFromContext(ctx context.Context) string {
	s, _ := ctx.Value(schemaContextKey{}).(string)
	return s
}
