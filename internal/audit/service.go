// Package audit provides a lightweight audit trail for sensitive platform
// operations. Log entries are written to public.audit_log and surfaced in
// the console's Compliance → Audit Log tab.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Action constants used across the codebase.
const (
	ActionAuthConfigUpdated   = "auth_config.updated"
	ActionAPIKeysRegenerated  = "api_keys.regenerated"
	ActionProjectCreated      = "project.created"
	ActionProjectDeleted      = "project.deleted"
	ActionVaultSecretSet      = "vault.secret_set"
	ActionVaultSecretUpdated  = "vault.secret_updated"
	ActionVaultSecretDeleted  = "vault.secret_deleted"
	ActionVaultSecretAccessed = "vault.secret_accessed"
	ActionVaultRekeyed        = "vault.rekeyed"
	ActionOAuthSecretSet      = "oauth.secret_set"
	ActionDataExported        = "data.exported"
	ActionMemberInvited       = "member.invited"
	ActionMemberRemoved       = "member.removed"
	ActionMemberRoleChanged   = "member.role_changed"
	ActionFunctionCreated     = "function.created"
	ActionFunctionDeleted     = "function.deleted"
	ActionAllowlistAdded      = "allowlist.added"
	ActionAllowlistRemoved    = "allowlist.removed"
	ActionAllowlistEmailed    = "allowlist.emailed"
	// Compliance / DSAR export lifecycle. Closes #100. Tenant +
	// per-user exports run on the platform admin path; self-serve
	// is an end-user-initiated export of their own data.
	ActionExportRequested     = "compliance.export.requested"
	ActionExportSelfRequested = "compliance.export.self_requested"
	ActionExportCompleted     = "compliance.export.completed"
	ActionExportFailed        = "compliance.export.failed"

	// MCP server lifecycle — Closes #165. The platform SQL handler
	// can be called either from the console (full access) or via
	// the MCP server's runSQL tool (read-only by default). The
	// audit-log entry distinguishes them via metadata.source='mcp'
	// so operators can filter for MCP-origin traffic and notice
	// unusual patterns (e.g. a Cursor session that queried
	// `vault_secrets` before the policy gated it).
	ActionMCPSQLExecuted         = "mcp.sql.executed"
	ActionMCPSQLRejectedReadOnly = "mcp.sql.rejected_write_in_readonly"
)

// Entry represents a single audit log row.
type Entry struct {
	ID         string          `json:"id"`
	ProjectID  *string         `json:"project_id"`
	ActorID    *string         `json:"actor_id"`
	ActorEmail string          `json:"actor_email"`
	Action     string          `json:"action"`
	TargetType *string         `json:"target_type"`
	TargetID   *string         `json:"target_id"`
	Metadata   json.RawMessage `json:"metadata"`
	IPAddress  *string         `json:"ip_address"`
	CreatedAt  time.Time       `json:"created_at"`
}

// ListParams controls filtering and pagination for audit log queries.
type ListParams struct {
	Limit  int
	Offset int
	Action string // optional: filter to a specific action
}

// ListResult is the paginated response from List.
type ListResult struct {
	Entries []Entry `json:"entries"`
	Total   int     `json:"total"`
}

// Service provides audit log read/write operations.
type Service struct {
	pool *pgxpool.Pool
}

