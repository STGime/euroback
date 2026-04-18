package tenant

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ── Role hierarchy ──

// roleLevel maps role names to numeric levels for comparison.
var roleLevel = map[string]int{
	"viewer":    1,
	"developer": 2,
	"admin":     3,
	"owner":     4,
}

// HasRole returns true if actualRole >= minimumRole in the hierarchy.
func HasRole(actualRole, minimumRole string) bool {
	return roleLevel[actualRole] >= roleLevel[minimumRole]
}

// ── Data types ──

// Member represents a project_members row.
type Member struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// Invitation represents a project_invitations row.
type Invitation struct {
	ID         string     `json:"id"`
	ProjectID  string     `json:"project_id"`
	Email      string     `json:"email"`
	Role       string     `json:"role"`
	InvitedBy  string     `json:"invited_by"`
	SentAt     time.Time  `json:"sent_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	AcceptedAt *time.Time `json:"accepted_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

// MembersResponse is the combined response for the members list endpoint.
type MembersResponse struct {
	Members     []Member     `json:"members"`
	Invitations []Invitation `json:"invitations"`
}

// ── Role resolution ──

// ResolveRole returns the caller's role on a project, or "" if no access.
func ResolveRole(ctx context.Context, pool *pgxpool.Pool, projectID, userID string) (string, error) {
	var role string
	err := pool.QueryRow(ctx,
		`SELECT role FROM public.project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, userID,
	).Scan(&role)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve role: %w", err)
	}
	return role, nil
}

// RequireRole is a helper that resolves the caller's role from the request
// context and returns 403 if insufficient. Returns the role on success.
func RequireRole(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, projectID, minRole string) (claims *auth.Claims, role string, ok bool) {
	c, hasAuth := auth.ClaimsFromContext(r.Context())
	if !hasAuth || c == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return nil, "", false
	}

	resolved, err := ResolveRole(r.Context(), pool, projectID, c.Subject)
	if err != nil {
		slog.Error("resolve role failed", "error", err)
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return nil, "", false
	}
	if resolved == "" || !HasRole(resolved, minRole) {
		http.Error(w, `{"error":"forbidden: requires `+minRole+` role"}`, http.StatusForbidden)
		return nil, "", false
	}
	return c, resolved, true
}

// ── Handlers ──

// HandleListMembers returns members and pending invitations for a project.
// GET /platform/projects/{id}/members
func HandleListMembers(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		claims, _, ok := RequireRole(w, r, pool, projectID, "viewer")
		if !ok {
			return
		}
		_ = claims

		// Members
		memberRows, err := pool.Query(r.Context(),
			`SELECT pm.id, pm.project_id, pm.user_id, u.email, pm.role, pm.created_at
			 FROM public.project_members pm
			 JOIN public.platform_users u ON u.id = pm.user_id
			 WHERE pm.project_id = $1
			 ORDER BY pm.created_at ASC`, projectID)
		if err != nil {
			http.Error(w, `{"error":"failed to list members"}`, http.StatusInternalServerError)
			return
		}
		defer memberRows.Close()

		members := make([]Member, 0)
		for memberRows.Next() {
			var m Member
			if err := memberRows.Scan(&m.ID, &m.ProjectID, &m.UserID, &m.Email, &m.Role, &m.CreatedAt); err != nil {
				http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
				return
			}
			members = append(members, m)
		}

		// Pending invitations (not yet accepted, not expired)
		invRows, err := pool.Query(r.Context(),
			`SELECT id, project_id, email, role, invited_by, sent_at, expires_at, accepted_at, created_at
			 FROM public.project_invitations
			 WHERE project_id = $1 AND accepted_at IS NULL AND expires_at > now()
			 ORDER BY created_at DESC`, projectID)
		if err != nil {
			http.Error(w, `{"error":"failed to list invitations"}`, http.StatusInternalServerError)
			return
		}
		defer invRows.Close()

		invitations := make([]Invitation, 0)
		for invRows.Next() {
			var inv Invitation
			if err := invRows.Scan(&inv.ID, &inv.ProjectID, &inv.Email, &inv.Role, &inv.InvitedBy, &inv.SentAt, &inv.ExpiresAt, &inv.AcceptedAt, &inv.CreatedAt); err != nil {
				http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
				return
			}
			invitations = append(invitations, inv)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(MembersResponse{Members: members, Invitations: invitations})
	}
}

// HandleInviteMember sends an invitation email.
// POST /platform/projects/{id}/members/invite
func HandleInviteMember(pool *pgxpool.Pool, sendEmail func(ctx context.Context, to, subject, html string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		claims, _, ok := RequireRole(w, r, pool, projectID, "admin")
		if !ok {
			return
		}

		var req struct {
			Email string `json:"email"`
			Role  string `json:"role"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		req.Email = strings.ToLower(strings.TrimSpace(req.Email))
		req.Role = strings.ToLower(strings.TrimSpace(req.Role))

		if req.Email == "" || !strings.Contains(req.Email, "@") {
			http.Error(w, `{"error":"valid email is required"}`, http.StatusBadRequest)
			return
		}
		if req.Role != "admin" && req.Role != "developer" && req.Role != "viewer" {
			http.Error(w, `{"error":"role must be admin, developer, or viewer"}`, http.StatusBadRequest)
			return
		}

		// Check if already a member.
		var existingCount int
		pool.QueryRow(r.Context(),
			`SELECT count(*) FROM public.project_members pm
			 JOIN public.platform_users u ON u.id = pm.user_id
			 WHERE pm.project_id = $1 AND u.email = $2`,
			projectID, req.Email).Scan(&existingCount)
		if existingCount > 0 {
			http.Error(w, `{"error":"user is already a member of this project"}`, http.StatusConflict)
			return
		}

		// Generate invitation token.
		rawToken, tokenHash, err := generateInviteToken()
		if err != nil {
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		expiresAt := time.Now().Add(7 * 24 * time.Hour) // 7 days

		// Upsert invitation (re-invite updates the token and sent_at).
		_, err = pool.Exec(r.Context(),
			`INSERT INTO public.project_invitations (project_id, email, role, token_hash, invited_by, expires_at, sent_at)
			 VALUES ($1, $2, $3, $4, $5, $6, now())
			 ON CONFLICT (project_id, email) DO UPDATE SET
			   role = EXCLUDED.role,
			   token_hash = EXCLUDED.token_hash,
			   invited_by = EXCLUDED.invited_by,
			   expires_at = EXCLUDED.expires_at,
			   sent_at = now(),
			   accepted_at = NULL`,
			projectID, req.Email, req.Role, tokenHash, claims.Subject, expiresAt)
		if err != nil {
			slog.Error("insert invitation failed", "error", err)
			http.Error(w, `{"error":"failed to create invitation"}`, http.StatusInternalServerError)
			return
		}

		// Look up project name for the email.
		var projectName string
		pool.QueryRow(r.Context(), `SELECT name FROM projects WHERE id = $1`, projectID).Scan(&projectName)

		// Send invitation email.
		if sendEmail != nil {
			subject := fmt.Sprintf("You've been invited to project %q on Eurobase", projectName)
			body := renderInviteEmail(projectName, req.Email, req.Role, claims.Email, rawToken)
			if err := sendEmail(r.Context(), req.Email, subject, body); err != nil {
				slog.Error("send invitation email failed", "error", err, "to", req.Email)
				// Don't fail — the invitation record exists, they can resend.
			}
		}

		// Audit.
		if auditSvc := audit.FromContext(r.Context()); auditSvc != nil {
			auditSvc.Log(r.Context(), projectID, claims.Subject, claims.Email,
				audit.ActionMemberInvited,
				audit.WithTarget("invitation", req.Email),
				audit.WithMetadata(map[string]interface{}{"email": req.Email, "role": req.Role}),
				audit.WithIP(r.RemoteAddr))
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"status": "invited", "email": req.Email, "role": req.Role})
	}
}

// HandleResendInvitation resends the invitation email with a fresh token.
// POST /platform/projects/{id}/members/resend
func HandleResendInvitation(pool *pgxpool.Pool, sendEmail func(ctx context.Context, to, subject, html string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		claims, _, ok := RequireRole(w, r, pool, projectID, "admin")
		if !ok {
			return
		}

		var req struct {
			Email string `json:"email"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		req.Email = strings.ToLower(strings.TrimSpace(req.Email))

		// Generate a fresh token.
		rawToken, tokenHash, err := generateInviteToken()
		if err != nil {
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		expiresAt := time.Now().Add(7 * 24 * time.Hour)

		// Update existing invitation with new token and sent_at.
		var role string
		err = pool.QueryRow(r.Context(),
			`UPDATE public.project_invitations
			 SET token_hash = $3, expires_at = $4, sent_at = now()
			 WHERE project_id = $1 AND email = $2 AND accepted_at IS NULL
			 RETURNING role`,
			projectID, req.Email, tokenHash, expiresAt).Scan(&role)
		if err != nil {
			http.Error(w, `{"error":"no pending invitation found for this email"}`, http.StatusNotFound)
			return
		}

		// Send email.
		var projectName string
		pool.QueryRow(r.Context(), `SELECT name FROM projects WHERE id = $1`, projectID).Scan(&projectName)

		if sendEmail != nil {
			subject := fmt.Sprintf("Reminder: You've been invited to project %q on Eurobase", projectName)
			body := renderInviteEmail(projectName, req.Email, role, claims.Email, rawToken)
			if err := sendEmail(r.Context(), req.Email, subject, body); err != nil {
				slog.Error("resend invitation email failed", "error", err, "to", req.Email)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "resent", "email": req.Email})
	}
}

// HandleRemoveMember removes a member from the project.
// DELETE /platform/projects/{id}/members/{userId}
func HandleRemoveMember(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		targetUserID := chi.URLParam(r, "userId")

		claims, _, ok := RequireRole(w, r, pool, projectID, "admin")
		if !ok {
			return
		}

		// Can't remove the owner.
		var targetRole string
		err := pool.QueryRow(r.Context(),
			`SELECT role FROM public.project_members WHERE project_id = $1 AND user_id = $2`,
			projectID, targetUserID).Scan(&targetRole)
		if err != nil {
			http.Error(w, `{"error":"member not found"}`, http.StatusNotFound)
			return
		}
		if targetRole == "owner" {
			http.Error(w, `{"error":"cannot remove the project owner"}`, http.StatusForbidden)
			return
		}

		_, err = pool.Exec(r.Context(),
			`DELETE FROM public.project_members WHERE project_id = $1 AND user_id = $2`,
			projectID, targetUserID)
		if err != nil {
			http.Error(w, `{"error":"failed to remove member"}`, http.StatusInternalServerError)
			return
		}

		// Look up removed user's email for audit.
		var removedEmail string
		pool.QueryRow(r.Context(), `SELECT email FROM platform_users WHERE id = $1`, targetUserID).Scan(&removedEmail)

		if auditSvc := audit.FromContext(r.Context()); auditSvc != nil {
			auditSvc.Log(r.Context(), projectID, claims.Subject, claims.Email,
				audit.ActionMemberRemoved,
				audit.WithTarget("member", targetUserID),
				audit.WithMetadata(map[string]interface{}{"removed_email": removedEmail, "removed_role": targetRole}),
				audit.WithIP(r.RemoteAddr))
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "removed"})
	}
}

// HandleChangeRole changes a member's role.
// PATCH /platform/projects/{id}/members/{userId}
func HandleChangeRole(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		targetUserID := chi.URLParam(r, "userId")

		claims, _, ok := RequireRole(w, r, pool, projectID, "owner")
		if !ok {
			return
		}

		var req struct {
			Role string `json:"role"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		req.Role = strings.ToLower(strings.TrimSpace(req.Role))

		if req.Role != "admin" && req.Role != "developer" && req.Role != "viewer" {
			http.Error(w, `{"error":"role must be admin, developer, or viewer"}`, http.StatusBadRequest)
			return
		}

		// Can't change the owner's role.
		if targetUserID == claims.Subject {
			http.Error(w, `{"error":"cannot change your own role as owner"}`, http.StatusForbidden)
			return
		}

		var oldRole string
		err := pool.QueryRow(r.Context(),
			`SELECT role FROM public.project_members WHERE project_id = $1 AND user_id = $2`,
			projectID, targetUserID).Scan(&oldRole)
		if err != nil {
			http.Error(w, `{"error":"member not found"}`, http.StatusNotFound)
			return
		}
		if oldRole == "owner" {
			http.Error(w, `{"error":"cannot change the owner's role"}`, http.StatusForbidden)
			return
		}

		_, err = pool.Exec(r.Context(),
			`UPDATE public.project_members SET role = $3 WHERE project_id = $1 AND user_id = $2`,
			projectID, targetUserID, req.Role)
		if err != nil {
			http.Error(w, `{"error":"failed to change role"}`, http.StatusInternalServerError)
			return
		}

		var targetEmail string
		pool.QueryRow(r.Context(), `SELECT email FROM platform_users WHERE id = $1`, targetUserID).Scan(&targetEmail)

		if auditSvc := audit.FromContext(r.Context()); auditSvc != nil {
			auditSvc.Log(r.Context(), projectID, claims.Subject, claims.Email,
				audit.ActionMemberRoleChanged,
				audit.WithTarget("member", targetUserID),
				audit.WithMetadata(map[string]interface{}{"email": targetEmail, "from": oldRole, "to": req.Role}),
				audit.WithIP(r.RemoteAddr))
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "changed", "role": req.Role})
	}
}

// HandleAcceptInvitation accepts an invitation token. This is a public
// endpoint (no auth required for the token validation itself), but the
// user must be signed in so we know which platform_users row to link.
// POST /platform/invitations/accept
func HandleAcceptInvitation(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok || claims == nil {
			http.Error(w, `{"error":"sign in first, then click the invitation link again"}`, http.StatusUnauthorized)
			return
		}

		var req struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}

		tokenHash := hashToken(req.Token)

		// Look up the invitation.
		var inv Invitation
		err := pool.QueryRow(r.Context(),
			`SELECT id, project_id, email, role, invited_by, sent_at, expires_at, accepted_at, created_at
			 FROM public.project_invitations
			 WHERE token_hash = $1`, tokenHash,
		).Scan(&inv.ID, &inv.ProjectID, &inv.Email, &inv.Role, &inv.InvitedBy,
			&inv.SentAt, &inv.ExpiresAt, &inv.AcceptedAt, &inv.CreatedAt)
		if err != nil {
			http.Error(w, `{"error":"invalid or expired invitation"}`, http.StatusBadRequest)
			return
		}
		if inv.AcceptedAt != nil {
			http.Error(w, `{"error":"invitation already accepted"}`, http.StatusConflict)
			return
		}
		if time.Now().After(inv.ExpiresAt) {
			http.Error(w, `{"error":"invitation has expired — ask the project admin to resend"}`, http.StatusGone)
			return
		}

		// Verify the accepting user's email matches the invitation (case-insensitive).
		if strings.ToLower(claims.Email) != strings.ToLower(inv.Email) {
			http.Error(w, `{"error":"this invitation was sent to a different email address"}`, http.StatusForbidden)
			return
		}

		// Create membership.
		_, err = pool.Exec(r.Context(),
			`INSERT INTO public.project_members (project_id, user_id, role, invited_by)
			 VALUES ($1, $2, $3, $4)
			 ON CONFLICT (project_id, user_id) DO UPDATE SET role = EXCLUDED.role`,
			inv.ProjectID, claims.Subject, inv.Role, inv.InvitedBy)
		if err != nil {
			slog.Error("insert member failed", "error", err)
			http.Error(w, `{"error":"failed to accept invitation"}`, http.StatusInternalServerError)
			return
		}

		// Mark invitation as accepted.
		pool.Exec(r.Context(),
			`UPDATE public.project_invitations SET accepted_at = now() WHERE id = $1`, inv.ID)

		// Audit.
		if auditSvc := audit.FromContext(r.Context()); auditSvc != nil {
			auditSvc.Log(r.Context(), inv.ProjectID, claims.Subject, claims.Email,
				"member.accepted",
				audit.WithTarget("invitation", inv.ID),
				audit.WithMetadata(map[string]interface{}{"role": inv.Role}),
				audit.WithIP(r.RemoteAddr))
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":     "accepted",
			"project_id": inv.ProjectID,
			"role":       inv.Role,
		})
	}
}

