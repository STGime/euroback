package tenant

// Team-tier organizations service — CRUD on public.organizations +
// public.org_members, plus the OIDC client_secret encrypt/decrypt
// helpers. All operations run against the eurobase_developer pool
// (migration 000114 grants developer full DML on both tables;
// gateway has none, so a SDK-runtime SQLi cannot enumerate roster).
//
// MVP scope decisions:
//   * SSO callback only accepts users already present as
//     org_members. Auto-provisioning based on primary_email_domain
//     requires DNS-TXT verification (per migration 000114's
//     SECURITY comment) which is a follow-up PR. This closes the
//     domain-squatting hazard without a new migration.
//   * client_secret encrypted directly in oidc_config JSONB via
//     AES-256-GCM using PLATFORM_ENCRYPTION_KEY. The migration's
//     aspirational `vault://` shape is a Phase 2 refactor once the
//     tenant vault grows a platform-scope surface.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Public errors — callers pattern-match to shape HTTP responses.
var (
	ErrOrgNotFound      = errors.New("organization not found")
	ErrOrgMemberMissing = errors.New("caller is not a member of the organization")
	ErrOrgAdminOnly     = errors.New("operation requires org admin role")
	ErrOIDCNotConfigured = errors.New("organization has no OIDC configuration")
	ErrMemberExists      = errors.New("member already exists in organization")
	ErrUserNotFound      = errors.New("no platform_users row matches the email")
)

// Field bounds match migration 000114's CHECK constraints — belt +
// braces so a bypassed validator lands as a 400 not a 500.
const (
	orgMaxNameLen   = 200
	orgMaxDomainLen = 253 // DNS RFC ceiling
)

// Role constants — match the CHECK on org_members.role. Kept as
// exported strings so callers can compare without importing enum
// pointers.
const (
	RoleOrgAdmin  = "admin"
	RoleOrgMember = "member"
)

// InvitedVia constants — match the CHECK on org_members.invited_via.
const (
	InvitedViaManual = "manual"
	InvitedViaSSO    = "sso"
)

