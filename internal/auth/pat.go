package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PATPrefix is the literal prefix on every Personal Access Token. Used to
// detect PATs in the Authorization header so we route them to DB lookup
// instead of the JWT validator.
const PATPrefix = "eb_pat_"

// PAT represents a personal access token row. The plaintext token is never
// stored or returned after creation — only the prefix (for display) and
// the SHA-256 hash (for lookup).
type PAT struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
	// ProjectID / Role: a project-scoped token (#702). Both nil for a
	// legacy account-wide token.
	ProjectID *string `json:"project_id"`
	Role      *string `json:"role"`
}

// PAT roles a project-scoped token can have. "owner" is never offered:
// owner-only actions (delete / transfer the project, billing) need a
// console session.
var patRoles = map[string]bool{"viewer": true, "developer": true, "admin": true}

// ProjectRoleFunc returns the caller's current role in a project ("" = no
// access). Wired to tenant.CallerProjectRole by the router (the auth
// package can't import tenant).
type ProjectRoleFunc func(ctx context.Context, claims *Claims, projectID string) (string, error)

// ErrPATRoleTooHigh: the requested token role exceeds the creator's role in
// the project.
var ErrPATRoleTooHigh = errors.New("a token's access can't exceed your own role in the project")

// ErrPATProjectNotFound: no access to the project the token is for.
var ErrPATProjectNotFound = errors.New("project not found")

var patRoleLevel = map[string]int{"viewer": 1, "developer": 2, "admin": 3, "owner": 4}

// PATService manages personal access tokens.
type PATService struct {
	pool        *pgxpool.Pool
	projectRole ProjectRoleFunc
}

// WithProjectRole wires the project-role lookup Create uses to check that
// a token's role doesn't exceed its creator's.
func (s *PATService) WithProjectRole(fn ProjectRoleFunc) *PATService {
	s.projectRole = fn
	return s
}

// NewPATService creates a new PAT service.
func NewPATService(pool *pgxpool.Pool) *PATService {
	return &PATService{pool: pool}
}

// ErrPATNotFound is returned when a token lookup fails (unknown hash, revoked,
// or expired). Callers should treat these as 401, not 500.
var ErrPATNotFound = errors.New("personal access token not found or expired")

// generatePAT produces a fresh token in the form `eb_pat_<32 hex chars>`.
// Returns the plaintext token (shown to the user once), the display prefix
// (first 14 chars — `eb_pat_` + 7 hex), and the hex-encoded SHA-256 hash.
func generatePAT() (token, prefix, hash string, err error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("generate pat random: %w", err)
	}
	body := hex.EncodeToString(buf)
	token = PATPrefix + body
	prefix = token[:14]
	sum := sha256.Sum256([]byte(token))
	hash = hex.EncodeToString(sum[:])
	return token, prefix, hash, nil
}

// CreateInput is the input to Create. New tokens are project-scoped:
// ProjectID and Role are required, and there is no expiry (users revoke
// tokens in the console). Claims are the creator's (console) session.
type CreateInput struct {
	Claims    *Claims
	Name      string
	ProjectID string
	Role      string
}

// CreateResult is what Create returns. PlaintextToken is shown once and
// never retrievable again.
type CreateResult struct {
	PAT            PAT
	PlaintextToken string
}