// NewService creates a new audit service.
func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// Log writes an audit entry. This is fire-and-forget: errors are logged but
// never returned to the caller, because audit failures must not break the
// operation being audited.
func (s *Service) Log(ctx context.Context, projectID, actorID, actorEmail, action string, opts ...LogOption) {
	o := logOptions{}
	for _, fn := range opts {
		fn(&o)
	}

	metaJSON := []byte("{}")
	if o.metadata != nil {
		if b, err := json.Marshal(o.metadata); err == nil {
			metaJSON = b
		}
	}

	// The row is linked into a per-project hash chain (migration 000058).
	// row_hash = SHA-256(prev_hash || canonical(row)), prev_hash = the
	// current chain head's hash. To keep the read-head-then-append atomic
	// under concurrent writes, serialize per-project with a transaction
	// advisory lock; the hash itself is computed in SQL by
	// public.audit_row_hash so it is byte-identical to Verify().
	//
	// Trade-off vs the previous single Exec: audited write latency is now
	// lock-bound per project — a burst of audited operations on the same
	// project serializes. Acceptable because audit writes are low-frequency
	// (sensitive admin actions), not a hot path.
	chainKey := projectID
	if chainKey == "" {
		chainKey = "__global__" // NULL-project rows share one chain
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		slog.Error("audit log write failed", "action", action, "project_id", projectID, "actor", actorEmail, "error", err)
		return
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, chainKey); err != nil {
		slog.Error("audit log write failed (lock)", "action", action, "project_id", projectID, "error", err)
		return
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO public.audit_log
		   (project_id, actor_id, actor_email, action, target_type, target_id, metadata, ip_address, created_at, prev_hash, row_hash)
		 SELECT v.project_id, v.actor_id, v.actor_email, v.action, v.target_type, v.target_id, v.metadata, v.ip_address, v.created_at,
		        p.h,
		        public.audit_row_hash(p.h, v.project_id, v.actor_id, v.actor_email, v.action,
		                              v.target_type, v.target_id, v.metadata, v.ip_address, v.created_at)
		 FROM (
		     SELECT NULLIF($1,'')::uuid AS project_id, NULLIF($2,'')::uuid AS actor_id,
		            $3::text AS actor_email, $4::text AS action, $5::text AS target_type,
		            $6::text AS target_id, $7::jsonb AS metadata, $8::text AS ip_address, now() AS created_at
		 ) v
		 LEFT JOIN LATERAL (
		     SELECT row_hash AS h FROM public.audit_log
		     WHERE project_id IS NOT DISTINCT FROM NULLIF($1,'')::uuid
		     ORDER BY seq DESC LIMIT 1
		 ) p ON true`,
		projectID, actorID, actorEmail, action,
		o.targetType, o.targetID, metaJSON, o.ipAddress,
	)
	if err != nil {
		slog.Error("audit log write failed",
			"action", action,
			"project_id", projectID,
			"actor", actorEmail,
			"error", err,
		)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Error("audit log write failed (commit)", "action", action, "project_id", projectID, "error", err)
	}
}

// List returns paginated audit entries for a project.
func (s *Service) List(ctx context.Context, projectID string, params ListParams) (*ListResult, error) {
	if params.Limit <= 0 || params.Limit > 200 {
		params.Limit = 50
	}

	// Count total.
	countQ := `SELECT count(*) FROM public.audit_log WHERE project_id = $1`
	args := []interface{}{projectID}
	if params.Action != "" {
		countQ += ` AND action = $2`
		args = append(args, params.Action)
	}
	var total int
	if err := s.pool.QueryRow(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count audit entries: %w", err)
	}

	// Fetch page.
	dataQ := `SELECT id, project_id, actor_id, actor_email, action,
	                 target_type, target_id, COALESCE(metadata, '{}'::jsonb),
	                 ip_address, created_at
	          FROM public.audit_log
	          WHERE project_id = $1`
	dataArgs := []interface{}{projectID}
	if params.Action != "" {
		dataQ += ` AND action = $2`
		dataArgs = append(dataArgs, params.Action)
	}
	dataQ += fmt.Sprintf(` ORDER BY created_at DESC LIMIT %d OFFSET %d`, params.Limit, params.Offset)

	rows, err := s.pool.Query(ctx, dataQ, dataArgs...)
	if err != nil {
		return nil, fmt.Errorf("query audit entries: %w", err)
	}
	defer rows.Close()

	entries := make([]Entry, 0)
	for rows.Next() {
		var e Entry
		if err := rows.Scan(
			&e.ID, &e.ProjectID, &e.ActorID, &e.ActorEmail, &e.Action,
			&e.TargetType, &e.TargetID, &e.Metadata, &e.IPAddress, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan audit entry: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit entries: %w", err)
	}

	return &ListResult{Entries: entries, Total: total}, nil
}

// logOptions holds optional fields set via LogOption funcs.
type logOptions struct {
	targetType *string
	targetID   *string
	metadata   map[string]interface{}
	ipAddress  *string
}

// ── Context helpers ──
// The audit service is stored in the request context by the router so that
// any handler can call audit.FromContext(ctx).Log(...) without needing the
// service injected into its constructor.

type ctxKey struct{}
type actorCtxKey struct{}

type actor struct {
	ID    string
	Email string
}

// WithContext stores the audit service in the context.
func WithContext(ctx context.Context, svc *Service) context.Context {
	return context.WithValue(ctx, ctxKey{}, svc)
}

// WithActor stores the authenticated actor's ID and email in the context
// so downstream packages (like query) can emit audit entries without
// importing the auth package. Called by the platform auth middleware
// or the audit injection middleware in the router.
func WithActor(ctx context.Context, id, email string) context.Context {
	return context.WithValue(ctx, actorCtxKey{}, &actor{ID: id, Email: email})
}

// ActorFromContext retrieves the actor ID and email from the context.
// Returns empty strings if not set.
func ActorFromContext(ctx context.Context) (id, email string) {
	a, _ := ctx.Value(actorCtxKey{}).(*actor)
	if a == nil {
		return "", ""
	}
	return a.ID, a.Email
}

// FromContext retrieves the audit service from the context. Returns nil if
// not set — callers must nil-check before calling Log.
func FromContext(ctx context.Context) *Service {
	svc, _ := ctx.Value(ctxKey{}).(*Service)
	return svc
}

// LogOption configures optional fields on an audit log entry.
type LogOption func(*logOptions)

// WithTarget sets the target_type and target_id on the entry (e.g. "api_key", "abc-123").
func WithTarget(targetType, targetID string) LogOption {
	return func(o *logOptions) {
		o.targetType = &targetType
		o.targetID = &targetID
	}
}

// WithMetadata attaches arbitrary key-value pairs to the entry.
func WithMetadata(m map[string]interface{}) LogOption {
	return func(o *logOptions) {
		o.metadata = m
	}
}

// WithIP records the client IP address.
func WithIP(ip string) LogOption {
	return func(o *logOptions) {
		if ip != "" {
			o.ipAddress = &ip
		}
	}
}
