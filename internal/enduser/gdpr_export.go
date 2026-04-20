package enduser

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	edb "github.com/eurobase/euroback/internal/db"
	"github.com/eurobase/euroback/internal/query"
	"github.com/eurobase/euroback/internal/ratelimit"
	"github.com/eurobase/euroback/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// gdprExportLimit caps per-actor exports to catch runaway scripts without
// blocking legitimate admin use. Data exports are expensive and sensitive.
const (
	gdprExportLimit  = 5
	gdprExportWindow = time.Hour
)

// GDPRExportResponse is the Article 15 subject access request response.
type GDPRExportResponse struct {
	ExportVersion string                            `json:"export_version"`
	ExportedAt    time.Time                         `json:"exported_at"`
	ProjectID     string                            `json:"project_id"`
	User          GDPRUserProfile                   `json:"user"`
	Identities    []GDPRIdentity                    `json:"identities"`
	StorageObjects []GDPRStorageObject              `json:"storage_objects"`
	RefreshTokens []GDPRRefreshToken                `json:"refresh_tokens"`
	UserData      map[string][]map[string]any       `json:"user_data"`
}

// GDPRUserProfile is the user profile section of the export (no password hash).
type GDPRUserProfile struct {
	ID               string         `json:"id"`
	Email            *string        `json:"email"`
	Phone            *string        `json:"phone,omitempty"`
	DisplayName      *string        `json:"display_name"`
	AvatarURL        *string        `json:"avatar_url,omitempty"`
	Metadata         map[string]any `json:"metadata"`
	Provider         *string        `json:"provider,omitempty"`
	EmailConfirmedAt *time.Time     `json:"email_confirmed_at,omitempty"`
	PhoneConfirmedAt *time.Time     `json:"phone_confirmed_at,omitempty"`
	LastSignInAt     *time.Time     `json:"last_sign_in_at,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

// GDPRIdentity is an OAuth identity linked to the user.
type GDPRIdentity struct {
	Provider       string         `json:"provider"`
	ProviderUserID string         `json:"provider_user_id"`
	IdentityData   map[string]any `json:"identity_data"`
	LastSignInAt   *time.Time     `json:"last_sign_in_at,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
}

