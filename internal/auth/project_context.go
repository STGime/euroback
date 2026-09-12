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
	Slug       string // Used for storage bucket naming; never read from request headers.
	JWTSecret  string
	KeyType    string // "public" or "secret"
	Plan       string // billing plan: "free", "pro", "team"
	AuthConfig json.RawMessage

	// HasDedicatedDB is true when the project has a live
	// project_databases row (Team-tier and above; migration 000083).
	// Populated by ResolveAPIKey when a matching row exists in
	// state IN (provisioning, active, restoring).
	//
	// M2 uses this only as a flag on the profile / project APIs
	// (console shows "Dedicated database" badge, backup/PITR
	// endpoints gate on CheckDedicatedDB). Full pool routing to the
	// dedicated instance lands as a follow-up (see M2.5 in the plan).
	HasDedicatedDB bool

	// ProjectDatabaseID is the primary key of the live
	// project_databases row when HasDedicatedDB is true. Nil
	// otherwise. Used by future pool-cache lookups.
	ProjectDatabaseID *string

	// MaintenanceMode mirrors public.projects.maintenance_mode
	// (added in migration 000117). When true, SDK requests for this
	// project are refused with HTTP 503 by
	// gateway.MaintenanceModeMiddleware. Set during Free/Pro → Team
	// tier upgrade cutover (~30 seconds) and by ops for emergencies.
	// Populated by the same SELECT that resolves the project, so no
	// extra DB hit per request.
	MaintenanceMode bool
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