// Create issues a new PAT for the given user. Name must be non-empty.
func (s *PATService) Create(ctx context.Context, in CreateInput) (*CreateResult, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if len(name) > 100 {
		return nil, fmt.Errorf("name must be 100 characters or fewer")
	}
	if in.Claims == nil {
		return nil, fmt.Errorf("unauthorized")
	}
	if in.ProjectID == "" {
		return nil, fmt.Errorf("project_id is required: a token is for one project")
	}
	if in.Role == "" {
		in.Role = "viewer"
	}
	if !patRoles[in.Role] {
		return nil, fmt.Errorf("role must be viewer, developer or admin")
	}
	if s.projectRole == nil {
		return nil, fmt.Errorf("token creation unavailable: project role lookup not configured")
	}
	own, err := s.projectRole(ctx, in.Claims, in.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("check project role: %w", err)
	}
	if own == "" {
		return nil, ErrPATProjectNotFound
	}
	if patRoleLevel[in.Role] > patRoleLevel[own] {
		return nil, ErrPATRoleTooHigh
	}

	token, prefix, hash, err := generatePAT()
	if err != nil {
		return nil, err
	}

	var pat PAT
	err = s.pool.QueryRow(ctx,
		`INSERT INTO public.personal_access_tokens (user_id, name, prefix, token_hash, project_id, role)
		 VALUES ($1, $2, $3, $4, $5::uuid, $6)
		 RETURNING id, user_id, name, prefix, expires_at, last_used_at, created_at, project_id::text, role`,
		in.Claims.Subject, name, prefix, hash, in.ProjectID, in.Role,
	).Scan(&pat.ID, &pat.UserID, &pat.Name, &pat.Prefix, &pat.ExpiresAt, &pat.LastUsedAt, &pat.CreatedAt, &pat.ProjectID, &pat.Role)
	if err != nil {
		return nil, fmt.Errorf("insert pat: %w", err)
	}

	return &CreateResult{PAT: pat, PlaintextToken: token}, nil
}

// List returns all PATs owned by the given user, newest first.
func (s *PATService) List(ctx context.Context, userID string) ([]PAT, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, user_id, name, prefix, expires_at, last_used_at, created_at, project_id::text, role
		 FROM public.personal_access_tokens
		 WHERE user_id = $1
		 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list pats: %w", err)
	}
	defer rows.Close()

	out := make([]PAT, 0)
	for rows.Next() {
		var p PAT
		if err := rows.Scan(&p.ID, &p.UserID, &p.Name, &p.Prefix, &p.ExpiresAt, &p.LastUsedAt, &p.CreatedAt, &p.ProjectID, &p.Role); err != nil {
			return nil, fmt.Errorf("scan pat: %w", err)
		}
		out = append(out, p)
	}
	return out, nil
}

// Revoke deletes a PAT. The userID argument scopes the delete so a token
// can't be revoked across accounts.
func (s *PATService) Revoke(ctx context.Context, userID, tokenID string) error {
	res, err := s.pool.Exec(ctx,
		`DELETE FROM public.personal_access_tokens
		 WHERE id = $1 AND user_id = $2`, tokenID, userID)
	if err != nil {
		return fmt.Errorf("revoke pat: %w", err)
	}
	if res.RowsAffected() == 0 {
		return ErrPATNotFound
	}
	return nil
}

// Validate looks up a plaintext token, checks it hasn't expired, bumps
// last_used_at, and returns the owning user's claims. PAT-derived claims
// always have IsSuperadmin = false: PATs are explicitly scoped down from
// the underlying account so a leaked token can't reach the admin surface.
func (s *PATService) Validate(ctx context.Context, plaintextToken string) (*Claims, error) {
	if !strings.HasPrefix(plaintextToken, PATPrefix) {
		return nil, ErrPATNotFound
	}
	sum := sha256.Sum256([]byte(plaintextToken))
	hash := hex.EncodeToString(sum[:])

	var (
		tokenID   string
		userID    string
		email     string
		expiresAt *time.Time
		projectID *string
		role      *string
	)
	err := s.pool.QueryRow(ctx,
		`SELECT t.id, t.user_id, u.email, t.expires_at, t.project_id::text, t.role
		 FROM public.personal_access_tokens t
		 JOIN public.platform_users u ON u.id = t.user_id
		 WHERE t.token_hash = $1`, hash,
	).Scan(&tokenID, &userID, &email, &expiresAt, &projectID, &role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPATNotFound
		}
		return nil, fmt.Errorf("lookup pat: %w", err)
	}

	if expiresAt != nil && expiresAt.Before(time.Now()) {
		return nil, ErrPATNotFound
	}

	// Bump last_used_at. Best-effort: a slow update shouldn't fail the auth.
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = s.pool.Exec(bgCtx,
			`UPDATE public.personal_access_tokens SET last_used_at = now() WHERE id = $1`, tokenID)
	}()

	c := &Claims{
		LoginVia:     LoginViaPAT,
		Subject:      userID,
		Email:        email,
		IsSuperadmin: false, // PATs never carry superadmin
		PATID:        tokenID,
	}
	if projectID != nil && role != nil {
		c.PATProjectID, c.PATRole = *projectID, *role
	}
	return c, nil
}
