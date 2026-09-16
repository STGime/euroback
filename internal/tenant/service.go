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
	// Team-tier org ownership (migration 000114). Nullable; NULL
	// means the project is per-user-owned. When set, org_members of
	// this org can see the project through the additive access path.
	OrgID   *string `json:"org_id,omitempty"`
	OrgName *string `json:"org_name,omitempty"`
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
	// providerRegistry lets DeleteProject best-effort tear down
	// dedicated Scaleway instances synchronously (in addition to
	// hard-deleting the project_databases rows). Optional — when nil,
	// DeleteProject still succeeds by hard-deleting the rows, but a
	// leaked provider instance stays until manual ops cleanup.
	providerRegistry *dbprovider.Registry
	// developerPool is the platform-authenticated pool used for reads
	// on tables the gateway pool has no grants on — chiefly
	// public.organizations and public.org_members (migration 000114
	// REVOKEs both from eurobase_gateway; see CLAUDE.md convention).
	// Optional: when nil, org-attach paths that depend on org_members
	// visibility fall back to leaving projects.org_id NULL.
	developerPool *pgxpool.Pool
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

// SetProviderRegistry wires an optional dbprovider.Registry into the
// tenant service. When set, DeleteProject best-effort tears down any
// dedicated Scaleway instances tied to the project synchronously
// before hard-deleting the project_databases rows. Optional — when
// nil, DeleteProject still hard-deletes the rows (unblocking the
// projects.id FK), but a leaked provider instance stays for manual
// cleanup via the Scaleway console.
func (s *TenantService) SetProviderRegistry(reg *dbprovider.Registry) {
	s.providerRegistry = reg
}

// ErrOrgAttachForbidden is returned when the caller tries to attach
// a project to an org they are not a member of. Handler maps to 403.
var ErrOrgAttachForbidden = errors.New("caller is not a member of the target organization")

// SetProjectOrg attaches a project to an org (orgID non-nil) or
// detaches it (orgID nil, "personal" state). Only the project's
// owner can move it. If orgID is set, the caller must also be a
// member of the target org — enforced via developerPool since
// migration 000114 REVOKEs org_members from the gateway pool.
//
// Returns ErrProjectNotFound if the project doesn't exist or isn't
// owned by the caller; ErrOrgAttachForbidden if the caller lacks
// membership in the target org.
func (s *TenantService) SetProjectOrg(ctx context.Context, projectID, platformUserID string, orgID *string) error {
	// Verify the caller owns the project. Using the gateway pool is
	// fine — projects.owner_id is grant-visible.
	var ownerID string
	err := s.pool.QueryRow(ctx,
		`SELECT owner_id::text FROM public.projects WHERE id = $1::uuid`,
		projectID,
	).Scan(&ownerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrProjectNotFound
		}
		return fmt.Errorf("lookup project owner: %w", err)
	}
	if ownerID != platformUserID {
		// Same 404 shape as "doesn't exist" — don't leak existence to
		// non-owners.
		return ErrProjectNotFound
	}

	// If attaching, verify org membership via the developer pool.
	// Detach (orgID nil) needs no org lookup — the owner can always
	// remove their project from any org they moved it into.
	if orgID != nil {
		if s.developerPool == nil {
			// Dev / partial-config: refuse rather than silently no-op,
			// since a "attach" request that lands as personal would
			// surprise the caller.
			return fmt.Errorf("org attach unavailable: developer pool not configured")
		}
		var one int
		err := s.developerPool.QueryRow(ctx,
			`SELECT 1 FROM public.org_members
			 WHERE org_id = $1::uuid AND platform_user_id = $2::uuid`,
			*orgID, platformUserID,
		).Scan(&one)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrOrgAttachForbidden
			}
			return fmt.Errorf("check org membership: %w", err)
		}
	}

	// Apply. FK is ON DELETE SET NULL (migration 000114:142) so a
	// concurrent DeleteOrg would leave the project as personal, not
	// error here.
	if orgID != nil {
		_, err = s.pool.Exec(ctx,
			`UPDATE public.projects SET org_id = $1::uuid, updated_at = now() WHERE id = $2::uuid`,
			*orgID, projectID)
	} else {
		_, err = s.pool.Exec(ctx,
			`UPDATE public.projects SET org_id = NULL, updated_at = now() WHERE id = $1::uuid`,
			projectID)
	}
	if err != nil {
		return fmt.Errorf("update project org_id: %w", err)
	}
	return nil
}

