// Package upgrade orchestrates the Free/Pro → Team tier upgrade
// path. Callers request an upgrade via Service.RequestUpgrade;
// the actual data migration + cutover runs asynchronously in
// internal/workers/upgrade_project.go.
package upgrade

import (
	"context"
	"errors"
	"fmt"

	"github.com/eurobase/euroback/internal/jobs"
	"github.com/eurobase/euroback/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// State enumerates the project_upgrades.state values. Values are
// stringly-typed at the DB layer (no CHECK constraint) so we can
// add states without a migration; the Go const block is the source
// of truth for the ones the orchestrator uses today.
type State string

const (
	StateRequested    State = "requested"
	StateProvisioning State = "provisioning"
	StateCopying      State = "copying"
	StateCuttingOver  State = "cutting_over"
	StateLive         State = "live"
	StateConfirmed    State = "confirmed"
	StateFailed       State = "failed"
)

// Errors surfaced by RequestUpgrade. Callers can distinguish
// user-facing failures (return 4xx) from platform errors
// (return 5xx) via errors.Is / errors.As.
var (
	ErrAlreadyTeam                = errors.New("project is already on a Team tier")
	ErrUpgradeInFlight            = errors.New("an upgrade is already in progress for this project")
	ErrTeamBetaAccessMissing      = errors.New("Team-tier upgrade requires team_beta_access")
	ErrLegalTeamBetaAccessMissing = errors.New("Legal-Team-tier upgrade requires legal_team_beta_access")
	ErrProjectNotFound            = errors.New("project not found")
	ErrUnsupportedPlan            = errors.New("target plan is not a Team tier")

	// Returned by admin operations that reference a specific upgrade ID.
	ErrUpgradeNotFound = errors.New("upgrade not found")
	// Returned by Abort/Confirm when the upgrade is already in a
	// terminal state — the operation is a no-op.
	ErrUpgradeTerminal = errors.New("upgrade is already in a terminal state")
)

// Service owns the guard logic + River enqueue for an upgrade
// request. The worker (internal/workers/upgrade_project.go) drives
// the state machine.
type Service struct {
	// pool is the developer pool. Every write here goes through
	// eurobase_developer (so it lifts to eurobase_migrator inside
	// tx via SET LOCAL ROLE) — project_upgrades is developer-only,
	// per migration 000117's grants.
	pool *pgxpool.Pool
	// river client enqueues UpgradeProjectArgs. Kept as an interface
	// alias for testability (unit tests use a fake).
	riverClient *river.Client[pgx.Tx]
}

// NewService constructs the orchestrator. Both pool and riverClient
// must be non-nil.
func NewService(pool *pgxpool.Pool, rc *river.Client[pgx.Tx]) *Service {
	return &Service{pool: pool, riverClient: rc}
}

// RequestUpgrade validates the request and enqueues the async
// worker. On success returns the new project_upgrades.id. On
// caller-facing failure returns one of the exported Err* sentinels;
// on transient DB failure returns a wrapped error.
//
// Guards (all fail-fast, no side effects on failure):
//
//   - Project must exist and be readable via the developer pool.
//   - Project's current plan must be free or pro (Team → higher
//     isn't a supported delta here; use tenant/service.go's
//     create-time path instead).
//   - toPlan must be team or legal_team. Anything else 4xx.
//   - Owner must have team_beta_access = true. Same gate as
//     CreateProject(plan='team') — matches beta posture.
//   - No prior upgrade row for this project may be in a non-terminal
//     state. The partial unique index ux_project_upgrades_active
//     enforces this at write time; we pre-check to return a nicer
//     error than "unique violation".
func (s *Service) RequestUpgrade(ctx context.Context, projectID, adminID, toPlan string) (upgradeID string, err error) {
	if toPlan != "team" && toPlan != "legal_team" {
		return "", ErrUnsupportedPlan
	}

	// Guard 1: project exists + current plan.
	var currentPlan string
	var ownerID string
	if err := s.pool.QueryRow(ctx,
		`SELECT plan, owner_id FROM public.projects WHERE id = $1`,
		projectID,
	).Scan(&currentPlan, &ownerID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrProjectNotFound
		}
		return "", fmt.Errorf("lookup project: %w", err)
	}
	if currentPlan != "free" && currentPlan != "pro" {
		return "", ErrAlreadyTeam
	}

	// Guard 2: owner has the right beta flag for the target plan.
	// Matches CreateProject's dispatch (tenant/service.go): team →
	// team_beta_access, legal_team → legal_team_beta_access. Two
	// separate flags because Legal Team ships a different SKU with
	// stricter compliance obligations (BSI C5 dossier, §203 StGB
	// staff declarations) — a customer approved for the base beta
	// isn't automatically approved for the legal-tech beta.
	switch toPlan {
	case "team":
		ok, err := tenant.UserHasTeamBetaAccess(ctx, s.pool, ownerID)
		if err != nil {
			return "", fmt.Errorf("lookup team beta: %w", err)
		}
		if !ok {
			return "", ErrTeamBetaAccessMissing
		}
	case "legal_team":
		ok, err := tenant.UserHasLegalTeamBetaAccess(ctx, s.pool, ownerID)
		if err != nil {
			return "", fmt.Errorf("lookup legal_team beta: %w", err)
		}
		if !ok {
			return "", ErrLegalTeamBetaAccessMissing
		}
	}

	// Guard 3: no in-flight upgrade. The partial unique index would
	// reject at INSERT time, but a friendly error beats a raw
	// constraint violation.
	var inFlight bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (
		     SELECT 1 FROM public.project_upgrades
		      WHERE project_id = $1
		        AND state NOT IN ('confirmed', 'failed')
		 )`,
		projectID,
	).Scan(&inFlight); err != nil {
		return "", fmt.Errorf("check in-flight upgrade: %w", err)
	}
	if inFlight {
		return "", ErrUpgradeInFlight
	}

	// Insert the row + enqueue the job in a single transaction so we
	// never end up with an orphan row (row inserted but River queue
	// down) or an orphan job (job enqueued but row insert failed).
	// River's tx-friendly Insert signature makes this trivial.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// SET LOCAL ROLE eurobase_migrator so the INSERT runs with
	// full ownership on public.project_upgrades — matches the
	// pattern used elsewhere for platform-side writes.
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE eurobase_migrator`); err != nil {
		return "", fmt.Errorf("set role: %w", err)
	}

	if err := tx.QueryRow(ctx,
		`INSERT INTO public.project_upgrades
		     (project_id, from_plan, to_plan, state, triggered_by)
		 VALUES ($1, $2, $3, 'requested', $4)
		 RETURNING id`,
		projectID, currentPlan, toPlan, nullableAdminID(adminID),
	).Scan(&upgradeID); err != nil {
		// Map ux_project_upgrades_active violation to the friendly
		// ErrUpgradeInFlight rather than surfacing a raw 23505.
		// Handles the guard-3 race: two concurrent RequestUpgrade
		// calls both pass the EXISTS pre-check and both try to
		// INSERT — the partial unique index rejects the second, and
		// without this the client sees a wrapped constraint error
		// instead of the friendly guard message.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) &&
			pgErr.Code == "23505" &&
			pgErr.ConstraintName == "ux_project_upgrades_active" {
			return "", ErrUpgradeInFlight
		}
		return "", fmt.Errorf("insert project_upgrades: %w", err)
	}

	// Enqueue inside the same tx so the row-insert + job-insert
	// commit atomically.
	if _, err := s.riverClient.InsertTx(ctx, tx, jobs.UpgradeProjectArgs{
		UpgradeID: upgradeID,
	}, nil); err != nil {
		return "", fmt.Errorf("enqueue upgrade job: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit tx: %w", err)
	}
	return upgradeID, nil
}

