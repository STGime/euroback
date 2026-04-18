package enduser

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// oauthStateTTL is how long a generated OAuth state is valid before callback.
const oauthStateTTL = 10 * time.Minute

// ErrOAuthStateNotFound is returned when the state is unknown, consumed, or expired.
var ErrOAuthStateNotFound = errors.New("oauth state not found or expired")

// OAuthStateRecord captures what was stored server-side when the user began
// the OAuth redirect, so the callback can validate without trusting the
// provider-roundtripped state beyond its opaque identifier.
type OAuthStateRecord struct {
	ProjectID   string
	Provider    string
	RedirectURL string
}

// storeOAuthState inserts a new state row. The caller passes an opaque random
// value; this function does not generate it so callers may choose length.
func storeOAuthState(ctx context.Context, pool *pgxpool.Pool, state, projectID, provider, redirectURL string) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO public.oauth_states (state, project_id, provider, redirect_url, expires_at)
		 VALUES ($1, $2, $3, $4, now() + $5::interval)`,
		state, projectID, provider, redirectURL, oauthStateTTL.String(),
	)
	return err
}

// consumeOAuthState atomically fetches and deletes a state row, enforcing
// single-use semantics. Expired rows are rejected and removed in the same
// call. Returns ErrOAuthStateNotFound if no matching unexpired row exists.
func consumeOAuthState(ctx context.Context, pool *pgxpool.Pool, state string) (*OAuthStateRecord, error) {
	var rec OAuthStateRecord
	err := pool.QueryRow(ctx,
		`DELETE FROM public.oauth_states
		 WHERE state = $1 AND expires_at > now()
		 RETURNING project_id::text, provider, redirect_url`,
		state,
	).Scan(&rec.ProjectID, &rec.Provider, &rec.RedirectURL)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Opportunistically clean expired rows — cheap and keeps the table small.
			_, _ = pool.Exec(ctx, `DELETE FROM public.oauth_states WHERE expires_at <= now()`)
			return nil, ErrOAuthStateNotFound
		}
		return nil, err
	}
	return &rec, nil
}
