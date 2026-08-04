package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/dbprovider"
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
	PublicKey string `json:"public_key,omitempty"`
	SecretKey string `json:"secret_key,omitempty"`
	// Phase B lifecycle columns (migration 000076). Nullable in JSON
	// so old clients don't break; console consults them to render the
	// paused-state banner + the grandfather-window countdown.
	State              string     `json:"state,omitempty"`
	LastActiveAt       *time.Time `json:"last_active_at,omitempty"`
	GrandfatheredUntil *time.Time `json:"grandfathered_until,omitempty"`
	// Billing legacy-Pro grace window (migration 000080, PR 5).
	// Set for existing beta-Pro projects at the moment
	// BILLING_ENABLED flips true; cleared when the user completes
	// checkout (PR 4 webhook activate) OR the downgrade sweep sets
	// the project to Free. The console consults this — combined
	// with plan='pro' — to render the "add a payment method"
	// modal for the 1% of beta users on Pro at billing-flip time.
	LegacyProGraceUntil *time.Time `json:"legacy_pro_grace_until,omitempty"`
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

// BetaGrantRecorder is the subset of billing.Service that
// CreateProject needs to record a Team-tier closed-beta subscription
// (M2). Matched by *billing.Service.RecordBetaGrant. Optional — if
// nil, CreateProject just skips the subscription row and logs a
// warning; the project itself still provisions.
type BetaGrantRecorder interface {
	RecordBetaGrant(ctx context.Context, projectID, planCode string) (string, error)
}