// UpgradeRecord is the read-model returned by GetUpgrade + ListUpgrades.
// Every field is populated verbatim from project_upgrades; nullable
// columns use pointers so JSON-encoding shows null rather than the
// zero value.
type UpgradeRecord struct {
	ID                string  `json:"id"`
	ProjectID         string  `json:"project_id"`
	FromPlan          string  `json:"from_plan"`
	ToPlan            string  `json:"to_plan"`
	State             string  `json:"state"`
	ProjectDatabaseID *string `json:"project_database_id,omitempty"`
	BytesCopied       int64   `json:"bytes_copied"`
	TablesCopied      int     `json:"tables_copied"`
	StartedAt         string  `json:"started_at"`
	ProvisionedAt     *string `json:"provisioned_at,omitempty"`
	CopiedAt          *string `json:"copied_at,omitempty"`
	CutoverAt         *string `json:"cutover_at,omitempty"`
	ConfirmedAt       *string `json:"confirmed_at,omitempty"`
	FailedAt          *string `json:"failed_at,omitempty"`
	Error             *string `json:"error,omitempty"`
	TriggeredBy       *string `json:"triggered_by,omitempty"`
	UpdatedAt         string  `json:"updated_at"`
}

// GetUpgrade returns a single upgrade by ID.
func (s *Service) GetUpgrade(ctx context.Context, id string) (*UpgradeRecord, error) {
	var r UpgradeRecord
	var startedAt, updatedAt string
	err := s.pool.QueryRow(ctx,
		`SELECT id::text, project_id::text, from_plan, to_plan, state,
		        project_database_id::text, bytes_copied, tables_copied,
		        started_at::text, provisioned_at::text, copied_at::text,
		        cutover_at::text, confirmed_at::text, failed_at::text,
		        error, triggered_by::text, updated_at::text
		   FROM public.project_upgrades WHERE id = $1`,
		id,
	).Scan(&r.ID, &r.ProjectID, &r.FromPlan, &r.ToPlan, &r.State,
		&r.ProjectDatabaseID, &r.BytesCopied, &r.TablesCopied,
		&startedAt, &r.ProvisionedAt, &r.CopiedAt,
		&r.CutoverAt, &r.ConfirmedAt, &r.FailedAt,
		&r.Error, &r.TriggeredBy, &updatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUpgradeNotFound
		}
		return nil, fmt.Errorf("get upgrade: %w", err)
	}
	r.StartedAt = startedAt
	r.UpdatedAt = updatedAt
	return &r, nil
}