// Org is one row of public.organizations, JSON-safe for the CRUD
// endpoints. The oidc_config JSONB field is split into typed pieces
// on read (see OIDCConfig) so the handler never has to touch raw
// JSONB.
type Org struct {
	ID                  string     `json:"id"`
	Name                string     `json:"name"`
	PrimaryEmailDomain  *string    `json:"primary_email_domain"`
	OIDCConfigured      bool       `json:"oidc_configured"`
	CreatedByID         *string    `json:"created_by_id"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// OrgWithMembership pairs an org with the caller's role — the shape
// returned from GET /platform/orgs (the listing endpoint) so the
// UI can render admin-only affordances without a second round-trip.
type OrgWithMembership struct {
	Org
	Role        string    `json:"role"`
	MemberSince time.Time `json:"member_since"`
}

// OrgMember is one row of public.org_members plus the joined
// platform_users email for display.
type OrgMember struct {
	ID             string    `json:"id"`
	PlatformUserID string    `json:"platform_user_id"`
	Email          string    `json:"email"`
	Role           string    `json:"role"`
	InvitedVia     string    `json:"invited_via"`
	CreatedAt      time.Time `json:"created_at"`
}

// OIDCConfig is the parsed shape of organizations.oidc_config JSONB.
// The ClientSecret is populated only on writes / internal reads;
// the CRUD endpoints NEVER return it in JSON responses (see
// OIDCConfigPublic).
type OIDCConfig struct {
	Provider     string `json:"provider"`
	Issuer       string `json:"issuer"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"-"` // in-memory only, never persisted plaintext
	RedirectURL  string `json:"redirect_url"`
}

// OIDCConfigPublic is what the CRUD endpoints return — everything
// EXCEPT the client_secret. Callers who need to display the config
// panel get provider/issuer/client_id/redirect but never the
// secret.
type OIDCConfigPublic struct {
	Provider    string `json:"provider"`
	Issuer      string `json:"issuer"`
	ClientID    string `json:"client_id"`
	RedirectURL string `json:"redirect_url"`
	// ClientSecretSet is true iff the sealed ciphertext is
	// non-empty in the DB. Lets the UI show "•••••••• (set)" vs
	// "not configured" without leaking the value.
	ClientSecretSet bool `json:"client_secret_set"`
}

// storedOIDCConfig is what actually lives in the JSONB column —
// the client_secret is base64-encoded AES-256-GCM ciphertext (with
// the nonce prepended), NOT plaintext.
type storedOIDCConfig struct {
	Provider           string `json:"provider"`
	Issuer             string `json:"issuer"`
	ClientID           string `json:"client_id"`
	ClientSecretSealed string `json:"client_secret_sealed"`
	RedirectURL        string `json:"redirect_url"`
}

// OrgsService bundles the CRUD + SSO-config surface. Constructed
// once at gateway startup and shared across requests.
type OrgsService struct {
	pool    *pgxpool.Pool
	seal    *aesGCMSeal
}

// NewOrgsService constructs the service. encryptionKey MUST be
// exactly 32 bytes (raw, not hex-encoded) for AES-256; empty is
// legal in dev but disables SSO config writes (they return an error
// so misconfigured envs fail loud rather than storing plaintext).
func NewOrgsService(pool *pgxpool.Pool, encryptionKey []byte) (*OrgsService, error) {
	s := &OrgsService{pool: pool}
	if len(encryptionKey) == 32 {
		seal, err := newAESGCMSeal(encryptionKey)
		if err != nil {
			return nil, fmt.Errorf("orgs: init AES seal: %w", err)
		}
		s.seal = seal
	} else if len(encryptionKey) > 0 {
		return nil, fmt.Errorf("orgs: encryption key must be 32 bytes (AES-256), got %d", len(encryptionKey))
	}
	return s, nil
}

// SSOConfigWritable reports whether the service was constructed
// with an encryption key. Callers that expose SSO-config write
// endpoints should 503 when this is false so misconfigured envs
// don't silently accept plaintext-storage requests.
func (s *OrgsService) SSOConfigWritable() bool {
	return s.seal != nil
}

// ── CRUD ────────────────────────────────────────────────────────

// CreateOrg inserts an org + the creator as its admin. All-or-none
// via a transaction; a partial state (org exists but has no
// members) is unreachable.
func (s *OrgsService) CreateOrg(ctx context.Context, creatorID, name string) (*Org, error) {
	name = strings.TrimSpace(name)
	if err := validateOrgName(name); err != nil {
		return nil, err
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var org Org
	err = tx.QueryRow(ctx, `
		INSERT INTO public.organizations (name, created_by)
		VALUES ($1, $2::uuid)
		RETURNING id, name, primary_email_domain, (oidc_config IS NOT NULL) AS oidc_configured,
		          created_by::text, created_at, updated_at
	`, name, creatorID).Scan(
		&org.ID, &org.Name, &org.PrimaryEmailDomain, &org.OIDCConfigured,
		&org.CreatedByID, &org.CreatedAt, &org.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert org: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO public.org_members (org_id, platform_user_id, role, invited_via)
		VALUES ($1::uuid, $2::uuid, 'admin', 'manual')
	`, org.ID, creatorID)
	if err != nil {
		return nil, fmt.Errorf("insert creator as admin: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return &org, nil
}

// ListOrgsForUser returns orgs the user is a member of, with their
// role. Newest-first (via created_at DESC on the org). Capped at
// 100 — a single user in more than 100 orgs is a strong signal we
// need the multi-org UI polish that's deferred.
func (s *OrgsService) ListOrgsForUser(ctx context.Context, userID string) ([]OrgWithMembership, error) {
	const q = `
		SELECT o.id::text, o.name, o.primary_email_domain,
		       (o.oidc_config IS NOT NULL) AS oidc_configured,
		       o.created_by::text, o.created_at, o.updated_at,
		       om.role, om.created_at AS member_since
		  FROM public.org_members om
		  JOIN public.organizations o ON o.id = om.org_id
		 WHERE om.platform_user_id = $1::uuid
		 ORDER BY o.created_at DESC
		 LIMIT 100
	`
	rows, err := s.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("list orgs: %w", err)
	}
	defer rows.Close()

	out := make([]OrgWithMembership, 0, 4)
	for rows.Next() {
		var e OrgWithMembership
		if err := rows.Scan(
			&e.ID, &e.Name, &e.PrimaryEmailDomain, &e.OIDCConfigured,
			&e.CreatedByID, &e.CreatedAt, &e.UpdatedAt,
			&e.Role, &e.MemberSince,
		); err != nil {
			return nil, fmt.Errorf("scan org row: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate orgs: %w", err)
	}
	return out, nil
}

// GetOrgForMember returns the org detail IF the caller is a member.
// Returns ErrOrgMemberMissing when the caller is not a member (used
// for both non-existent org and non-member cases — deliberate: we
// don't leak the existence of orgs the caller isn't in).
func (s *OrgsService) GetOrgForMember(ctx context.Context, userID, orgID string) (*Org, string, error) {
	var org Org
	var role string
	err := s.pool.QueryRow(ctx, `
		SELECT o.id::text, o.name, o.primary_email_domain,
		       (o.oidc_config IS NOT NULL) AS oidc_configured,
		       o.created_by::text, o.created_at, o.updated_at,
		       om.role
		  FROM public.organizations o
		  JOIN public.org_members om
		    ON om.org_id = o.id
		   AND om.platform_user_id = $1::uuid
		 WHERE o.id = $2::uuid
	`, userID, orgID).Scan(
		&org.ID, &org.Name, &org.PrimaryEmailDomain, &org.OIDCConfigured,
		&org.CreatedByID, &org.CreatedAt, &org.UpdatedAt,
		&role,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, "", ErrOrgMemberMissing
		}
		return nil, "", fmt.Errorf("get org: %w", err)
	}
	return &org, role, nil
}

