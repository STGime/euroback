package email

// Per-project SMTP sender (BYO custom SMTP — #235 Part 1).
//
// A project that has set up its own SMTP provider sends auth + transactional
// email through that provider instead of the shared platform TEM. This unlocks
// the platform EmailsPerHour ceiling (#227 / #234) and lets the project own
// its sender reputation.
//
// The password is sealed at rest with the per-tenant HKDF key (same pattern
// as edge_functions env_vars in #206) so a DB dump can't reveal credentials.
//
// The non-secret columns (host, port, from_email, ...) live alongside the
// sealed trio in public.project_email_senders so a single SELECT either
// returns a fully-configured sender or none — the absence of a row is the
// "use platform" signal.

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/vault"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SenderEncryption enumerates the supported wire encryption modes. The
// values match the CHECK constraint in migration 000071.
type SenderEncryption string

const (
	EncryptionSTARTTLS SenderEncryption = "starttls"
	EncryptionTLS      SenderEncryption = "tls"
	EncryptionNone     SenderEncryption = "none"
)

// validEncryptions is the closed set the migration's CHECK constraint
// accepts. Kept in sync by hand — the migration has the same list.
var validEncryptions = map[SenderEncryption]struct{}{
	EncryptionSTARTTLS: {},
	EncryptionTLS:      {},
	EncryptionNone:     {},
}

// ProjectSender is the per-project SMTP config the gateway uses to send
// auth + transactional email when the project has BYO-SMTP enabled.
// The Password is the decrypted plaintext, populated only by LoadForSend
// (the read path used by the actual email send) — the CRUD/list-shaped
// reads use LoadConfig which leaves it empty so the console can never
// see a project's SMTP password.
type ProjectSender struct {
	ProjectID   string           `json:"project_id"`
	Host        string           `json:"host"`
	Port        int              `json:"port"`
	Username    string           `json:"username,omitempty"`
	FromEmail   string           `json:"from_email"`
	FromName    string           `json:"from_name,omitempty"`
	Encryption  SenderEncryption `json:"encryption"`
	Password    string           `json:"-"` // never JSON-marshalled
	HasPassword bool             `json:"has_password"`
	VerifiedAt  *time.Time       `json:"verified_at,omitempty"`
	LastError   string           `json:"last_error,omitempty"`
	LastErrorAt *time.Time       `json:"last_error_at,omitempty"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`

	// SovereigntyWarning is set when the configured host matches a known
	// US-based provider. Surfaced to the console so the operator can
	// see the sovereignty consequence of their choice; not a hard block.
	SovereigntyWarning string `json:"sovereignty_warning,omitempty"`
}

// UpsertRequest is the payload the API accepts for PUT
// /platform/projects/{id}/email-sender. Password is the plaintext the
// caller wants sealed; empty Password on an existing sender means
// "keep the password that's already there" (so the console doesn't
// have to re-prompt for it on every edit).
type UpsertRequest struct {
	Host       string           `json:"host"`
	Port       int              `json:"port"`
	Username   string           `json:"username"`
	FromEmail  string           `json:"from_email"`
	FromName   string           `json:"from_name"`
	Encryption SenderEncryption `json:"encryption"`
	Password   string           `json:"password"`
}

// SenderService is the CRUD + read-path for per-project SMTP senders.
// The vault dependency is the per-tenant sealing surface (#206).
type SenderService struct {
	pool  *pgxpool.Pool
	vault *vault.VaultService
}

// NewSenderService wires the dependencies. vault may be nil in dev
// without VAULT_ENCRYPTION_KEY — in that case Upsert refuses to seal a
// password (returns ErrVaultNotConfigured), but loading a sender with
// NULL password columns still works for providers that don't need
// authentication.
func NewSenderService(pool *pgxpool.Pool, vaultSvc *vault.VaultService) *SenderService {
	return &SenderService{pool: pool, vault: vaultSvc}
}

// ErrNotConfigured is returned by Load* when a project has no sender
// row. Callers translate this into "fall back to the platform sender",
// not an error response.
var ErrNotConfigured = errors.New("project has no custom email sender")

// ErrVaultNotConfigured is returned by Upsert when a password is
// provided but VAULT_ENCRYPTION_KEY is not set on the gateway. Surfaces
// in the console as a setup-blocker so the operator fixes the secret
// before continuing.
var ErrVaultNotConfigured = errors.New("vault not configured — cannot seal SMTP password")

// ErrSenderNotVerified is returned by LoadForSend when a sender exists
// but has not been verified via a successful test-send. The send-path
// MUST fall back to platform; the console MUST surface the unverified
// state so the operator runs the test.
var ErrSenderNotVerified = errors.New("project email sender not verified — run a test send first")