// ListUpgrades returns the most recent `limit` upgrades ordered by
// started_at DESC. limit is clamped to [1, 500].
func (s *Service) ListUpgrades(ctx context.Context, limit int) ([]UpgradeRecord, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, project_id::text, from_plan, to_plan, state,
		        project_database_id::text, bytes_copied, tables_copied,
		        started_at::text, provisioned_at::text, copied_at::text,
		        cutover_at::text, confirmed_at::text, failed_at::text,
		        error, triggered_by::text, updated_at::text
		   FROM public.project_upgrades
		  ORDER BY started_at DESC
		  LIMIT $1`,
		limit)
	if err != nil {
		return nil, fmt.Errorf("list upgrades: %w", err)
	}
	defer rows.Close()

	var out []UpgradeRecord
	for rows.Next() {
		var r UpgradeRecord
		var startedAt, updatedAt string
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.FromPlan, &r.ToPlan, &r.State,
			&r.ProjectDatabaseID, &r.BytesCopied, &r.TablesCopied,
			&startedAt, &r.ProvisionedAt, &r.CopiedAt,
			&r.CutoverAt, &r.ConfirmedAt, &r.FailedAt,
			&r.Error, &r.TriggeredBy, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan upgrade row: %w", err)
		}
		r.StartedAt = startedAt
		r.UpdatedAt = updatedAt
		out = append(out, r)
	}
	return out, rows.Err()
}

// AbortUpgrade marks a non-terminal upgrade as failed. Clears
// projects.maintenance_mode as a side effect so the project isn't
// stuck at 503 after ops aborts a stuck job. No-op with
// ErrUpgradeTerminal if the upgrade is already confirmed/failed.
//
// This does NOT tear down the dedicated managed-PG instance the
// upgrade may have provisioned — the project_databases row stays
// live so ops can inspect it. A separate follow-up call to the
// deprovision worker handles cleanup.
func (s *Service) AbortUpgrade(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE eurobase_migrator`); err != nil {
		return fmt.Errorf("set migrator role: %w", err)
	}

	// Find the row + verify it isn't already terminal, and grab the
	// project_id so we can clear maintenance_mode below.
	var projectID, state string
	if err := tx.QueryRow(ctx,
		`SELECT project_id::text, state
		   FROM public.project_upgrades WHERE id = $1`,
		id,
	).Scan(&projectID, &state); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUpgradeNotFound
		}
		return fmt.Errorf("lookup upgrade: %w", err)
	}
	if state == string(StateConfirmed) || state == string(StateFailed) {
		return ErrUpgradeTerminal
	}

	if _, err := tx.Exec(ctx,
		`UPDATE public.project_upgrades
		    SET state = 'failed', failed_at = now(),
		        error = 'aborted by admin'
		  WHERE id = $1 AND state NOT IN ('confirmed', 'failed')`,
		id,
	); err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}

	// Clear maintenance mode so the project isn't stuck at 503.
	if _, err := tx.Exec(ctx,
		`UPDATE public.projects SET maintenance_mode = false WHERE id = $1`,
		projectID,
	); err != nil {
		return fmt.Errorf("clear maintenance: %w", err)
	}

	return tx.Commit(ctx)
}