// TenantService encapsulates database operations for tenant/project management.
type TenantService struct {
	pool        *pgxpool.Pool
	riverClient *river.Client[pgx.Tx]
	secrets     SecretStore
	betaGrants  BetaGrantRecorder
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

// SetBetaGrantRecorder wires an optional beta-grant recorder
// (typically *billing.Service) into the tenant service. When set,
// CreateProject with plan=team writes a beta_grant subscription row
// so the console shows an accurate billing state. Optional — nil
// causes the project to still provision, just without the
// bookkeeping row.
func (s *TenantService) SetBetaGrantRecorder(r BetaGrantRecorder) {
	s.betaGrants = r
}

// ErrTeamBetaRequired is returned by CreateProject when a caller
// asks for plan=team but doesn't have the closed-beta flag set on
// their platform_user row. Handlers should surface this as HTTP 403
// with a code the console can map to a "join the waitlist" prompt.
var ErrTeamBetaRequired = errors.New("team plan requires closed-beta access")

// CreateProject provisions a new project for the given owner within a transaction.
// It upserts the platform_user, inserts the project, calls provision_tenant(),
// and updates the status to 'active' or 'provisioning_failed'.
// The platformUserID is the platform_users.id (UUID), and email is the user's email.
//
// Team-tier dispatch (M2): if req.Plan == "team":
//   1. Verify the platform_user has team_beta_access = true. If not,
//      return ErrTeamBetaRequired (the beta window is admin-managed —
//      users must be granted via the admin panel first).
//   2. Provision the shared-cluster tenant schema like any other
//      project (SDK / REST still land there for M2; gateway routing
//      to the dedicated instance is a follow-up).
//   3. After the tx commits, enqueue ProvisionTeamDatabaseArgs so
//      the worker (internal/workers/provision_team_db.go) spins up
//      the per-project dedicated managed-PG instance.
//   4. Also enqueue a beta_grant subscription via billing.RecordBetaGrant
//      so the console's /billing screen shows the "Team (closed beta)"
//      status instead of "no active subscription."
func (s *TenantService) CreateProject(ctx context.Context, platformUserID, email string, req CreateProjectRequest) (*Project, error) {
	slug := req.Slug
	if slug == "" {
		slug = slugify(req.Name)
	}

	// Team-plan gate — verify beta access BEFORE opening the tx so a
	// rejected user doesn't waste a schema-create round trip.
	if req.Plan == "team" {
		var granted bool
		err := s.pool.QueryRow(ctx,
			`SELECT COALESCE(team_beta_access, false) FROM platform_users WHERE id = $1::uuid`,
			platformUserID,
		).Scan(&granted)
		if err != nil {
			return nil, fmt.Errorf("check team beta access: %w", err)
		}
		if !granted {
			return nil, ErrTeamBetaRequired
		}
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

	// Insert owner membership row so role-based access checks work from
	// the moment the project is created. Uses ON CONFLICT for idempotency
	// in case the migration backfill already ran.
	_, err = tx.Exec(ctx,
		`INSERT INTO project_members (project_id, user_id, role)
		 VALUES ($1, $2, 'owner')
		 ON CONFLICT (project_id, user_id) DO NOTHING`,
		projectID, ownerID,
	)
	if err != nil {
		return nil, fmt.Errorf("insert owner membership: %w", err)
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

	// Team-tier: enqueue the dedicated managed-PG provisioning worker
	// (M1). Runs in parallel with the S3 bucket job above — the two
	// don't interact. If enqueue fails, we log loudly and continue —
	// ops can manually enqueue via a river-cli command; the project
	// row is already 'active' with a NULL project_databases row that
	// the console can render as "provisioning pending."
	if status == "active" && req.Plan == "team" && s.riverClient != nil {
		_, err := s.riverClient.Insert(ctx, jobs.ProvisionTeamDatabaseArgs{
			ProjectID: projectID,
			Slug:      slug,
			Provider:  "scaleway",
			Region:    "fr-par",
			Size:      "medium",
		}, nil)
		if err != nil {
			slog.Error("failed to enqueue team-database provision job",
				"error", err, "project_id", projectID)
		} else {
			slog.Info("team-database provision job enqueued", "project_id", projectID, "slug", slug)
		}

		// Record the closed-beta subscription so /billing shows a
		// coherent state. Missing recorder or duplicate row (race)
		// is non-fatal; the project still provisions.
		if s.betaGrants != nil {
			_, err := s.betaGrants.RecordBetaGrant(ctx, projectID, req.Plan)
			if err != nil {
				slog.Warn("failed to record team-tier beta_grant subscription",
					"error", err, "project_id", projectID)
			}
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
		`SELECT id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status,
		        auth_config, created_at, state, last_active_at, grandfathered_until,
		        legacy_pro_grace_until
		 FROM projects WHERE id = $1`,
		projectID,
	).Scan(&p.ID, &p.OwnerID, &p.Name, &p.Slug, &p.SchemaName, &p.S3Bucket, &p.Region, &p.Plan, &p.Status,
		&p.AuthConfig, &p.CreatedAt, &p.State, &p.LastActiveAt, &p.GrandfatheredUntil, &p.LegacyProGraceUntil)
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

// ListProjects returns all projects the given platform user is a member of
// (owner, admin, developer, or viewer).
func (s *TenantService) ListProjects(ctx context.Context, platformUserID string) ([]Project, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT p.id, p.owner_id, p.name, p.slug, p.schema_name, p.s3_bucket,
		        p.region, p.plan, p.status, p.auth_config, p.created_at,
		        p.state, p.last_active_at, p.grandfathered_until,
		        p.legacy_pro_grace_until
		 FROM projects p
		 JOIN project_members pm ON pm.project_id = p.id
		 WHERE pm.user_id = $1::uuid
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
		if err := rows.Scan(&p.ID, &p.OwnerID, &p.Name, &p.Slug, &p.SchemaName, &p.S3Bucket, &p.Region, &p.Plan, &p.Status,
			&p.AuthConfig, &p.CreatedAt, &p.State, &p.LastActiveAt, &p.GrandfatheredUntil, &p.LegacyProGraceUntil); err != nil {
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
//
// Team-tier note (M2 review bug_002): if a live project_databases row
// exists for this project, mark it deleted_at=now() BEFORE the
// DELETE FROM projects. The FK is ON DELETE RESTRICT (migration
// 000083) — the delete would otherwise 23503 and leave the tenant
// schema already dropped by deprovision_tenant but the row still
// visible. MarkDeleted flips state='deleting' and sets deleted_at,
// which drops the row out of the unique-live-per-project index and
// lets the FK-target-side cascade proceed. The DeprovisionTeamDatabaseWorker
// sweeps the marked row 7 days later + destroys the actual Scaleway
// instance.
func (s *TenantService) DeleteProject(ctx context.Context, projectID string) error {
	// Mark any live project_databases row deleted so the ON DELETE
	// RESTRICT FK doesn't block the projects row delete. Idempotent
	// — no live row is fine (Free / Pro projects), returns quickly.
	repo := dbprovider.NewRepo(s.pool)
	rec, err := repo.GetLiveByProject(ctx, projectID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("lookup project_databases: %w", err)
	}
	if rec != nil {
		if err := repo.MarkDeleted(ctx, rec.ID); err != nil {
			return fmt.Errorf("mark project_databases deleted: %w", err)
		}
		slog.Info("marked project_databases row deleted before project drop",
			"project_id", projectID, "project_database_id", rec.ID)
	}

	// Call deprovision_tenant to drop the schema.
	_, err = s.pool.Exec(ctx, `SELECT deprovision_tenant($1::uuid)`, projectID)
	if err != nil {
		slog.Error("deprovision_tenant failed", "error", err, "project_id", projectID)
		return fmt.Errorf("deprovision tenant: %w", err)
	}

	// Delete the project row (cascades to api_keys, webhooks, etc.).
	// project_databases (Team-tier) is RESTRICT rather than CASCADE —
	// the MarkDeleted above soft-deletes the row so the RESTRICT sees
	// no live reference. superseded_by FK on project_databases is
	// SET NULL so nothing else blocks.
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

// UpdateAuthConfig updates the auth_config for a project.
//
// Any client_secret values passed in the incoming config are routed through
// the configured SecretStore (typically the vault) and stripped from the
// persisted JSONB. Values that look like masked placeholders (contain "*")
// are ignored — preserving whatever the vault already holds — so the console
// can safely echo back a masked secret on save without clobbering the real one.
// If the vault is not configured, any attempt to set a new OAuth secret fails
// with a clear error rather than silently falling through to plaintext storage.
// Returns the list of OAuth provider names whose secrets were rotated.
func (s *TenantService) UpdateAuthConfig(ctx context.Context, projectID, ownerID string, config AuthConfig) (rotatedProviders []string, err error) {
	// Resolve project schema up-front — we need it for every vault call.
	// Access control (role check) is done by the handler before calling this
	// method, so we only need to verify the project exists.
	var schemaName string
	err = s.pool.QueryRow(ctx,
		`SELECT schema_name FROM projects WHERE id = $1`,
		projectID,
	).Scan(&schemaName)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("project not found")
		}
		return nil, fmt.Errorf("lookup project schema: %w", err)
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
			return nil, fmt.Errorf("cannot store oauth secret for %q: vault not configured (set VAULT_ENCRYPTION_KEY)", name)
		}

		if err := s.secrets.SetRaw(ctx, schemaName, oauthSecretVaultKey(name), incoming); err != nil {
			return nil, fmt.Errorf("store oauth secret for %q: %w", name, err)
		}
		rotatedProviders = append(rotatedProviders, name)
	}

	// Marshal the (now secret-free) config and persist to auth_config.
	configJSON, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("marshal auth config: %w", err)
	}

	tag, err := s.pool.Exec(ctx,
		`UPDATE projects SET auth_config = $1 WHERE id = $2`,
		configJSON, projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("update auth config: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, fmt.Errorf("project not found")
	}

	slog.Info("auth config updated", "project_id", projectID)
	return rotatedProviders, nil
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
