package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LogEntry represents a single request log to be batch-inserted.
type LogEntry struct {
	ProjectID  string
	Method     string
	Path       string
	StatusCode int
	LatencyMs  int
	IPAddress  string
	UserAgent  string
}

// statusCapture wraps http.ResponseWriter to capture the status code.
type statusCapture struct {
	http.ResponseWriter
	code int
}

func (sc *statusCapture) WriteHeader(code int) {
	sc.code = code
	sc.ResponseWriter.WriteHeader(code)
}

// RequestLoggingMiddleware returns middleware that sends log entries to the provided channel.
func RequestLoggingMiddleware(logCh chan<- LogEntry) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			sc := &statusCapture{ResponseWriter: w, code: http.StatusOK}
			next.ServeHTTP(sc, r)

			projectID := chi.URLParam(r, "id")
			if projectID == "" {
				return
			}

			entry := LogEntry{
				ProjectID:  projectID,
				Method:     r.Method,
				Path:       r.URL.Path,
				StatusCode: sc.code,
				LatencyMs:  int(time.Since(start).Milliseconds()),
				IPAddress:  r.RemoteAddr,
				UserAgent:  r.UserAgent(),
			}

			// Non-blocking send.
			select {
			case logCh <- entry:
			default:
				slog.Warn("request log channel full, dropping entry")
			}
		})
	}
}

// StartLogWriter starts a background goroutine that batch-inserts log entries.
// It flushes every 1s or when 100 entries are buffered.
func StartLogWriter(ctx context.Context, pool *pgxpool.Pool, logCh <-chan LogEntry) {
	go func() {
		buf := make([]LogEntry, 0, 100)
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		flush := func() {
			if len(buf) == 0 {
				return
			}
			if err := batchInsertLogs(ctx, pool, buf); err != nil {
				slog.Error("failed to batch insert request logs", "error", err, "count", len(buf))
			}
			buf = buf[:0]
		}

		for {
			select {
			case <-ctx.Done():
				flush()
				return
			case entry, ok := <-logCh:
				if !ok {
					flush()
					return
				}
				buf = append(buf, entry)
				if len(buf) >= 100 {
					flush()
				}
			case <-ticker.C:
				flush()
			}
		}
	}()
}

func batchInsertLogs(ctx context.Context, pool *pgxpool.Pool, entries []LogEntry) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for _, e := range entries {
		_, err := tx.Exec(ctx,
			`INSERT INTO request_logs (project_id, method, path, status_code, latency_ms, ip_address, user_agent)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			e.ProjectID, e.Method, e.Path, e.StatusCode, e.LatencyMs, e.IPAddress, e.UserAgent,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// StartLogCleanup starts a background goroutine that deletes logs older than 7 days.
func StartLogCleanup(ctx context.Context, pool *pgxpool.Pool) {
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()

		// Run once on startup.
		cleanupOldLogs(ctx, pool)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanupOldLogs(ctx, pool)
			}
		}
	}()
}

func cleanupOldLogs(ctx context.Context, pool *pgxpool.Pool) {
	result, err := pool.Exec(ctx, `DELETE FROM request_logs WHERE created_at < now() - interval '7 days'`)
	if err != nil {
		slog.Error("failed to clean up old request logs", "error", err)
		return
	}
	if result.RowsAffected() > 0 {
		slog.Info("cleaned up old request logs", "deleted", result.RowsAffected())
	}
}