// LoadConfig reads the non-secret config for the console. The Password
// field is left empty; HasPassword indicates whether sealed bytes
// exist. Returns ErrNotConfigured when no row exists.
func (s *SenderService) LoadConfig(ctx context.Context, projectID string) (*ProjectSender, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT host, port, COALESCE(username,''), from_email, COALESCE(from_name,''),
		        encryption, (password_blob IS NOT NULL) AS has_password,
		        verified_at, COALESCE(last_error,''), last_error_at,
		        created_at, updated_at
		 FROM public.project_email_senders
		 WHERE project_id = $1`, projectID)

	out := &ProjectSender{ProjectID: projectID}
	if err := row.Scan(
		&out.Host, &out.Port, &out.Username, &out.FromEmail, &out.FromName,
		&out.Encryption, &out.HasPassword,
		&out.VerifiedAt, &out.LastError, &out.LastErrorAt,
		&out.CreatedAt, &out.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotConfigured
		}
		return nil, fmt.Errorf("load project email sender config: %w", err)
	}
	out.SovereigntyWarning = sovereigntyWarningFor(out.Host)
	return out, nil
}

// LoadForSend reads the sender + decrypts the password for the actual
// email-send path. Returns ErrSenderNotVerified for unverified senders
// — the caller MUST treat that as "fall back to platform" rather than
// using an unverified sender (the whole point of the verify-first flow
// is to fail loudly at setup, not silently at first signup).
func (s *SenderService) LoadForSend(ctx context.Context, projectID string) (*ProjectSender, error) {
	var (
		out                   ProjectSender
		passwordBlob, nonce   []byte
		passwordKeyVersion    *int16
	)
	out.ProjectID = projectID
	err := s.pool.QueryRow(ctx,
		`SELECT host, port, COALESCE(username,''), from_email, COALESCE(from_name,''),
		        encryption,
		        password_blob, password_nonce, password_key_version,
		        verified_at, COALESCE(last_error,''), last_error_at,
		        created_at, updated_at
		 FROM public.project_email_senders
		 WHERE project_id = $1`, projectID,
	).Scan(
		&out.Host, &out.Port, &out.Username, &out.FromEmail, &out.FromName,
		&out.Encryption,
		&passwordBlob, &nonce, &passwordKeyVersion,
		&out.VerifiedAt, &out.LastError, &out.LastErrorAt,
		&out.CreatedAt, &out.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotConfigured
		}
		return nil, fmt.Errorf("load project email sender: %w", err)
	}
	if out.VerifiedAt == nil {
		return &out, ErrSenderNotVerified
	}
	if len(passwordBlob) > 0 {
		schemaName, err := s.schemaName(ctx, projectID)
		if err != nil {
			return nil, err
		}
		if s.vault == nil || !s.vault.Configured() {
			return nil, fmt.Errorf("project has sealed SMTP password but vault not configured")
		}
		plaintext, err := s.vault.OpenForTenant(ctx, schemaName, passwordBlob, nonce, derefInt16(passwordKeyVersion))
		if err != nil {
			return nil, fmt.Errorf("decrypt SMTP password: %w", err)
		}
		out.Password = plaintext
		out.HasPassword = true
	}
	return &out, nil
}

// Upsert writes the sender config + seals a new password if provided.
// On an existing row, an empty Password preserves the stored password
// — the console doesn't have to re-prompt every edit. Updates always
// reset verified_at to NULL: any config change invalidates the
// previous test-send and the operator must re-verify.
func (s *SenderService) Upsert(ctx context.Context, projectID string, req UpsertRequest) (*ProjectSender, error) {
	if err := validateUpsert(req); err != nil {
		return nil, err
	}

	// Seal new password, or carry over the existing sealed columns.
	var (
		blob, nonce        []byte
		version            int16
		preservePassword   bool
	)
	if req.Password != "" {
		schemaName, err := s.schemaName(ctx, projectID)
		if err != nil {
			return nil, err
		}
		if s.vault == nil || !s.vault.Configured() {
			return nil, ErrVaultNotConfigured
		}
		blob, nonce, version, err = s.vault.SealForTenant(ctx, schemaName, req.Password)
		if err != nil {
			return nil, fmt.Errorf("seal SMTP password: %w", err)
		}
	} else {
		preservePassword = true
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin upsert: %w", err)
	}
	defer tx.Rollback(ctx)

	if preservePassword {
		// UPSERT keeping existing sealed columns. INSERT path writes
		// NULL trio (no password) — the all-or-nothing CHECK is fine.
		_, err = tx.Exec(ctx,
			`INSERT INTO public.project_email_senders
			 (project_id, host, port, username, from_email, from_name, encryption,
			  verified_at, last_error, last_error_at)
			 VALUES ($1,$2,$3,NULLIF($4,''),$5,NULLIF($6,''),$7,NULL,NULL,NULL)
			 ON CONFLICT (project_id) DO UPDATE SET
			   host = EXCLUDED.host,
			   port = EXCLUDED.port,
			   username = EXCLUDED.username,
			   from_email = EXCLUDED.from_email,
			   from_name = EXCLUDED.from_name,
			   encryption = EXCLUDED.encryption,
			   verified_at = NULL,
			   last_error = NULL,
			   last_error_at = NULL`,
			projectID, req.Host, req.Port, req.Username, req.FromEmail, req.FromName, string(req.Encryption),
		)
	} else {
		_, err = tx.Exec(ctx,
			`INSERT INTO public.project_email_senders
			 (project_id, host, port, username, from_email, from_name, encryption,
			  password_blob, password_nonce, password_key_version,
			  verified_at, last_error, last_error_at)
			 VALUES ($1,$2,$3,NULLIF($4,''),$5,NULLIF($6,''),$7,$8,$9,$10,NULL,NULL,NULL)
			 ON CONFLICT (project_id) DO UPDATE SET
			   host = EXCLUDED.host,
			   port = EXCLUDED.port,
			   username = EXCLUDED.username,
			   from_email = EXCLUDED.from_email,
			   from_name = EXCLUDED.from_name,
			   encryption = EXCLUDED.encryption,
			   password_blob = EXCLUDED.password_blob,
			   password_nonce = EXCLUDED.password_nonce,
			   password_key_version = EXCLUDED.password_key_version,
			   verified_at = NULL,
			   last_error = NULL,
			   last_error_at = NULL`,
			projectID, req.Host, req.Port, req.Username, req.FromEmail, req.FromName, string(req.Encryption),
			blob, nonce, version,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("upsert project email sender: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit upsert: %w", err)
	}
	return s.LoadConfig(ctx, projectID)
}

// Delete clears the sender so the project falls back to the platform
// sender. Idempotent — deleting a non-existent sender returns nil.
func (s *SenderService) Delete(ctx context.Context, projectID string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM public.project_email_senders WHERE project_id = $1`, projectID)
	if err != nil {
		return fmt.Errorf("delete project email sender: %w", err)
	}
	return nil
}

