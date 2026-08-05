package tenant

// Direct-database-URL exposure (Team-tier M4).
//
// Team-tier and above get a dedicated managed-PG instance
// (provisioned in M1, routed via M2.5). This surface hands the
// tenant a real `postgres://` connection string they can point
// Payload / Prisma / Drizzle / Directus / psql at.
//
// Gate: plans.CheckDedicatedDB (402 for Free/Pro).
//
// Two endpoints:
//   * GET  /platform/projects/{id}/connection[?role=readwrite]
//     — returns the current URL. Read-only by default; ?role=readwrite
//       returns the owner-role URL. Actor is audited every time the
//       URL is fetched.
//   * POST /platform/projects/{id}/connection/rotate
//     — rotates the tenant DB password via the provider, re-seals
//       the row's password column, returns a fresh URL. Old URL is
//       invalidated immediately at the Scaleway side.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/dbprovider"
	"github.com/eurobase/euroback/internal/plans"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ConnectionService owns the read + rotate endpoints. Depends on the
// dbprovider registry (rotation calls provider.RotatePassword —
// added in this milestone) and cipher (seals the fresh password back
// into project_databases).
type ConnectionService struct {
	pool     *pgxpool.Pool
	registry *dbprovider.Registry
	cipher   *dbprovider.Cipher
	repo     *dbprovider.Repo
	limits   *plans.LimitsService
	// poolCache is nil when Team-tier routing is disabled. When set,
	// rotate paths evict the cached pool so subsequent SDK traffic
	// opens a fresh pool with the new password. Without eviction, the
	// pgxpool.Pool's cached DSN keeps the old password and new
	// connections start failing auth within ~30 min (as the pool
	// cycles past MaxConnLifetime).
	poolCache *dbprovider.PoolCache
}

func NewConnectionService(pool *pgxpool.Pool, registry *dbprovider.Registry, cipher *dbprovider.Cipher, limits *plans.LimitsService) *ConnectionService {
	return &ConnectionService{
		pool:     pool,
		registry: registry,
		cipher:   cipher,
		repo:     dbprovider.NewRepo(pool),
		limits:   limits,
	}
}

// WithPoolCache attaches the gateway's per-project pool cache so
// /connection/rotate can evict the cached pool immediately after a
// password rotation. Nil-safe — a service constructed without a
// cache still functions (rotate + return new URL), but stale pool
// entries will keep serving until they cycle past MaxConnLifetime.
func (s *ConnectionService) WithPoolCache(c *dbprovider.PoolCache) *ConnectionService {
	s.poolCache = c
	return s
}

// ConnectionResponse is the JSON body returned by GET /connection
// and POST /connection/rotate. `role` is the *effective* role the
// URL grants (never a promise the caller can't verify), so a caller
// that asked for "readonly" while the _ro role is still being
// provisioned sees `role: "readwrite"` + `readonly_pending: true`
// and knows to treat the URL as a bearer write credential.
type ConnectionResponse struct {
	URL             string `json:"url"`
	Host            string `json:"host"`
	Port            int    `json:"port"`
	Database        string `json:"database"`
	Username        string `json:"username"`
	Role            string `json:"role"`
	ReadonlyPending bool   `json:"readonly_pending,omitempty"`
}

