package tenant

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// Project represents a provisioned tenant project.
type Project struct {
	ID         string    `json:"id"`
	OwnerID    string    `json:"owner_id"`
	Name       string    `json:"name"`
	Slug       string    `json:"slug"`
	SchemaName string    `json:"schema_name"`
	S3Bucket   string    `json:"s3_bucket"`
	Region     string    `json:"region"`
	Plan       string    `json:"plan"`
	Status     string    `json:"status"`
	APIURL     string          `json:"api_url"`
	AuthConfig json.RawMessage `json:"auth_config,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	// PublicKey and SecretKey are only populated on creation (plaintext shown once).
	PublicKey  string `json:"public_key,omitempty"`
	SecretKey  string `json:"secret_key,omitempty"`
}

// SecretStore is the minimal interface the tenant package needs to persist
// OAuth client secrets. It matches vault.VaultService so we can wire the
// real vault in without a hard import.
type SecretStore interface {
	SetRaw(ctx context.Context, schemaName, name, value string) error
	GetRaw(ctx context.Context, schemaName, name string) (string, error)
	DeleteRaw(ctx context.Context, schemaName, name string) error
	HasRaw(ctx context.Context, schemaName, name string) (bool, error)
	Configured() bool
}

// TenantService encapsulates database operations for tenant/project management.
type TenantService struct {
	pool        *pgxpool.Pool
	riverClient *river.Client[pgx.Tx]
	secrets     SecretStore
}

// NewTenantService creates a new TenantService backed by the given connection pool.
// The River client is used to enqueue async provisioning jobs.
func NewTenantService(pool *pgxpool.Pool) *TenantService {
	// Create a River client in insert-only mode (no workers — the worker process handles those).
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		slog.Error("failed to create river client for tenant service", "error", err)
		// Continue without River — CreateProject will fall back to synchronous-only.
		return &TenantService{pool: pool}
	}

	return &TenantService{
		pool:        pool,
		riverClient: riverClient,
	}
}

// SetSecretStore wires an optional secret store (typically the vault service)
// into the tenant service. When set, OAuth client secrets from auth_config
// are routed through the store instead of being persisted to the
// projects.auth_config JSONB column.
func (s *TenantService) SetSecretStore(store SecretStore) {
	s.secrets = store
}

// CreateProject provisions a new project for the given owner within a transaction.
// It upserts the platform_user, inserts the project, calls provision_tenant(),
// and updates the status to 'active' or 'provisioning_failed'.
// The platformUserID is the platform_users.id (UUID), and email is the user's email.
func (s *TenantService) CreateProject(ctx context.Context, platformUserID, email string, req CreateProjectRequest) (*Project, error) {
	slug := req.Slug
	if slug == "" {
		slug = slugify(req.Name)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Resolve the owner — platformUserID is the platform_users.id (UUID).
	var ownerID string
	err = tx.QueryRow(ctx,
		`SELECT id FROM platform_users WHERE id = $1::uuid`,
		platformUserID,
	).Scan(&ownerID)
	if err != nil {
		return nil, fmt.Errorf("resolve platform user: %w", err)
	}

	// Derive temporary schema_name and s3_bucket.
	tempSchemaName := fmt.Sprintf("tenant_%s", strings.ReplaceAll(slug, "-", "_"))
	s3Bucket := fmt.Sprintf("eurobase-%s", slug)

	// Insert the project with status='provisioning'.
	var projectID string
	var createdAt time.Time
	err = tx.QueryRow(ctx,
		`INSERT INTO projects (owner_id, name, slug, schema_name, s3_bucket, region, plan)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, created_at`,
		ownerID, req.Name, slug, tempSchemaName, s3Bucket, req.Region, req.Plan,
	).Scan(&projectID, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("insert project: %w", err)
	}

	slog.Info("project record inserted", "project_id", projectID, "slug", slug)

	// Call provision_tenant to create the isolated tenant schema.
	_, err = tx.Exec(ctx,
		`SELECT provision_tenant($1, $2, $3)`,
		projectID, req.Name, req.Plan,
	)

	var status string
	var publicKey, secretKey string
	if err != nil {
		slog.Error("provision_tenant failed", "error", err, "project_id", projectID)
		// Mark as failed.
		_, updateErr := tx.Exec(ctx,
			`UPDATE projects SET status = 'provisioning_failed' WHERE id = $1`,
			projectID,
		)
		if updateErr != nil {
			return nil, fmt.Errorf("provision_tenant failed and could not update status: provision=%w, update=%v", err, updateErr)
		}
		status = "provisioning_failed"
	} else {
		// Generate API keys synchronously (pure crypto, microseconds).
		var publicKeyHash, secretKeyHash string
		publicKey, secretKey, publicKeyHash, secretKeyHash, err = GenerateAPIKeyPair()
		if err != nil {
			return nil, fmt.Errorf("generate api keys: %w", err)
		}

		publicKeyPrefix := publicKey[:14]
		secretKeyPrefix := secretKey[:14]

		if err := StoreAPIKeys(ctx, tx, projectID, publicKeyHash, publicKeyPrefix, secretKeyHash, secretKeyPrefix); err != nil {
			return nil, fmt.Errorf("store api keys: %w", err)
		}

		// Mark as active immediately — only S3 bucket creation stays async.
		_, err = tx.Exec(ctx,
			`UPDATE projects SET status = 'active' WHERE id = $1`,
			projectID,
		)
		if err != nil {
			return nil, fmt.Errorf("update project status: %w", err)
		}
		status = "active"
	}

	// Read back the final schema_name (provision_tenant may have updated it).
	var schemaName string
	err = tx.QueryRow(ctx,
		`SELECT schema_name FROM projects WHERE id = $1`,
		projectID,
	).Scan(&schemaName)
	if err != nil {
		return nil, fmt.Errorf("read back schema_name: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	// Enqueue async provisioning job (S3 bucket only) if schema provisioning succeeded.
	if status == "active" && s.riverClient != nil {
		_, err := s.riverClient.Insert(ctx, jobs.ProvisionProjectArgs{
			ProjectID: projectID,
			Slug:      slug,
			Plan:      req.Plan,
		}, nil)
		if err != nil {
			slog.Error("failed to enqueue provision job", "error", err, "project_id", projectID)
		} else {
			slog.Info("async provision job enqueued (s3 bucket)", "project_id", projectID, "slug", slug)
		}
	}

	slog.Info("project provisioned",
		"project_id", projectID,
		"slug", slug,
		"schema_name", schemaName,
		"status", status,
	)

	return &Project{
		ID:         projectID,
		OwnerID:    ownerID,
		Name:       req.Name,
		Slug:       slug,
		SchemaName: schemaName,
		S3Bucket:   s3Bucket,
		Region:     req.Region,
		Plan:       req.Plan,
		Status:     status,
		APIURL:     fmt.Sprintf("https://%s.eurobase.app", slug),
		CreatedAt:  createdAt,
		PublicKey:  publicKey,
		SecretKey:  secretKey,
	}, nil
}

// GetProject retrieves a single project by its ID.
func (s *TenantService) GetProject(ctx context.Context, projectID string) (*Project, error) {
	var p Project
	err := s.pool.QueryRow(ctx,
		`SELECT id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status, auth_config, created_at
		 FROM projects WHERE id = $1`,
		projectID,
	).Scan(&p.ID, &p.OwnerID, &p.Name, &p.Slug, &p.SchemaName, &p.S3Bucket, &p.Region, &p.Plan, &p.Status, &p.AuthConfig, &p.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("project not found: %s", projectID)
		}
		return nil, fmt.Errorf("query project: %w", err)
	}
	p.APIURL = fmt.Sprintf("https://%s.eurobase.app", p.Slug)
	p.AuthConfig = s.annotateAuthConfig(ctx, p.SchemaName, p.AuthConfig)
	return &p, nil
}

// annotateAuthConfig strips any stale client_secret values from auth_config
// and decorates each OAuth provider with a "secret_set" boolean based on the
// vault. Safe to call when the vault is not configured — the result will
// simply report secret_set=false for every provider.
func (s *TenantService) annotateAuthConfig(ctx context.Context, schemaName string, raw []byte) []byte {
	return AnnotateOAuthSecretStatus(raw, func(provider string) bool {
		return s.HasOAuthClientSecret(ctx, schemaName, provider)
	})
}

// ListProjects returns all projects owned by the given platform user.
func (s *TenantService) ListProjects(ctx context.Context, platformUserID string) ([]Project, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT p.id, p.owner_id, p.name, p.slug, p.schema_name, p.s3_bucket,
		        p.region, p.plan, p.status, p.auth_config, p.created_at
		 FROM projects p
		 WHERE p.owner_id = $1::uuid
		 ORDER BY p.created_at DESC`,
		platformUserID,
	)
	if err != nil {
		return nil, fmt.Errorf("query projects: %w", err)
	}
	defer rows.Close()

	projects := make([]Project, 0)
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.OwnerID, &p.Name, &p.Slug, &p.SchemaName, &p.S3Bucket, &p.Region, &p.Plan, &p.Status, &p.AuthConfig, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan project row: %w", err)
		}
		p.APIURL = fmt.Sprintf("https://%s.eurobase.app", p.Slug)
		p.AuthConfig = s.annotateAuthConfig(ctx, p.SchemaName, p.AuthConfig)
		projects = append(projects, p)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project rows: %w", err)
	}

	return projects, nil
}

