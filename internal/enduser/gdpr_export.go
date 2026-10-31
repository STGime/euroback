package enduser

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
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
	ExportVersion  string                      `json:"export_version"`
	ExportedAt     time.Time                   `json:"exported_at"`
	ProjectID      string                      `json:"project_id"`
	User           GDPRUserProfile             `json:"user"`
	Identities     []GDPRIdentity              `json:"identities"`
	StorageObjects []GDPRStorageObject         `json:"storage_objects"`
	RefreshTokens  []GDPRRefreshToken          `json:"refresh_tokens"`
	UserData       map[string][]map[string]any `json:"user_data"`
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
	Key         string         `json:"key"`
	ContentType *string        `json:"content_type"`
	SizeBytes   *int64         `json:"size_bytes"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
}

// GDPRRefreshToken is token metadata (not the hash).
type GDPRRefreshToken struct {
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Revoked   bool       `json:"revoked"`
}

// HandleGDPRExport returns a handler for GET /platform/projects/{id}/users/{userId}/export.
// Requires admin or owner role on the project; rate-limited per actor.
// catalog answers the table and column discovery (the developer pool on
// the shared cluster); nil uses pool.
func HandleGDPRExport(pool, catalog *pgxpool.Pool, limiter *ratelimit.RateLimiter) http.HandlerFunc {
	if catalog == nil {
		catalog = pool
	}
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

		// Which developer tables hold rows owned by a user: read from the
		// catalog before the service transaction (the runtime role has no
		// catalog access).
		ownedTables, ownedErr := discoverUserOwnedTables(ctx, catalog, schema)

		// Run all export queries inside a single service-role transaction
		// so RLS policies on the tenant schema permit reading rows owned
		// by the target user (platform admin is acting on their behalf).
		var profile *GDPRUserProfile
		var identities []GDPRIdentity
		var storageObjs []GDPRStorageObject
		var tokens []GDPRRefreshToken
		var userData map[string][]map[string]any
		// Closes #164. The GDPR export reads refresh_tokens (line 273),
		// email_tokens, vault_secrets — all RLS-gated behind the
		// internal_auth_path intent by migration 000055. This is a
		// legitimate admin path so the GUC is set explicitly.
		profileErr := edb.RunAsAuthService(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
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
			if ownedErr != nil {
				slog.Error("gdpr export: user data tables", "error", ownedErr, "user_id", userID)
				userData = map[string][]map[string]any{}
			} else if d, err := exportUserOwnedRows(ctx, tx, schema, userID, ownedTables); err != nil {
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
	"storage_shared_prefixes": true,
	// #630: passkey tables are platform-managed (the generic owner-column
	// scan would otherwise pick them up via user_id).
	"user_passkey_credentials": true, "webauthn_challenges": true,
}

// ownerColumns are column names that link a row to an end-user.
var ownerColumns = []string{"user_id", "owner_id", "created_by"}

// ownedTable is a developer table with the column that links its rows to
// an end user.
type ownedTable struct{ table, column string }

// discoverUserOwnedTables lists the developer-created tables in the schema
// that have one of ownerColumns (the first one present, in that order).
// It reads the catalog, so q must be a pool with catalog access.
func discoverUserOwnedTables(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, schema string) ([]ownedTable, error) {
	rows, err := q.Query(ctx,
		`SELECT c.table_name, c.column_name
		   FROM information_schema.columns c
		   JOIN information_schema.tables t
		     ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		  WHERE c.table_schema = $1 AND t.table_type = 'BASE TABLE'
		    AND c.column_name = ANY($2)
		  ORDER BY c.table_name`, schema, ownerColumns)
	if err != nil {
		return nil, err
	}
	found := map[string]map[string]bool{}
	var order []string
	for rows.Next() {
		var t, c string
		if err := rows.Scan(&t, &c); err != nil {
			rows.Close()
			return nil, err
		}
		if gdprPlatformTables[t] {
			continue
		}
		if found[t] == nil {
			found[t] = map[string]bool{}
			order = append(order, t)
		}
		found[t][c] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []ownedTable
	for _, t := range order {
		for _, c := range ownerColumns {
			if found[t][c] {
				out = append(out, ownedTable{table: t, column: c})
				break
			}
		}
	}
	return out, nil
}

// exportUserOwnedRows reads, from each owned table, the rows belonging to
// the given user.
func exportUserOwnedRows(ctx context.Context, tx pgx.Tx, schema, userID string, tables []ownedTable) (map[string][]map[string]any, error) {
	qs := quoteIdent(schema)
	result := map[string][]map[string]any{}

	for _, ot := range tables {
		q := fmt.Sprintf(`SELECT row_to_json(t)::text FROM %s.%s t WHERE %s = $1`,
			qs, quoteIdent(ot.table), quoteIdent(ot.column))
		rows, err := tx.Query(ctx, q, userID)
		if err != nil {
			slog.Debug("gdpr export: skip table", "table", ot.table, "error", err)
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
			result[ot.table] = tableRows
		}
	}

	return result, nil
}