// ConfirmUpgrade transitions a live upgrade to confirmed (skips the
// 7-day cool-down before the sweeper does the same). Fails with
// ErrUpgradeTerminal if already confirmed/failed.
//
// Note: this PR marks the row confirmed but does NOT yet purge the
// source data from the shared cluster. The confirmation sweeper (this
// PR) also just marks state=confirmed since the copying step in the
// upgrade worker is currently stubbed — nothing was copied, so nothing
// to purge. The real cleanup lands with the copy work in a follow-up.
func (s *Service) ConfirmUpgrade(ctx context.Context, id string) error {
	tag, err := s.execAndCount(ctx,
		`UPDATE public.project_upgrades
		    SET state = 'confirmed', confirmed_at = now()
		  WHERE id = $1 AND state = 'live'`,
		id)
	if err != nil {
		return fmt.Errorf("confirm upgrade: %w", err)
	}
	if tag == 0 {
		// Row exists but not in 'live' — or doesn't exist. Distinguish
		// so callers see the right error.
		var state string
		if err := s.pool.QueryRow(ctx,
			`SELECT state FROM public.project_upgrades WHERE id = $1`,
			id,
		).Scan(&state); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrUpgradeNotFound
			}
			return fmt.Errorf("lookup upgrade state: %w", err)
		}
		return ErrUpgradeTerminal
	}
	return nil
}

// execAndCount runs an UPDATE inside a tx with SET LOCAL ROLE
// eurobase_migrator and returns rows-affected. Same pattern the
// worker uses; kept internal to this file so both share the
// migrator-role guarantee.
func (s *Service) execAndCount(ctx context.Context, sql string, args ...any) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE eurobase_migrator`); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// nullableAdminID returns nil for empty string so the FK to
// platform_users doesn't try to resolve "". System-driven upgrades
// (none today, but Phase 2 auto-upgrade after checkout) will pass
// "" and get NULL triggered_by.
func nullableAdminID(id string) any {
	if id == "" {
		return nil
	}
	return id
}
