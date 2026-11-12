package realtime

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// KeyCheck returns which of the given API key hashes still belong to an
// active project.
type KeyCheck func(ctx context.Context, hashes []string) (map[string]bool, error)

// PoolKeyCheck checks key hashes against api_keys, with the same rule the
// connect-time lookup uses (the key exists and its project is active).
func PoolKeyCheck(pool *pgxpool.Pool) KeyCheck {
	return func(ctx context.Context, hashes []string) (map[string]bool, error) {
		rows, err := pool.Query(ctx,
			`SELECT ak.key_hash FROM api_keys ak
			   JOIN projects p ON p.id = ak.project_id
			  WHERE ak.key_hash = ANY($1) AND p.status = 'active'`, hashes)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		live := make(map[string]bool, len(hashes))
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				return nil, err
			}
			live[h] = true
		}
		return live, rows.Err()
	}
}

// SweepRevokedKeys closes the connections whose API key no longer exists
// (rotated or regenerated) or whose project is no longer active, and
// returns how many it closed. An API key is checked only when a
// connection opens; this ends access for connections that are already
// open. If the check fails, nothing is closed.
func (h *Hub) SweepRevokedKeys(ctx context.Context, check KeyCheck) int {
	h.mu.RLock()
	byHash := make(map[string][]*Client)
	for c := range h.keyed {
		byHash[c.apiKeyHash] = append(byHash[c.apiKeyHash], c)
	}
	h.mu.RUnlock()
	if len(byHash) == 0 {
		return 0
	}
	hashes := make([]string, 0, len(byHash))
	for k := range byHash {
		hashes = append(hashes, k)
	}
	live, err := check(ctx, hashes)
	if err != nil {
		slog.Warn("realtime key sweep: lookup failed", "error", err)
		return 0
	}
	closed := 0
	for hash, clients := range byHash {
		if live[hash] {
			continue
		}
		for _, c := range clients {
			// Closing the connection ends readPump, which unregisters
			// the client.
			if c.conn != nil {
				c.conn.Close()
			}
			closed++
		}
	}
	if closed > 0 {
		slog.Info("realtime: closed connections whose API key was revoked", "count", closed)
	}
	return closed
}

// RunKeySweep runs SweepRevokedKeys every interval until ctx ends.
func (h *Hub) RunKeySweep(ctx context.Context, interval time.Duration, check KeyCheck) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			h.SweepRevokedKeys(sctx, check)
			cancel()
		}
	}
}
