package enduser

import (
	"context"
	"errors"
	"log/slog"
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
// single-use semantics. Expired rows are rejected (and quietly skipped by
// the WHERE clause). Returns ErrOAuthStateNotFound if no matching unexpired
// row exists.
//
// Bulk cleanup of expired rows runs out of band — see RunOAuthStateSweeper
// — so this hot path stays a single indexed DELETE … RETURNING rather than
// also doing a full-table sweep on every miss.
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
			return nil, ErrOAuthStateNotFound
		}
		return nil, err
	}
	return &rec, nil
}

// CleanupExpiredOAuthStates deletes every oauth_states row whose
// expires_at is in the past. The query is an indexed range scan
// (idx_oauth_states_expires) so it's O(deleted), not O(table).
// Returns the number of rows removed.
func CleanupExpiredOAuthStates(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	tag, err := pool.Exec(ctx, `DELETE FROM public.oauth_states WHERE expires_at <= now()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// RunOAuthStateSweeper runs CleanupExpiredOAuthStates on `every` cadence
// until ctx is cancelled. Closes #58 — the previous design only swept
// opportunistically on lookup misses, which both pile up rows during
// quiet periods and emit a redundant full-scan DELETE on every miss
// during busy periods.
//
// Errors are logged and swallowed — losing one sweep is harmless; the
// next tick will pick up the same rows.
func RunOAuthStateSweeper(ctx context.Context, pool *pgxpool.Pool, every time.Duration) {
	if every <= 0 {
		every = 10 * time.Minute
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := CleanupExpiredOAuthStates(ctx, pool)
			if err != nil {
				// Tick log; cancellation also lands here.
				if ctx.Err() != nil {
					return
				}
				slog.Warn("oauth state sweeper failed", "error", err)
				continue
			}
			if n > 0 {
				slog.Debug("oauth state sweeper removed expired rows", "count", n)
			}
		}
	}
}
