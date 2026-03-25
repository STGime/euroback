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

// TenantService encapsulates database operations for tenant/project management.
type TenantService struct {
	pool        *pgxpool.Pool
	riverClient *river.Client[pgx.Tx]
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
	return &p, nil
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

// UpdateAuthConfig updates the auth_config for a project, verifying ownership.
func (s *TenantService) UpdateAuthConfig(ctx context.Context, projectID, ownerID string, config AuthConfig) error {
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