// ListMembers returns the roster for an org. Caller must be a
// member (any role) — enforced by the handler via GetOrgForMember
// before this is called.
func (s *OrgsService) ListMembers(ctx context.Context, orgID string) ([]OrgMember, error) {
	const q = `
		SELECT om.id::text, om.platform_user_id::text, u.email,
		       om.role, om.invited_via, om.created_at
		  FROM public.org_members om
		  JOIN public.platform_users u ON u.id = om.platform_user_id
		 WHERE om.org_id = $1::uuid
		 ORDER BY om.created_at ASC
		 LIMIT 500
	`
	rows, err := s.pool.Query(ctx, q, orgID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()

	out := make([]OrgMember, 0, 8)
	for rows.Next() {
		var m OrgMember
		if err := rows.Scan(&m.ID, &m.PlatformUserID, &m.Email, &m.Role, &m.InvitedVia, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate members: %w", err)
	}
	return out, nil
}

// ── SSO configuration ──────────────────────────────────────────

// SetSSOConfig persists the OIDC configuration for an org.
// Encrypts the client_secret via AES-256-GCM before writing to
// JSONB. Admin-only (caller checks role before invoking).
func (s *OrgsService) SetSSOConfig(ctx context.Context, orgID string, cfg OIDCConfig) error {
	if s.seal == nil {
		return errors.New("orgs: cannot store SSO config — PLATFORM_ENCRYPTION_KEY not configured")
	}
	if err := validateOIDCConfig(&cfg); err != nil {
		return err
	}
	sealed, err := s.seal.Seal([]byte(cfg.ClientSecret))
	if err != nil {
		return fmt.Errorf("seal client_secret: %w", err)
	}
	stored := storedOIDCConfig{
		Provider:           cfg.Provider,
		Issuer:             cfg.Issuer,
		ClientID:           cfg.ClientID,
		ClientSecretSealed: sealed,
		RedirectURL:        cfg.RedirectURL,
	}
	body, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("marshal oidc_config: %w", err)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE public.organizations
		   SET oidc_config = $1::jsonb,
		       updated_at = now()
		 WHERE id = $2::uuid
	`, body, orgID)
	if err != nil {
		return fmt.Errorf("update oidc_config: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrOrgNotFound
	}
	return nil
}

// GetOIDCConfigForOrg returns the resolved OIDC config with the
// plaintext client_secret. Used internally by the SSO callback
// handler ONLY — never expose the return value in a JSON response.
func (s *OrgsService) GetOIDCConfigForOrg(ctx context.Context, orgID string) (*OIDCConfig, error) {
	if s.seal == nil {
		return nil, errors.New("orgs: cannot read SSO config — PLATFORM_ENCRYPTION_KEY not configured")
	}
	var body []byte
	err := s.pool.QueryRow(ctx, `
		SELECT oidc_config
		  FROM public.organizations
		 WHERE id = $1::uuid
	`, orgID).Scan(&body)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrOrgNotFound
		}
		return nil, fmt.Errorf("query oidc_config: %w", err)
	}
	if len(body) == 0 {
		return nil, ErrOIDCNotConfigured
	}
	var stored storedOIDCConfig
	if err := json.Unmarshal(body, &stored); err != nil {
		return nil, fmt.Errorf("unmarshal oidc_config: %w", err)
	}
	if stored.ClientSecretSealed == "" {
		return nil, ErrOIDCNotConfigured
	}
	secret, err := s.seal.Open(stored.ClientSecretSealed)
	if err != nil {
		return nil, fmt.Errorf("open client_secret: %w", err)
	}
	return &OIDCConfig{
		Provider:     stored.Provider,
		Issuer:       stored.Issuer,
		ClientID:     stored.ClientID,
		ClientSecret: string(secret),
		RedirectURL:  stored.RedirectURL,
	}, nil
}

// GetOIDCConfigPublic returns the OIDC config WITHOUT the client
// secret — safe for JSON responses.
func (s *OrgsService) GetOIDCConfigPublic(ctx context.Context, orgID string) (*OIDCConfigPublic, error) {
	var body []byte
	err := s.pool.QueryRow(ctx, `
		SELECT oidc_config
		  FROM public.organizations
		 WHERE id = $1::uuid
	`, orgID).Scan(&body)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrOrgNotFound
		}
		return nil, fmt.Errorf("query oidc_config: %w", err)
	}
	if len(body) == 0 {
		return nil, ErrOIDCNotConfigured
	}
	var stored storedOIDCConfig
	if err := json.Unmarshal(body, &stored); err != nil {
		return nil, fmt.Errorf("unmarshal oidc_config: %w", err)
	}
	return &OIDCConfigPublic{
		Provider:        stored.Provider,
		Issuer:          stored.Issuer,
		ClientID:        stored.ClientID,
		RedirectURL:     stored.RedirectURL,
		ClientSecretSet: stored.ClientSecretSealed != "",
	}, nil
}

// ── Membership management ─────────────────────────────────────

// InviteMember adds a platform_user to the org by email. The user
// must already exist in platform_users — MVP doesn't send an email
// invitation to non-users. Follow-up PR adds email invitation flow.
func (s *OrgsService) InviteMember(ctx context.Context, orgID, email, role string) (*OrgMember, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, errors.New("email is required")
	}
	if role != RoleOrgAdmin && role != RoleOrgMember {
		return nil, fmt.Errorf("role must be admin or member")
	}

	// Find the target user by email.
	var userID string
	err := s.pool.QueryRow(ctx, `
		SELECT id::text FROM public.platform_users
		 WHERE lower(email) = $1
		 LIMIT 1
	`, email).Scan(&userID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("lookup user: %w", err)
	}

	var m OrgMember
	err = s.pool.QueryRow(ctx, `
		INSERT INTO public.org_members (org_id, platform_user_id, role, invited_via)
		VALUES ($1::uuid, $2::uuid, $3, 'manual')
		RETURNING id::text, platform_user_id::text, $4::text AS email, role, invited_via, created_at
	`, orgID, userID, role, email).Scan(
		&m.ID, &m.PlatformUserID, &m.Email, &m.Role, &m.InvitedVia, &m.CreatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrMemberExists
		}
		return nil, fmt.Errorf("insert member: %w", err)
	}
	return &m, nil
}

// RemoveMember drops an org_members row. Admin-only. Refuses to
// remove the last admin (prevents the org from becoming
// unmanageable — the caller must promote another member first).
func (s *OrgsService) RemoveMember(ctx context.Context, orgID, targetUserID string) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Guard: don't allow removing the last remaining admin.
	var adminCount int
	err = tx.QueryRow(ctx, `
		SELECT count(*) FROM public.org_members
		 WHERE org_id = $1::uuid AND role = 'admin'
	`, orgID).Scan(&adminCount)
	if err != nil {
		return fmt.Errorf("count admins: %w", err)
	}
	var targetIsAdmin bool
	err = tx.QueryRow(ctx, `
		SELECT (role = 'admin') FROM public.org_members
		 WHERE org_id = $1::uuid AND platform_user_id = $2::uuid
	`, orgID, targetUserID).Scan(&targetIsAdmin)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil // idempotent: already not a member
		}
		return fmt.Errorf("lookup target: %w", err)
	}
	if targetIsAdmin && adminCount == 1 {
		return errors.New("cannot remove the last admin from an organization")
	}

	tag, err := tx.Exec(ctx, `
		DELETE FROM public.org_members
		 WHERE org_id = $1::uuid AND platform_user_id = $2::uuid
	`, orgID, targetUserID)
	if err != nil {
		return fmt.Errorf("delete member: %w", err)
	}
	_ = tag // idempotent-fine when zero rows

	return tx.Commit(ctx)
}

// FindOrgForSSOMember returns the (org_id, oidc_config) for a user
// during the SSO INIT step. Used to look up which org owns the SSO
// flow for a given email — for MVP, we require the user to already
// have an org_members entry (created via manual invite). Domain-
// based auto-provisioning is a follow-up gated on DNS-TXT proof.
//
// If the user is in multiple orgs with SSO configured (rare — MVP
// UI single-org), returns the first one (arbitrary order — the
// multi-org UI polish is a deferred follow-up).
func (s *OrgsService) FindOrgForSSOMember(ctx context.Context, email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var orgID string
	err := s.pool.QueryRow(ctx, `
		SELECT o.id::text
		  FROM public.organizations o
		  JOIN public.org_members om ON om.org_id = o.id
		  JOIN public.platform_users u ON u.id = om.platform_user_id
		 WHERE lower(u.email) = $1
		   AND o.oidc_config IS NOT NULL
		 ORDER BY om.created_at ASC
		 LIMIT 1
	`, email).Scan(&orgID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", ErrOrgMemberMissing
		}
		return "", fmt.Errorf("find org: %w", err)
	}
	return orgID, nil
}

// EnsureMemberFromSSO is called by the SSO callback after we've
// verified the ID token. Confirms the user has an existing
// org_members row for the given org. Never inserts — the callback
// upstream already rejects unknown users at platform_users lookup
// time (see errSSOUserNotProvisioned), and InviteMember is the only
// caller-facing route that creates org_members rows.
//
// Auto-provisioning based on primary_email_domain (DNS-TXT verified)
// is Phase 2 — this function stays lookup-only until then so we don't
// silently regress the manual-invite gate.
func (s *OrgsService) EnsureMemberFromSSO(ctx context.Context, orgID, platformUserID string) error {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM public.org_members
			 WHERE org_id = $1::uuid AND platform_user_id = $2::uuid
		)
	`, orgID, platformUserID).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check membership: %w", err)
	}
	if !exists {
		return ErrOrgMemberMissing
	}
	return nil
}