// ── Helpers ──

func generateInviteToken() (raw string, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	raw = hex.EncodeToString(b)
	h := sha256.Sum256([]byte(raw))
	hash = hex.EncodeToString(h[:])
	return raw, hash, nil
}

func hashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

func renderInviteEmail(projectName, recipientEmail, role, inviterEmail, token string) string {
	// In production, CONSOLE_URL would be used. For now, hardcode the pattern.
	acceptURL := fmt.Sprintf("https://console.eurobase.app/invite?token=%s", token)

	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; max-width: 600px; margin: 0 auto; padding: 24px; color: #111;">
  <h2 style="color: #1e40af; margin-top: 0;">You're invited to join a project on Eurobase</h2>
  <p><strong>%s</strong> has invited you to project <strong>%s</strong> as a <strong>%s</strong>.</p>
  <p>Click the button below to accept the invitation:</p>
  <p style="margin: 24px 0;">
    <a href="%s" style="display: inline-block; background: #2563eb; color: #fff; padding: 12px 24px; border-radius: 8px; text-decoration: none; font-weight: 600;">
      Accept Invitation
    </a>
  </p>
  <p style="color: #6b7280; font-size: 13px;">This invitation expires in 7 days. If you don't have a Eurobase account, sign up first and then click the link above.</p>
  <p style="color: #9ca3af; font-size: 12px; margin-top: 32px;">If you didn't expect this invitation, you can safely ignore this email.</p>
</body>
</html>`, inviterEmail, projectName, role, acceptURL)
}
