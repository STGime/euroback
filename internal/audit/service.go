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
	ActionAuthConfigUpdated  = "auth_config.updated"
	ActionAPIKeysRegenerated = "api_keys.regenerated"
	ActionProjectCreated     = "project.created"
	ActionProjectDeleted     = "project.deleted"
	ActionVaultSecretSet     = "vault.secret_set"
	ActionVaultSecretDeleted = "vault.secret_deleted"
	ActionOAuthSecretSet     = "oauth.secret_set"
	ActionDataExported       = "data.exported"
	ActionMemberInvited      = "member.invited"
	ActionMemberRemoved      = "member.removed"
	ActionMemberRoleChanged  = "member.role_changed"
	ActionFunctionCreated    = "function.created"
	ActionFunctionDeleted    = "function.deleted"
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

	_, err := s.pool.Exec(ctx,
		`INSERT INTO public.audit_log
		   (project_id, actor_id, actor_email, action, target_type, target_id, metadata, ip_address)
		 VALUES
		   (NULLIF($1,'')::uuid, NULLIF($2,'')::uuid, $3, $4, $5, $6, $7, $8)`,
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

// WithContext stores the audit service in the context.
func WithContext(ctx context.Context, svc *Service) context.Context {
	return context.WithValue(ctx, ctxKey{}, svc)
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
