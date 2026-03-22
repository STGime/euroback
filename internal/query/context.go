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

type endUserIDKey struct{}

// ContextWithEndUserID stores the end-user ID in the context for RLS.
func ContextWithEndUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, endUserIDKey{}, userID)
}

// EndUserIDFromContext extracts the end-user ID from the context.
func EndUserIDFromContext(ctx context.Context) string {
	s, _ := ctx.Value(endUserIDKey{}).(string)
	return s
}

type apiKeyTypeKey struct{}

// ContextWithKeyType stores the API key type ("public" or "secret") in context.
func ContextWithKeyType(ctx context.Context, keyType string) context.Context {
	return context.WithValue(ctx, apiKeyTypeKey{}, keyType)
}

// KeyTypeFromContext extracts the API key type from the context.
func KeyTypeFromContext(ctx context.Context) string {
	s, _ := ctx.Value(apiKeyTypeKey{}).(string)
	return s
}