// DeleteProject drops the tenant schema and deletes the project row.
// The caller must verify ownership before calling this.
func (s *TenantService) DeleteProject(ctx context.Context, projectID string) error {
	// Call deprovision_tenant to drop the schema.
	_, err := s.pool.Exec(ctx, `SELECT deprovision_tenant($1::uuid)`, projectID)
	if err != nil {
		slog.Error("deprovision_tenant failed", "error", err, "project_id", projectID)
		return fmt.Errorf("deprovision tenant: %w", err)
	}

	// Delete the project row (cascades to api_keys, webhooks, etc.).
	tag, err := s.pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, projectID)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("project not found")
	}

	slog.Info("project deleted", "project_id", projectID)
	return nil
}

// oauthSecretVaultKey returns the canonical vault key used to store an OAuth
// provider's client_secret for a given project.
func oauthSecretVaultKey(provider string) string {
	return "oauth." + provider + ".client_secret"
}

// UpdateAuthConfig updates the auth_config for a project, verifying ownership.
//
// Any client_secret values passed in the incoming config are routed through
// the configured SecretStore (typically the vault) and stripped from the
// persisted JSONB. Values that look like masked placeholders (contain "*")
// are ignored — preserving whatever the vault already holds — so the console
// can safely echo back a masked secret on save without clobbering the real one.
// If the vault is not configured, any attempt to set a new OAuth secret fails
// with a clear error rather than silently falling through to plaintext storage.
func (s *TenantService) UpdateAuthConfig(ctx context.Context, projectID, ownerID string, config AuthConfig) error {
	// Resolve project schema up-front — we need it for every vault call.
	var schemaName string
	err := s.pool.QueryRow(ctx,
		`SELECT schema_name FROM projects WHERE id = $1 AND owner_id = $2::uuid`,
		projectID, ownerID,
	).Scan(&schemaName)
	if err != nil {
		if err == pgx.ErrNoRows {
			return fmt.Errorf("project not found or not owned by user")
		}
		return fmt.Errorf("lookup project schema: %w", err)
	}

	// Walk OAuth providers, route secrets to vault, strip from persisted config.
	for name, provider := range config.OAuthProviders {
		incoming := provider.ClientSecret
		// Always strip the secret from the persisted struct.
		provider.ClientSecret = ""
		config.OAuthProviders[name] = provider

		if incoming == "" || IsMaskedSecret(incoming) {
			// No change — leave vault entry alone.
			continue
		}

		if s.secrets == nil || !s.secrets.Configured() {
			return fmt.Errorf("cannot store oauth secret for %q: vault not configured (set VAULT_ENCRYPTION_KEY)", name)
		}

		if err := s.secrets.SetRaw(ctx, schemaName, oauthSecretVaultKey(name), incoming); err != nil {
			return fmt.Errorf("store oauth secret for %q: %w", name, err)
		}
	}

	// Marshal the (now secret-free) config and persist to auth_config.
	configJSON, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshal auth config: %w", err)
	}

	tag, err := s.pool.Exec(ctx,
		`UPDATE projects SET auth_config = $1 WHERE id = $2 AND owner_id = $3::uuid`,
		configJSON, projectID, ownerID,
	)
	if err != nil {
		return fmt.Errorf("update auth config: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("project not found or not owned by user")
	}

	slog.Info("auth config updated", "project_id", projectID)
	return nil
}

// GetOAuthClientSecret returns the decrypted OAuth client_secret for a given
// provider by reading it from the vault. Returns an empty string and no error
// if no secret is stored. Used by the auth service during the OAuth code
// exchange.
func (s *TenantService) GetOAuthClientSecret(ctx context.Context, schemaName, providerName string) (string, error) {
	if s.secrets == nil || !s.secrets.Configured() {
		return "", fmt.Errorf("vault not configured")
	}
	return s.secrets.GetRaw(ctx, schemaName, oauthSecretVaultKey(providerName))
}

// HasOAuthClientSecret reports whether a vault entry exists for the given
// provider. Used by AnnotateOAuthSecretStatus to decorate API responses
// with a "secret_set" boolean so the UI can show "Secret configured".
func (s *TenantService) HasOAuthClientSecret(ctx context.Context, schemaName, providerName string) bool {
	if s.secrets == nil || !s.secrets.Configured() {
		return false
	}
	has, err := s.secrets.HasRaw(ctx, schemaName, oauthSecretVaultKey(providerName))
	if err != nil {
		return false
	}
	return has
}
