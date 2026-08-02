package dbprovider

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo persists project_databases rows — the platform-level record
// of which dedicated instance backs a Team-tier project. Workers
// insert during provisioning + restore; the gateway reads on the
// request path (see M2 apikey_middleware).
type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// Record is the persisted view of a project_databases row. Password
// stays sealed here — callers Open() it via a Cipher when they need
// the plaintext to build a DSN.
type Record struct {
	ID                   string
	ProjectID            string
	Provider             string
	ProviderInstanceID   string
	Host                 string
	Port                 int
	DatabaseName         string
	Username             string
	PasswordCiphertext   []byte
	PasswordNonce        []byte
	PasswordKeyVersion   int16
	Region               string
	State                State
	SupersededBy         *string
	CreatedAt            time.Time
	UpdatedAt            time.Time
	DeletedAt            *time.Time
}

// InsertProvisioning writes the initial project_databases row while
// the worker is still creating the underlying instance. State starts
// at StateProvisioning; the worker calls MarkActive once the
// provider reports StateActive.
//
// Password is sealed by the caller and passed as ciphertext + nonce
// + version — Repo has no crypto responsibility.
func (r *Repo) InsertProvisioning(
	ctx context.Context,
	projectID string,
	inst *Instance,
	provider string,
	passwordCiphertext, passwordNonce []byte,
	passwordKeyVersion int16,
) (*Record, error) {
	const q = `
		INSERT INTO public.project_databases
		    (project_id, provider, provider_instance_id, host, port,
		     database_name, username, password_ciphertext, password_nonce,
		     password_key_version, region, state)
		VALUES
		    ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING id, project_id, provider, provider_instance_id, host, port,
		          database_name, username, password_ciphertext, password_nonce,
		          password_key_version, region, state,
		          superseded_by, created_at, updated_at, deleted_at
	`
	var rec Record
	err := r.pool.QueryRow(ctx, q,
		projectID, provider, inst.ProviderID, inst.Host, inst.Port,
		inst.DBName, inst.Username, passwordCiphertext, passwordNonce,
		passwordKeyVersion, inst.Region, string(inst.State),
	).Scan(
		&rec.ID, &rec.ProjectID, &rec.Provider, &rec.ProviderInstanceID,
		&rec.Host, &rec.Port, &rec.DatabaseName, &rec.Username,
		&rec.PasswordCiphertext, &rec.PasswordNonce, &rec.PasswordKeyVersion,
		&rec.Region, &rec.State,
		&rec.SupersededBy, &rec.CreatedAt, &rec.UpdatedAt, &rec.DeletedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("dbprovider.Repo.InsertProvisioning: %w", err)
	}
	return &rec, nil
}