// GDPRStorageObject is file metadata (not file contents).
type GDPRStorageObject struct {
	Key         string     `json:"key"`
	ContentType *string    `json:"content_type"`
	SizeBytes   *int64     `json:"size_bytes"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// GDPRRefreshToken is token metadata (not the hash).
type GDPRRefreshToken struct {
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Revoked   bool       `json:"revoked"`
}

// HandleGDPRExport returns a handler for GET /platform/projects/{id}/users/{userId}/export.
// Requires admin or owner role on the project; rate-limited per actor.
func HandleGDPRExport(pool *pgxpool.Pool, limiter *ratelimit.RateLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		userID := chi.URLParam(r, "userId")
		schema := query.SchemaFromContext(r.Context())
		if schema == "" || userID == "" {
			http.Error(w, `{"error":"missing project or user context"}`, http.StatusBadRequest)
			return
		}

		// Gate to admin/owner — viewer and developer can't export PII.
		claims, _, ok := tenant.RequireRole(w, r, pool, projectID, "admin")
		if !ok {
			return
		}

		// Per-actor rate limit so a compromised admin token can't be used to
		// bulk-enumerate users.
		if limiter != nil {
			if ratelimit.CheckAuthRate(limiter, w, r.Context(), "gdpr_export",
				claims.Subject, gdprExportLimit, gdprExportWindow) {
				return
			}
		}

		ctx := r.Context()
		qs := quoteIdent(schema)

		// Run all export queries inside a single service-role transaction
		// so RLS policies on the tenant schema permit reading rows owned
		// by the target user (platform admin is acting on their behalf).
		var profile *GDPRUserProfile
		var identities []GDPRIdentity
		var storageObjs []GDPRStorageObject
		var tokens []GDPRRefreshToken
		var userData map[string][]map[string]any
		profileErr := edb.RunAsService(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
			p, e := exportUserProfile(ctx, tx, qs, userID)
			if e != nil {
				return e
			}
			profile = p
			if i, err := exportIdentities(ctx, tx, qs, userID); err != nil {
				slog.Error("gdpr export: identities", "error", err, "user_id", userID)
				identities = []GDPRIdentity{}
			} else {
				identities = i
			}
			if s, err := exportStorageObjects(ctx, tx, qs, userID); err != nil {
				slog.Error("gdpr export: storage objects", "error", err, "user_id", userID)
				storageObjs = []GDPRStorageObject{}
			} else {
				storageObjs = s
			}
			if t, err := exportRefreshTokens(ctx, tx, qs, userID); err != nil {
				slog.Error("gdpr export: refresh tokens", "error", err, "user_id", userID)
				tokens = []GDPRRefreshToken{}
			} else {
				tokens = t
			}
			if d, err := exportUserOwnedRows(ctx, tx, schema, userID); err != nil {
				slog.Error("gdpr export: user data", "error", err, "user_id", userID)
				userData = map[string][]map[string]any{}
			} else {
				userData = d
			}
			return nil
		})
		if profileErr != nil {
			slog.Error("gdpr export: user profile", "error", profileErr, "user_id", userID)
			http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
			return
		}

		resp := GDPRExportResponse{
			ExportVersion:  "1.0",
			ExportedAt:     time.Now().UTC(),
			ProjectID:      projectID,
			User:           *profile,
			Identities:     identities,
			StorageObjects: storageObjs,
			RefreshTokens:  tokens,
			UserData:       userData,
		}

		// Audit trail.
		if auditSvc := audit.FromContext(ctx); auditSvc != nil {
			actorID, actorEmail := audit.ActorFromContext(ctx)
			auditSvc.Log(ctx, projectID, actorID, actorEmail,
				audit.ActionDataExported,
				audit.WithTarget("user", userID),
				audit.WithMetadata(map[string]any{"user_email": profile.Email}),
				audit.WithIP(r.RemoteAddr))
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func exportUserProfile(ctx context.Context, tx pgx.Tx, qs, userID string) (*GDPRUserProfile, error) {
	q := fmt.Sprintf(
		`SELECT id, email, phone, display_name, avatar_url, metadata, provider,
		        email_confirmed_at, phone_confirmed_at, last_sign_in_at, created_at, updated_at
		 FROM %s.users WHERE id = $1`, qs)

	var p GDPRUserProfile
	var metaBytes []byte
	err := tx.QueryRow(ctx, q, userID).Scan(
		&p.ID, &p.Email, &p.Phone, &p.DisplayName, &p.AvatarURL, &metaBytes,
		&p.Provider, &p.EmailConfirmedAt, &p.PhoneConfirmedAt, &p.LastSignInAt,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if metaBytes != nil {
		_ = json.Unmarshal(metaBytes, &p.Metadata)
	}
	if p.Metadata == nil {
		p.Metadata = map[string]any{}
	}
	return &p, nil
}

func exportIdentities(ctx context.Context, tx pgx.Tx, qs, userID string) ([]GDPRIdentity, error) {
	q := fmt.Sprintf(
		`SELECT provider, provider_user_id, identity_data, last_sign_in_at, created_at
		 FROM %s.user_identities WHERE user_id = $1 ORDER BY created_at`, qs)

	rows, err := tx.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []GDPRIdentity
	for rows.Next() {
		var i GDPRIdentity
		var dataBytes []byte
		if err := rows.Scan(&i.Provider, &i.ProviderUserID, &dataBytes, &i.LastSignInAt, &i.CreatedAt); err != nil {
			return nil, err
		}
		if dataBytes != nil {
			_ = json.Unmarshal(dataBytes, &i.IdentityData)
		}
		if i.IdentityData == nil {
			i.IdentityData = map[string]any{}
		}
		out = append(out, i)
	}
	if out == nil {
		out = []GDPRIdentity{}
	}
	return out, nil
}

func exportStorageObjects(ctx context.Context, tx pgx.Tx, qs, userID string) ([]GDPRStorageObject, error) {
	q := fmt.Sprintf(
		`SELECT key, content_type, size_bytes, metadata, created_at
		 FROM %s.storage_objects WHERE uploaded_by = $1 ORDER BY created_at`, qs)

	rows, err := tx.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []GDPRStorageObject
	for rows.Next() {
		var o GDPRStorageObject
		var metaBytes []byte
		if err := rows.Scan(&o.Key, &o.ContentType, &o.SizeBytes, &metaBytes, &o.CreatedAt); err != nil {
			return nil, err
		}
		if metaBytes != nil {
			_ = json.Unmarshal(metaBytes, &o.Metadata)
		}
		out = append(out, o)
	}
	if out == nil {
		out = []GDPRStorageObject{}
	}
	return out, nil
}

func exportRefreshTokens(ctx context.Context, tx pgx.Tx, qs, userID string) ([]GDPRRefreshToken, error) {
	q := fmt.Sprintf(
		`SELECT created_at, expires_at, revoked
		 FROM %s.refresh_tokens WHERE user_id = $1 ORDER BY created_at`, qs)

	rows, err := tx.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []GDPRRefreshToken
	for rows.Next() {
		var t GDPRRefreshToken
		if err := rows.Scan(&t.CreatedAt, &t.ExpiresAt, &t.Revoked); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if out == nil {
		out = []GDPRRefreshToken{}
	}
	return out, nil
}

// platformTables are managed by the platform and scanned separately above.
var gdprPlatformTables = map[string]bool{
	"users": true, "refresh_tokens": true, "storage_objects": true,
	"email_tokens": true, "user_identities": true, "vault_secrets": true,
}

// ownerColumns are column names that link a row to an end-user.
var ownerColumns = []string{"user_id", "owner_id", "created_by"}

// exportUserOwnedRows scans every developer-created table in the schema for
// rows belonging to the given user, identified by any column in ownerColumns.
func exportUserOwnedRows(ctx context.Context, tx pgx.Tx, schema, userID string) (map[string][]map[string]any, error) {
	qs := quoteIdent(schema)

	// Discover tables.
	tableRows, err := tx.Query(ctx,
		`SELECT table_name FROM information_schema.tables
		 WHERE table_schema = $1 AND table_type = 'BASE TABLE'
		 ORDER BY table_name`, schema)
	if err != nil {
		return nil, err
	}
	defer tableRows.Close()

	var tables []string
	for tableRows.Next() {
		var t string
		if err := tableRows.Scan(&t); err != nil {
			return nil, err
		}
		if !gdprPlatformTables[t] {
			tables = append(tables, t)
		}
	}

	result := map[string][]map[string]any{}

	for _, table := range tables {
		// Find which owner column exists in this table.
		col, err := findOwnerColumn(ctx, tx, schema, table)
		if err != nil || col == "" {
			continue
		}

		q := fmt.Sprintf(`SELECT row_to_json(t)::text FROM %s.%s t WHERE %s = $1`,
			qs, quoteIdent(table), quoteIdent(col))
		rows, err := tx.Query(ctx, q, userID)
		if err != nil {
			slog.Debug("gdpr export: skip table", "table", table, "error", err)
			continue
		}

		var tableRows []map[string]any
		for rows.Next() {
			var jsonStr string
			if err := rows.Scan(&jsonStr); err != nil {
				continue
			}
			var row map[string]any
			if err := json.Unmarshal([]byte(jsonStr), &row); err != nil {
				continue
			}
			tableRows = append(tableRows, row)
		}
		rows.Close()

		if len(tableRows) > 0 {
			result[table] = tableRows
		}
	}

	return result, nil
}

// findOwnerColumn checks if a table has any of the known owner columns.
func findOwnerColumn(ctx context.Context, tx pgx.Tx, schema, table string) (string, error) {
	placeholders := make([]string, len(ownerColumns))
	args := make([]any, len(ownerColumns)+2)
	args[0] = schema
	args[1] = table
	for i, c := range ownerColumns {
		placeholders[i] = fmt.Sprintf("$%d", i+3)
		args[i+2] = c
	}

	q := fmt.Sprintf(
		`SELECT column_name FROM information_schema.columns
		 WHERE table_schema = $1 AND table_name = $2
		   AND column_name IN (%s)
		 LIMIT 1`, strings.Join(placeholders, ","))

	var col string
	err := tx.QueryRow(ctx, q, args...).Scan(&col)
	if err != nil {
		return "", err
	}
	return col, nil
}