// MarkVerified bumps verified_at and clears the last-error fields.
// Called by the test-send handler after a successful send.
func (s *SenderService) MarkVerified(ctx context.Context, projectID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE public.project_email_senders
		 SET verified_at = now(), last_error = NULL, last_error_at = NULL
		 WHERE project_id = $1`, projectID)
	return err
}

// MarkFailed records a test-send failure on the sender row. Surfaced
// in the console so an operator can see exactly what the SMTP server
// said without re-running the test.
func (s *SenderService) MarkFailed(ctx context.Context, projectID string, errMsg string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE public.project_email_senders
		 SET last_error = $2, last_error_at = now(), verified_at = NULL
		 WHERE project_id = $1`, projectID, errMsg)
	return err
}

func (s *SenderService) schemaName(ctx context.Context, projectID string) (string, error) {
	var schemaName string
	err := s.pool.QueryRow(ctx,
		`SELECT schema_name FROM public.projects WHERE id = $1`, projectID,
	).Scan(&schemaName)
	if err != nil {
		return "", fmt.Errorf("resolve schema_name for project %s: %w", projectID, err)
	}
	return schemaName, nil
}

func derefInt16(p *int16) int16 {
	if p == nil {
		return 0
	}
	return *p
}

// validateUpsert is the shape-check for an incoming UpsertRequest.
// Catches operator typos (bad port, missing host, malformed from_email)
// before the seal/DB write so errors surface with a clear message.
func validateUpsert(r UpsertRequest) error {
	if strings.TrimSpace(r.Host) == "" {
		return errors.New("host is required")
	}
	if r.Port < 1 || r.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535, got %d", r.Port)
	}
	if strings.TrimSpace(r.FromEmail) == "" {
		return errors.New("from_email is required")
	}
	if _, err := mail.ParseAddress(r.FromEmail); err != nil {
		return fmt.Errorf("from_email is not a valid address: %w", err)
	}
	if _, ok := validEncryptions[r.Encryption]; !ok {
		return fmt.Errorf("encryption must be one of starttls/tls/none, got %q", r.Encryption)
	}
	return nil
}

// sovereigntyWarningFor checks the host against a small list of known
// US-based providers and returns a one-line advisory if matched. Empty
// string means "no concern surfaced".
//
// We surface a warning rather than block: a project legitimately may
// choose a US provider (their email is their data, they own the
// sovereignty trade-off). The CLAUDE.md "no US cloud services" rule
// is platform-side; tenant-owned config is the tenant's call.
//
// The list is intentionally short and well-known. Expanding it forever
// is a losing game — the goal is to catch the obvious case where an
// operator paste a "smtp.sendgrid.net" config and didn't think about
// it, not to be a comprehensive provider classifier.
func sovereigntyWarningFor(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	type pair struct{ match, name string }
	usProviders := []pair{
		{"sendgrid.net", "SendGrid (US)"},
		{"sendgrid.com", "SendGrid (US)"},
		{"mailgun.org", "Mailgun (US)"},
		{"mailgun.net", "Mailgun (US)"},
		{"postmarkapp.com", "Postmark (US)"},
		{"amazonaws.com", "Amazon SES (US)"},
		{"sparkpostmail.com", "SparkPost (US)"},
	}
	for _, p := range usProviders {
		if strings.HasSuffix(h, p.match) {
			return fmt.Sprintf("Host matches %s — a US provider. Data sent through this SMTP leaves the EU jurisdiction; consider an EU-based provider (Scaleway TEM, Brevo, Mailjet, Mailtrap EU) to preserve sovereignty.", p.name)
		}
	}
	return ""
}