// HandleGetConnection — GET /platform/projects/{id}/connection[?role=readwrite].
// Default role is `readonly`. Audited every call.
func (s *ConnectionService) HandleGetConnection() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if projectID == "" {
			http.Error(w, `{"error":"project id required"}`, http.StatusBadRequest)
			return
		}
		if err := s.limits.CheckDedicatedDB(r.Context(), projectID); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q,"code":"dedicated_db_required"}`, err.Error()), http.StatusPaymentRequired)
			return
		}

		requestedRole := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("role")))
		switch requestedRole {
		case "", "ro":
			requestedRole = "readonly"
		case "rw":
			requestedRole = "readwrite"
		}
		if requestedRole != "readonly" && requestedRole != "readwrite" {
			http.Error(w, `{"error":"role must be readonly or readwrite"}`, http.StatusBadRequest)
			return
		}

		rec, err := s.repo.GetLiveByProject(r.Context(), projectID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				http.Error(w, `{"error":"project has no active dedicated database"}`, http.StatusConflict)
				return
			}
			slog.Error("connection lookup failed", "error", err, "project_id", projectID)
			http.Error(w, `{"error":"lookup failed"}`, http.StatusInternalServerError)
			return
		}

		password, err := s.cipher.Open(rec.PasswordCiphertext, rec.PasswordNonce, rec.PasswordKeyVersion)
		if err != nil {
			slog.Error("connection cipher open failed", "error", err, "project_id", projectID)
			http.Error(w, `{"error":"credential unavailable"}`, http.StatusInternalServerError)
			return
		}

		// Read-only role convention: <owner_username>_ro. Not yet
		// materialised — TODO(m4-follow-up) provisions it on the
		// dedicated instance. Until then a `?role=readonly` request
		// still emits the *owner* URL, but we tell the truth in the
		// JSON body: the effective role is "readwrite" and
		// `readonly_pending=true`. The console renders both the
		// destructive-access warning and the "read-only role pending"
		// banner off that flag — a header-only signal was lost by the
		// SPA's fetch wrapper (drops res.headers) and could mislead a
		// customer into handing an owner URL to an analyst.
		username := rec.Username
		effectiveRole := "readwrite"
		readonlyPending := requestedRole == "readonly"

		connURL := buildPostgresURL(username, password, rec.Host, rec.Port, rec.DatabaseName)

		writeConnectionAudit(r, projectID, audit.ActionConnectionURLViewed, map[string]any{
			"requested_role":   requestedRole,
			"effective_role":   effectiveRole,
			"readonly_pending": readonlyPending,
			"host":             rec.Host,
		})

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(ConnectionResponse{
			URL:             connURL,
			Host:            rec.Host,
			Port:            rec.Port,
			Database:        rec.DatabaseName,
			Username:        username,
			Role:            effectiveRole,
			ReadonlyPending: readonlyPending,
		})
	}
}

// HandleRotateConnection — POST /platform/projects/{id}/connection/rotate.
// Rotates the owner-role password via the provider, re-seals into
// project_databases, and returns a fresh URL. The old URL is
// unusable within seconds (Scaleway propagation is near-real-time).
func (s *ConnectionService) HandleRotateConnection() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if projectID == "" {
			http.Error(w, `{"error":"project id required"}`, http.StatusBadRequest)
			return
		}
		if err := s.limits.CheckDedicatedDB(r.Context(), projectID); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q,"code":"dedicated_db_required"}`, err.Error()), http.StatusPaymentRequired)
			return
		}

		rec, err := s.repo.GetLiveByProject(r.Context(), projectID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				http.Error(w, `{"error":"project has no active dedicated database"}`, http.StatusConflict)
				return
			}
			http.Error(w, `{"error":"lookup failed"}`, http.StatusInternalServerError)
			return
		}

		provider, err := s.registry.Get(rec.Provider)
		if err != nil {
			http.Error(w, `{"error":"provider not available"}`, http.StatusInternalServerError)
			return
		}

		rotator, ok := provider.(dbprovider.PasswordRotator)
		if !ok {
			slog.Error("provider does not support password rotation", "provider", rec.Provider)
			http.Error(w, `{"error":"provider does not support rotation"}`, http.StatusNotImplemented)
			return
		}

		// Generate fresh 32-byte hex password (matches the shape
		// Scaleway.Provision uses).
		newPassword, err := randomHexPassword(32)
		if err != nil {
			http.Error(w, `{"error":"password generation failed"}`, http.StatusInternalServerError)
			return
		}

		if err := rotator.RotatePassword(r.Context(), rec.ProviderInstanceID, rec.Username, newPassword); err != nil {
			// Provider-side detail (instance IDs, provider URLs) stays
			// in slog — do not echo `err` to the tenant, both to avoid
			// leaking internal identifiers and to keep the JSON body
			// well-formed (err may contain quotes / newlines).
			slog.Error("provider rotate failed", "error", err, "project_id", projectID)
			http.Error(w, `{"error":"provider rotate failed"}`, http.StatusBadGateway)
			return
		}

		ct, nonce, ver, err := s.cipher.Seal(newPassword)
		if err != nil {
			// Password rotated at provider but we can't seal — the
			// console user is locked out and only ops can recover.
			// Return 500 with loud logging so the incident is visible.
			slog.Error("cipher seal after rotate failed — provider updated but local cache broken",
				"error", err, "project_id", projectID)
			http.Error(w, `{"error":"password rotated at provider but local update failed — contact support"}`, http.StatusInternalServerError)
			return
		}

		if _, err := s.pool.Exec(r.Context(),
			`UPDATE public.project_databases
			    SET password_ciphertext  = $2,
			        password_nonce       = $3,
			        password_key_version = $4
			  WHERE id = $1::uuid`,
			rec.ID, ct, nonce, ver,
		); err != nil {
			slog.Error("password_ciphertext update failed", "error", err, "project_id", projectID)
			http.Error(w, `{"error":"password rotated at provider but DB write failed — contact support"}`, http.StatusInternalServerError)
			return
		}

		// Evict the cached pool (M2.5) so subsequent SDK traffic
		// opens fresh connections against the new password. Without
		// this, the pgxpool.Pool retains the old password in its
		// pinned config; new connections opened after MaxConnLifetime
		// (30 min) would auth with the old creds and Scaleway would
		// reject them — the tenant's SDK traffic starts failing with
		// no signal tying it back to the rotate. Local-pod eviction
		// only in part 1; cross-pod via LISTEN/NOTIFY lands in part 2.
		if s.poolCache != nil {
			s.poolCache.Evict(projectID)
		}

		writeConnectionAudit(r, projectID, audit.ActionConnectionURLRotated, map[string]any{
			"host": rec.Host,
		})

		connURL := buildPostgresURL(rec.Username, newPassword, rec.Host, rec.Port, rec.DatabaseName)

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(ConnectionResponse{
			URL:      connURL,
			Host:     rec.Host,
			Port:     rec.Port,
			Database: rec.DatabaseName,
			Username: rec.Username,
			Role:     "readwrite",
		})
	}
}

// buildPostgresURL assembles a standard postgres:// URL. Password
// is URL-encoded so hex characters (always safe) + potential future
// special characters land in the URL without breakage.
func buildPostgresURL(user, password, host string, port int, db string) string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   fmt.Sprintf("%s:%d", host, port),
		Path:   "/" + db,
	}
	// Managed-PG providers universally require TLS; make it explicit
	// in the URL so a client that defaults to sslmode=disable still
	// negotiates TLS.
	q := u.Query()
	q.Set("sslmode", "require")
	u.RawQuery = q.Encode()
	return u.String()
}

func randomHexPassword(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func writeConnectionAudit(r *http.Request, projectID, action string, metadata map[string]any) {
	svc := audit.FromContext(r.Context())
	if svc == nil {
		return
	}
	var actorID, actorEmail string
	if claims, ok := auth.ClaimsFromContext(r.Context()); ok && claims != nil {
		actorID = claims.Subject
		actorEmail = claims.Email
	}
	svc.Log(r.Context(), projectID, actorID, actorEmail, action,
		audit.WithMetadata(metadata),
		audit.WithIP(r.RemoteAddr))
}