// ── Validators ─────────────────────────────────────────────────

func validateOrgName(name string) error {
	if name == "" {
		return errors.New("name is required")
	}
	n := utf8.RuneCountInString(name)
	if n < 1 || n > orgMaxNameLen {
		return fmt.Errorf("name must be 1-%d characters", orgMaxNameLen)
	}
	return nil
}

func validateOIDCConfig(cfg *OIDCConfig) error {
	if cfg.Issuer == "" {
		return errors.New("issuer is required")
	}
	if !strings.HasPrefix(cfg.Issuer, "https://") {
		return errors.New("issuer must be https://")
	}
	if cfg.ClientID == "" {
		return errors.New("client_id is required")
	}
	if cfg.ClientSecret == "" {
		return errors.New("client_secret is required")
	}
	if cfg.RedirectURL == "" {
		return errors.New("redirect_url is required")
	}
	if !strings.HasPrefix(cfg.RedirectURL, "https://") {
		return errors.New("redirect_url must be https://")
	}
	if cfg.Provider == "" {
		cfg.Provider = "generic"
	}
	return nil
}

// ── AES-256-GCM sealer for the client_secret ──────────────────

type aesGCMSeal struct {
	aead cipher.AEAD
}

func newAESGCMSeal(key []byte) (*aesGCMSeal, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("key must be 32 bytes for AES-256, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &aesGCMSeal{aead: aead}, nil
}

// Seal returns base64(nonce || ciphertext). Nonces are 12 bytes
// per GCM standard; a random nonce per Seal call is safe up to
// 2^32 encryptions of the same key.
func (s *aesGCMSeal) Seal(plaintext []byte) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := s.aead.Seal(nil, nonce, plaintext, nil)
	buf := make([]byte, 0, len(nonce)+len(ct))
	buf = append(buf, nonce...)
	buf = append(buf, ct...)
	return base64.StdEncoding.EncodeToString(buf), nil
}

// Open reverses Seal. Rejects malformed input (short buffer,
// invalid base64, AEAD verification failure).
func (s *aesGCMSeal) Open(sealed string) ([]byte, error) {
	buf, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	nsz := s.aead.NonceSize()
	if len(buf) < nsz {
		return nil, errors.New("sealed value too short")
	}
	nonce, ct := buf[:nsz], buf[nsz:]
	pt, err := s.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("aead open: %w", err)
	}
	return pt, nil
}

// isUniqueViolation returns true when err is a Postgres 23505
// unique-constraint violation.
func isUniqueViolation(err error) bool {
	// pgx errors surface the SQLSTATE code on the pg-conn error type.
	// Not importing pgconn here — the string check is stable enough
	// and matches the pattern used elsewhere in this codebase.
	return err != nil && strings.Contains(err.Error(), "23505")
}
