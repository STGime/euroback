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

type projectIDContextKey struct{}

// ContextWithProjectID stores the tenant project UUID in the context.
// Set alongside ContextWithSchema by the tenant middleware so handlers
// can broadcast realtime events keyed on the project UUID (which is
// what WebSocket subscribers also key on — see internal/realtime).
//
// Closes #62: without this, the SDK runtime path only had the schema
// name, and the realtime publisher was broadcasting on schema name
// while subscribers were listening on project UUID — no events ever
// matched.
func ContextWithProjectID(ctx context.Context, projectID string) context.Context {
	return context.WithValue(ctx, projectIDContextKey{}, projectID)
}

// ProjectIDFromContext extracts the project UUID from the context.
// Returns empty string if not present.
func ProjectIDFromContext(ctx context.Context) string {
	s, _ := ctx.Value(projectIDContextKey{}).(string)
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

type endUserEmailKey struct{}

// ContextWithEndUserEmail stores the end-user email in the context.
func ContextWithEndUserEmail(ctx context.Context, email string) context.Context {
	return context.WithValue(ctx, endUserEmailKey{}, email)
}

// EndUserEmailFromContext extracts the end-user email from the context.
func EndUserEmailFromContext(ctx context.Context) string {
	s, _ := ctx.Value(endUserEmailKey{}).(string)
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

type developerRoleKey struct{}

// WithDeveloperRole flags the request as platform-authenticated developer
// traffic. The query engine reads this flag and, when set, runs
// `SET LOCAL ROLE eurobase_migrator` at the start of every transaction
// so DDL on tenant schemas works against migrator-owned tables and
// objects created during the request are owned by the migrator —
// keeping ownership uniform with CI-applied migrations.
//
// Only the platform-route middleware should set this. The SDK runtime
// path leaves it unset.
func WithDeveloperRole(ctx context.Context) context.Context {
	return context.WithValue(ctx, developerRoleKey{}, true)
}

// DeveloperRoleFromContext reports whether the developer-role flag is set.
func DeveloperRoleFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(developerRoleKey{}).(bool)
	return v
}
