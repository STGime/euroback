package auth

import (
	"context"
	"encoding/json"
)

type projectContextKey struct{}

// ProjectContext holds the resolved project info from an API key lookup.
type ProjectContext struct {
	ProjectID  string
	SchemaName string
	JWTSecret  string
	KeyType    string // "public" or "secret"
	AuthConfig json.RawMessage
}

// ContextWithProject stores a ProjectContext in the given context.
func ContextWithProject(ctx context.Context, pc *ProjectContext) context.Context {
	return context.WithValue(ctx, projectContextKey{}, pc)
}

// ProjectFromContext retrieves the ProjectContext from the given context.
func ProjectFromContext(ctx context.Context) (*ProjectContext, bool) {
	pc, ok := ctx.Value(projectContextKey{}).(*ProjectContext)
	return pc, ok
}
