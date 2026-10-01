package auth

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// VerifySuperadminPasskeyBypass is the database half of the superadmin
// passkey bypass of an org's sso_required (tenant.ClaimsSatisfySSOFor):
// the account must still be a superadmin — the JWT's flag can be up to a
// day old — and must have a passkey registered more than a day ago. A
// passkey enrolled minutes ago could come from someone who only has the
// password (a password session may enrol a passkey on an account that has
// none); a day gives the owner time to notice. pool must be the developer
// pool (platform_passkey_credentials is revoked from the gateway role).
// Any error → false.
func VerifySuperadminPasskeyBypass(ctx context.Context, pool *pgxpool.Pool, userID string) bool {
	if pool == nil || userID == "" {
		return false
	}
	var ok bool
	err := pool.QueryRow(ctx,
		`SELECT COALESCE(u.is_superadmin, false) AND EXISTS (
		   SELECT 1 FROM public.platform_passkey_credentials c
		   WHERE c.platform_user_id = u.id AND c.created_at < now() - interval '24 hours')
		 FROM public.platform_users u WHERE u.id = $1::uuid`, userID).Scan(&ok)
	if err != nil {
		slog.Warn("superadmin SSO bypass: account check failed, refusing", "user_id", userID, "error", err)
		return false
	}
	return ok
}