// UpdateState transitions a row to a new lifecycle state. Also
// refreshes host/port from the current provider view so a
// restart-that-changed-endpoint is reflected on the next request.
func (r *Repo) UpdateState(ctx context.Context, id string, state State, host string, port int) error {
	const q = `
		UPDATE public.project_databases
		   SET state = $2,
		       host  = COALESCE(NULLIF($3, ''), host),
		       port  = COALESCE(NULLIF($4, 0),  port)
		 WHERE id = $1
	`
	tag, err := r.pool.Exec(ctx, q, id, string(state), host, port)
	if err != nil {
		return fmt.Errorf("dbprovider.Repo.UpdateState: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("dbprovider.Repo.UpdateState: no rows affected")
	}
	return nil
}

// MarkFailed is a small helper for the terminal-failure path — used
// by the provisioning worker when Scaleway returns an unrecoverable
// error and we want the state visible in the console.
func (r *Repo) MarkFailed(ctx context.Context, id string) error {
	return r.UpdateState(ctx, id, StateFailed, "", 0)
}

// MarkDeleted flips the row to state='deleting', sets deleted_at.
// The deprovision sweep picks it up 7 days later.
func (r *Repo) MarkDeleted(ctx context.Context, id string) error {
	const q = `
		UPDATE public.project_databases
		   SET state = 'deleting',
		       deleted_at = COALESCE(deleted_at, now())
		 WHERE id = $1
	`
	tag, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("dbprovider.Repo.MarkDeleted: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("dbprovider.Repo.MarkDeleted: no rows affected")
	}
	return nil
}

// GetLiveByProject returns the currently-live project_databases row
// for a project (state IN provisioning|active|restoring, deleted_at
// IS NULL). Returns pgx.ErrNoRows if there isn't one — the caller
// treats that as "this is a Free/Pro project, use the shared pool."
func (r *Repo) GetLiveByProject(ctx context.Context, projectID string) (*Record, error) {
	const q = `
		SELECT id, project_id, provider, provider_instance_id, host, port,
		       database_name, username, password_ciphertext, password_nonce,
		       password_key_version, region, state,
		       superseded_by, created_at, updated_at, deleted_at
		  FROM public.project_databases
		 WHERE project_id = $1
		   AND state IN ('provisioning', 'active', 'restoring')
		   AND deleted_at IS NULL
		 LIMIT 1
	`
	var rec Record
	err := r.pool.QueryRow(ctx, q, projectID).Scan(
		&rec.ID, &rec.ProjectID, &rec.Provider, &rec.ProviderInstanceID,
		&rec.Host, &rec.Port, &rec.DatabaseName, &rec.Username,
		&rec.PasswordCiphertext, &rec.PasswordNonce, &rec.PasswordKeyVersion,
		&rec.Region, &rec.State,
		&rec.SupersededBy, &rec.CreatedAt, &rec.UpdatedAt, &rec.DeletedAt,
	)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// Get returns a single row by primary key.
func (r *Repo) Get(ctx context.Context, id string) (*Record, error) {
	const q = `
		SELECT id, project_id, provider, provider_instance_id, host, port,
		       database_name, username, password_ciphertext, password_nonce,
		       password_key_version, region, state,
		       superseded_by, created_at, updated_at, deleted_at
		  FROM public.project_databases
		 WHERE id = $1
	`
	var rec Record
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&rec.ID, &rec.ProjectID, &rec.Provider, &rec.ProviderInstanceID,
		&rec.Host, &rec.Port, &rec.DatabaseName, &rec.Username,
		&rec.PasswordCiphertext, &rec.PasswordNonce, &rec.PasswordKeyVersion,
		&rec.Region, &rec.State,
		&rec.SupersededBy, &rec.CreatedAt, &rec.UpdatedAt, &rec.DeletedAt,
	)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// ListDeprovisionCandidates returns rows past the 7-day rollback
// window that the deprovision worker should tear down.
func (r *Repo) ListDeprovisionCandidates(ctx context.Context, olderThan time.Duration) ([]Record, error) {
	const q = `
		SELECT id, project_id, provider, provider_instance_id, host, port,
		       database_name, username, password_ciphertext, password_nonce,
		       password_key_version, region, state,
		       superseded_by, created_at, updated_at, deleted_at
		  FROM public.project_databases
		 WHERE deleted_at IS NOT NULL
		   AND deleted_at < now() - $1::interval
		 ORDER BY deleted_at ASC
		 LIMIT 100
	`
	// pgx serialises time.Duration as an interval only when passed
	// through a string cast, so format it explicitly.
	interval := fmt.Sprintf("%d seconds", int(olderThan.Seconds()))
	rows, err := r.pool.Query(ctx, q, interval)
	if err != nil {
		return nil, fmt.Errorf("dbprovider.Repo.ListDeprovisionCandidates: %w", err)
	}
	defer rows.Close()
	out := make([]Record, 0, 16)
	for rows.Next() {
		var rec Record
		if err := rows.Scan(
			&rec.ID, &rec.ProjectID, &rec.Provider, &rec.ProviderInstanceID,
			&rec.Host, &rec.Port, &rec.DatabaseName, &rec.Username,
			&rec.PasswordCiphertext, &rec.PasswordNonce, &rec.PasswordKeyVersion,
			&rec.Region, &rec.State,
			&rec.SupersededBy, &rec.CreatedAt, &rec.UpdatedAt, &rec.DeletedAt,
		); err != nil {
			return nil, fmt.Errorf("dbprovider.Repo.ListDeprovisionCandidates: scan: %w", err)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// HardDelete removes a row entirely — called by the deprovision
// worker AFTER the provider-side instance has been destroyed.
func (r *Repo) HardDelete(ctx context.Context, id string) error {
	const q = `DELETE FROM public.project_databases WHERE id = $1`
	tag, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("dbprovider.Repo.HardDelete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("dbprovider.Repo.HardDelete: no rows affected")
	}
	return nil
}

// ErrNoLiveInstance is returned by the caller when GetLiveByProject
// yields pgx.ErrNoRows — a semantic wrapper so callers don't have
// to import pgx to switch on it.
var ErrNoLiveInstance = errors.New("dbprovider: no live instance for project")

// Wrap wraps pgx.ErrNoRows into ErrNoLiveInstance so callers can
// distinguish "no row" from a real DB failure.
func Wrap(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoLiveInstance
	}
	return err
}