// SetDeveloperPool wires the platform-authenticated pool so paths
// that need to read org tables (public.organizations,
// public.org_members) can bypass the gateway pool's REVOKE-ALL from
// migration 000114. Chief users: CreateProject's org auto-attach,
// ListProjects's org-membership union, PATCH project's org-attach
// membership check. Optional in dev — when nil, project-org
// attachment is skipped and org-owned projects don't surface via
// ListProjects for members-only-via-org callers.
func (s *TenantService) SetDeveloperPool(devPool *pgxpool.Pool) {
	s.developerPool = devPool
}

// CreateProjectForBilling is the adapter that satisfies
// billing.ProjectCreator. Called from the billing webhook's
// new-project-first-payment branch after Mollie confirms payment
// (issue #406). Wraps CreateProject with a primitives-only
// signature so the billing package doesn't have to import
// tenant.CreateProjectRequest.
//
// Returns the newly-created project's ID string. The webhook uses
// it to insert the corresponding subscriptions + invoices rows in
// the same transaction as the project creation.
func (s *TenantService) CreateProjectForBilling(ctx context.Context, ownerID, email, name, slug, region, plan string) (string, error) {
	proj, err := s.CreateProject(ctx, ownerID, email, CreateProjectRequest{
		Name:   name,
		Slug:   slug,
		Region: region,
		Plan:   plan,
	})
	if err != nil {
		return "", err
	}
	return proj.ID, nil
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

// ErrLegalTeamBetaRequired is the M2b analog: caller asked for
// plan=legal_team but doesn't have legal_team_beta_access. Separate
// flag from Team so ops can hand them out independently.
var ErrLegalTeamBetaRequired = errors.New("legal team plan requires closed-beta access")

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

	// Team-plan / Legal-Team-plan gates — verify beta access BEFORE
	// opening the tx so a rejected user doesn't waste a schema-create
	// round trip. Each plan reads its own beta flag so ops can hand
	// out Team and Legal-Team grants independently. Both use the
	// exported *_BetaAccess helpers so the CreateProject path and the
	// admin-lookup path can't drift on the query shape (M2 review
	// minor #1 + M2b addition).
	switch req.Plan {
	case "team":
		granted, err := UserHasTeamBetaAccess(ctx, s.pool, platformUserID)
		if err != nil {
			return nil, fmt.Errorf("check team beta access: %w", err)
		}
		if !granted {
			return nil, ErrTeamBetaRequired
		}
	case "legal_team":
		granted, err := UserHasLegalTeamBetaAccess(ctx, s.pool, platformUserID)
		if err != nil {
			return nil, fmt.Errorf("check legal-team beta access: %w", err)
		}
		if !granted {
			return nil, ErrLegalTeamBetaRequired
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

	// Org auto-attach: if the caller already owns an org (via
	// public.organizations.created_by), stamp the new project with
	// its org_id so org_members can see it via the union path in
	// ListProjects. Runs on the developer pool because migration
	// 000114 REVOKEs ALL from the gateway pool on organizations.
	// Nil pool (dev / partial-config) → skip; project lands with
	// org_id NULL, matching pre-#591 behaviour.
	//
	// Note: this is a separate query against developerPool (not the
	// project-creation tx). A concurrent DeleteOrg between here and
	// the INSERT below would leave the project with a dangling
	// org_id, but the FK is ON DELETE SET NULL (migration
	// 000114:142) so the row ends up as `org_id NULL` rather than a
	// broken reference. Acceptable — DeleteOrg is admin-driven and
	// rare.
	var orgID *string
	if s.developerPool != nil {
		var id string
		err := s.developerPool.QueryRow(ctx,
			`SELECT id::text FROM public.organizations WHERE created_by = $1::uuid LIMIT 1`,
			platformUserID,
		).Scan(&id)
		if err == nil {
			orgID = &id
		} else if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("project org auto-attach: developer-pool query failed; project will land as personal",
				"error", err, "platform_user_id", platformUserID)
		}
	}

	// Derive temporary schema_name and s3_bucket.
	tempSchemaName := fmt.Sprintf("tenant_%s", strings.ReplaceAll(slug, "-", "_"))
	s3Bucket := fmt.Sprintf("eurobase-%s", slug)

	// Insert the project with status='provisioning'. org_id is
	// nullable — NULL for the personal / no-org case, set for
	// auto-attach.
	var projectID string
	var createdAt time.Time
	err = tx.QueryRow(ctx,
		`INSERT INTO projects (owner_id, name, slug, schema_name, s3_bucket, region, plan, org_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8::uuid)
		 RETURNING id, created_at`,
		ownerID, req.Name, slug, tempSchemaName, s3Bucket, req.Region, req.Plan, orgID,
	).Scan(&projectID, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("insert project: %w", err)
	}

	slog.Info("project record inserted", "project_id", projectID, "slug", slug)

	// Team-tier / Legal-Team tenant schemas live on the dedicated
	// Scaleway instance, not the platform DB. The async
	// ProvisionTeamDatabaseWorker calls provision_tenant() on the
	// dedicated instance via BootstrapDedicated once the instance is
	// up. Creating a duplicate schema on the platform DB here would
	// diverge from the dedicated copy the moment SDK / console traffic
	// routes there — silent data loss.
	skipPlatformSchema := req.Plan == "team" || req.Plan == "legal_team"

	if !skipPlatformSchema {
		// Call provision_tenant to create the isolated tenant schema on
		// the platform DB (Free / Pro live here). provision_tenant()
		// also overwrites projects.schema_name with the projectID-based
		// derivation (`tenant_<projectID_with_underscores>`) as its
		// final step — the slug-based value inserted above is a
		// placeholder.
		_, err = tx.Exec(ctx,
			`SELECT provision_tenant($1, $2, $3)`,
			projectID, req.Name, req.Plan,
		)
	} else {
		// Team-tier: mirror provision_tenant()'s final UPDATE so
		// projects.schema_name matches the projectID-based name the
		// dedicated-instance provision_tenant() will create. Keeps a
		// single source of truth for the schema name across every
		// downstream reader (usage tracker, console handlers, etc.).
		_, err = tx.Exec(ctx,
			`UPDATE public.projects
			    SET schema_name = 'tenant_' || replace(id::text, '-', '_')
			  WHERE id = $1`,
			projectID,
		)
	}

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

	// Team + Legal-Team: enqueue the dedicated managed-PG provisioning
	// worker (M1). Runs in parallel with the S3 bucket job above — the
	// two don't interact. If enqueue fails, we log loudly and continue
	// — ops can manually enqueue via a river-cli command; the project
	// row is already 'active' with a NULL project_databases row that
	// the console can render as "provisioning pending."
	//
	// Both tiers get identical infra provisioning; the compliance
	// features that differentiate Legal-Team from Team are gated at
	// endpoint time via plans.CheckLegalTeamTier, not at provision
	// time.
	if status == "active" && (req.Plan == "team" || req.Plan == "legal_team") && s.riverClient != nil {
		_, err := s.riverClient.Insert(ctx, jobs.ProvisionTeamDatabaseArgs{
			ProjectID: projectID,
			Slug:      slug,
			Provider:  "scaleway",
			Region:    "fr-par",
			Size:      "medium",
		}, nil)
		if err != nil {
			slog.Error("failed to enqueue team-database provision job",
				"error", err, "project_id", projectID, "plan", req.Plan)
		} else {
			slog.Info("team-database provision job enqueued", "project_id", projectID, "slug", slug, "plan", req.Plan)
		}

		// Record the closed-beta subscription so /billing shows a
		// coherent state. Missing recorder or duplicate row (race)
		// is non-fatal; the project still provisions.
		if s.betaGrants != nil {
			_, err := s.betaGrants.RecordBetaGrant(ctx, projectID, req.Plan)
			if err != nil {
				slog.Warn("failed to record beta_grant subscription",
					"error", err, "project_id", projectID, "plan", req.Plan)
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
// (owner, admin, developer, or viewer) PLUS all projects owned by any
// org the user is a member of.
//
// Split-pool implementation (re-lands the Phase-2 work reverted after
// #536): the direct-member query runs on the gateway pool as before
// (`project_members` is grant-visible). The org-membership branch
// runs on the developer pool because migration 000114 REVOKEs ALL
// from eurobase_gateway on both org tables (security property: org
// membership is NOT SDK-facing). Results are merged in Go and
// deduped by project id — a user who is both directly added AND an
// org member sees the row once, not twice.
//
// When developerPool is unset (dev / partial-config), the org branch
// is skipped and behaviour matches the pre-#536 hotfix rollback.
// Never emits SQLSTATE 42501 on org_members even when the org pool
// is nil.
func (s *TenantService) ListProjects(ctx context.Context, platformUserID string) ([]Project, error) {
	// Direct-member branch. Runs on gateway pool as today.
	projects, err := s.listDirectMemberProjects(ctx, platformUserID)
	if err != nil {
		return nil, err
	}

	// Org-membership branch. Skipped when developerPool is nil; when
	// present, fetches ids the user reaches via org_members and
	// enriches with the same project columns. Deduped against the
	// direct set by project id.
	if s.developerPool != nil {
		orgProjectIDs, err := s.listOrgProjectIDsForUser(ctx, platformUserID)
		if err != nil {
			// Log but don't fail the whole call — the direct-member set
			// is already a useful answer. Under-count is a UX bug, not
			// a security issue.
			slog.Warn("ListProjects org branch failed; returning direct-member set only",
				"error", err, "platform_user_id", platformUserID)
			return projects, nil
		}
		if len(orgProjectIDs) > 0 {
			seen := make(map[string]struct{}, len(projects))
			for _, p := range projects {
				seen[p.ID] = struct{}{}
			}
			missing := orgProjectIDs[:0]
			for _, id := range orgProjectIDs {
				if _, ok := seen[id]; !ok {
					missing = append(missing, id)
				}
			}
			if len(missing) > 0 {
				extra, err := s.listProjectsByIDs(ctx, missing)
				if err != nil {
					slog.Warn("ListProjects org-projects fetch failed; returning direct-member set only",
						"error", err, "platform_user_id", platformUserID)
					return projects, nil
				}
				projects = append(projects, extra...)
			}
		}
	}
	return projects, nil
}

// listDirectMemberProjects returns projects where the caller has a
// project_members row directly. Gateway-pool query — matches the
// current (post-#536 hotfix) behaviour.
func (s *TenantService) listDirectMemberProjects(ctx context.Context, platformUserID string) ([]Project, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT p.id, p.owner_id, p.name, p.slug, p.schema_name, p.s3_bucket,
		        p.region, p.plan, p.status, p.auth_config, p.created_at,
		        p.state, p.last_active_at, p.grandfathered_until,
		        p.legacy_pro_grace_until,
		        p.org_id::text
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
			&p.AuthConfig, &p.CreatedAt, &p.State, &p.LastActiveAt, &p.GrandfatheredUntil, &p.LegacyProGraceUntil,
			&p.OrgID); err != nil {
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

// listOrgProjectIDsForUser returns project ids the caller reaches via
// org_members (i.e. projects owned by an org the user is a member of).
// Runs on the developer pool — gateway pool has REVOKE-ALL on both
// org tables. Deliberately returns ONLY ids, not full rows: the org
// membership check is the sensitive part, and the projects table is
// grant-visible to the gateway pool for the enrichment step in
// listProjectsByIDs. This split limits developer-pool query volume
// (one small select for ids) and keeps most of the read pattern on
// the gateway pool where indexes are already tuned.
func (s *TenantService) listOrgProjectIDsForUser(ctx context.Context, platformUserID string) ([]string, error) {
	rows, err := s.developerPool.Query(ctx,
		`SELECT p.id::text
		 FROM public.projects p
		 JOIN public.org_members om ON om.org_id = p.org_id
		 WHERE om.platform_user_id = $1::uuid AND p.org_id IS NOT NULL`,
		platformUserID,
	)
	if err != nil {
		return nil, fmt.Errorf("query org projects: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan org project id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate org project ids: %w", err)
	}
	return ids, nil
}

// listProjectsByIDs enriches a set of project ids into full Project
// rows using the gateway pool. Same column list as
// listDirectMemberProjects so downstream JSON marshaling is uniform.
// Preserves the caller-supplied newest-first ordering by leaning on
// created_at DESC (matches the direct-member branch), so the merged
// list stays in the shape the console expects.
func (s *TenantService) listProjectsByIDs(ctx context.Context, ids []string) ([]Project, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT p.id, p.owner_id, p.name, p.slug, p.schema_name, p.s3_bucket,
		        p.region, p.plan, p.status, p.auth_config, p.created_at,
		        p.state, p.last_active_at, p.grandfathered_until,
		        p.legacy_pro_grace_until,
		        p.org_id::text
		 FROM projects p
		 WHERE p.id = ANY($1::uuid[])
		 ORDER BY p.created_at DESC`,
		ids,
	)
	if err != nil {
		return nil, fmt.Errorf("query projects by ids: %w", err)
	}
	defer rows.Close()

	projects := make([]Project, 0, len(ids))
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.OwnerID, &p.Name, &p.Slug, &p.SchemaName, &p.S3Bucket, &p.Region, &p.Plan, &p.Status,
			&p.AuthConfig, &p.CreatedAt, &p.State, &p.LastActiveAt, &p.GrandfatheredUntil, &p.LegacyProGraceUntil,
			&p.OrgID); err != nil {
			return nil, fmt.Errorf("scan org project row: %w", err)
		}
		p.APIURL = fmt.Sprintf("https://%s.eurobase.app", p.Slug)
		p.AuthConfig = s.annotateAuthConfig(ctx, p.SchemaName, p.AuthConfig)
		projects = append(projects, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projects by ids: %w", err)
	}
	return projects, nil
}

// DeleteProject drops the tenant schema and deletes the project row.
// The caller must verify ownership before calling this.
//
// Team-tier teardown (extends M2 review bug_002): every
// project_databases row tied to this project must go before the
// DELETE FROM projects — the FK is ON DELETE RESTRICT (migration
// 000083) and doesn't care about deleted_at. Failed provisioning
// attempts leave soft-deleted rows (state='deleting', deleted_at
// set) that the 7-day sweeper hasn't yet reaped, and those alone
// were enough to block the projects delete with 23503 for the
// myteam repro. Fix: enumerate every row, best-effort provider
// teardown for any with provider_instance_id, then hard-delete
// all rows for this project.
//
// The 7-day rollback window that deprovision_sweeper enforces is
// the two-instance-restore rollback (M3): restore creates a fresh
// instance and marks the old row deleted; if the restore is bad, ops
// flips deleted_at back within 7 days to re-adopt the original.
// That contract is scoped to a *live* project. User-initiated project
// deletion collapses the entire tenant — there's no project row left
// for a re-adopted row to belong to — so we bypass the window here.
// Slug-confirm in the console is the guard against accidental delete.
//
// Known limitations, tracked as follow-ups (see PR #372 review):
//   - No advisory lock: a provision worker mid-InsertProvisioning
//     could race the list-then-delete. Fix: pg_advisory_xact_lock
//     on hashtext('project:'||projectID) taken by both paths.
//   - Steps 3-5 run as separate implicit txs: a failure between
//     HardDeleteAllByProject and DELETE FROM projects leaks the
//     provider_instance_ids. Fix: wrap in BeginTx.
//   - If ENV=production and providerRegistry is nil (or its
//     provider.Delete returns ErrUnauthorized because SCW_SECRET_KEY
//     is empty), we silently leak Scaleway instances. Fix:
//     fail-closed in production when the teardown path has no
//     working provider.
func (s *TenantService) DeleteProject(ctx context.Context, projectID string) error {
	repo := dbprovider.NewRepo(s.pool)

	// 1. Enumerate ALL project_databases rows (live + soft-deleted).
	rows, err := repo.ListAllByProject(ctx, projectID)
	if err != nil {
		return fmt.Errorf("list project_databases: %w", err)
	}

	// 2. Best-effort provider teardown for any row that has an
	// instance ID. Prior worker runs called bestEffortDelete for the
	// failed-provisioning rows, but that path can silently fail
	// (Scaleway 5xx during shutdown, etc.) — a second pass here
	// closes the leak window before we lose the ProviderInstanceID
	// by hard-deleting the row.
	//
	// If the registry isn't wired (dev / tests without SCW creds), or
	// the provider isn't registered, we skip teardown and log the
	// leaked instance IDs so ops can clean up manually. This
	// preserves the ability to delete a project even when the
	// provider layer is unavailable.
	if s.providerRegistry != nil {
		for _, r := range rows {
			if r.ProviderInstanceID == "" {
				continue
			}
			provider, err := s.providerRegistry.Get(r.Provider)
			if err != nil {
				slog.Warn("provider not registered — leaving instance for manual cleanup",
					"project_id", projectID,
					"provider", r.Provider,
					"provider_instance_id", r.ProviderInstanceID,
					"error", err)
				continue
			}
			if err := provider.Delete(ctx, r.ProviderInstanceID); err != nil {
				slog.Warn("best-effort provider delete failed during project delete — instance may be orphaned",
					"project_id", projectID,
					"provider", r.Provider,
					"provider_instance_id", r.ProviderInstanceID,
					"error", err)
			}
		}
	} else if len(rows) > 0 {
		for _, r := range rows {
			if r.ProviderInstanceID != "" {
				slog.Warn("project has provider instance but registry unavailable — instance may leak",
					"project_id", projectID,
					"provider", r.Provider,
					"provider_instance_id", r.ProviderInstanceID)
			}
		}
	}

	// 3. Hard-delete every project_databases row for the project.
	// Bypasses the 7-day sweeper's rollback window (which is a
	// safety net for accidental soft-delete, not for user-initiated
	// project deletion).
	if n, err := repo.HardDeleteAllByProject(ctx, projectID); err != nil {
		return fmt.Errorf("hard-delete project_databases rows: %w", err)
	} else if n > 0 {
		slog.Info("hard-deleted project_databases rows before project drop",
			"project_id", projectID, "row_count", n)
	}

	// 4. Call deprovision_tenant to drop the schema.
	if _, err := s.pool.Exec(ctx, `SELECT deprovision_tenant($1::uuid)`, projectID); err != nil {
		slog.Error("deprovision_tenant failed", "error", err, "project_id", projectID)
		return fmt.Errorf("deprovision tenant: %w", err)
	}

	// 5. Delete the project row (cascades to api_keys, webhooks,
	// etc.). project_databases is now clear so the RESTRICT FK
	// won't block. superseded_by FK on project_databases is SET
	// NULL, so it never blocked anything either.
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

	// Validate per-project rate-limit overrides against MaxRateLimits.
	// Refuses values that would exceed platform ceilings (SMS spend,
	// per-IP throttle bypass). Ops override via SQL when a real
	// enterprise case needs headroom. See ValidateRateLimits doc.
	if err := ValidateRateLimits(config.RateLimits); err != nil {
		return nil, fmt.Errorf("rate limits: %w", err)
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
