// Package auth provides authentication middleware and helpers for the Eurobase platform.
package auth

import "context"

// contextKey is a type-safe key for context values in this package.
type contextKey int

const claimsKey contextKey = iota

// Claims holds the authenticated user's identity extracted from a JWT.
type Claims struct {
	Subject       string // Platform user ID (UUID from platform_users.id)
	Email         string // User email
	IsSuperadmin  bool   // Platform-wide admin. Granted via platform_users.is_superadmin.
}

// ClaimsFromContext extracts the authenticated claims from the request context.
// Returns nil, false if no claims are present (i.e. unauthenticated request).
func ClaimsFromContext(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(claimsKey).(*Claims)
	return c, ok
}

// contextWithClaims stores the claims in the given context (internal use).
func contextWithClaims(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, claimsKey, c)
}

// ContextWithClaims stores the claims in the given context.
// Exported for use in test auth middleware.
func ContextWithClaims(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, claimsKey, c)
}
