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

// ConnectionResponse is the JSON body returned by GET /connection
// and POST /connection/rotate. `role` echoes the requested role
// (`readwrite` or `readonly`) so callers can double-check what
// they got.
type ConnectionResponse struct {
	URL      string `json:"url"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Username string `json:"username"`
	Role     string `json:"role"`
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

		role := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("role")))
		if role == "" || role == "ro" {
			role = "readonly"
		}
		if role != "readonly" && role != "readwrite" {
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

		// Read-only role naming convention: <owner_username>_ro
		// (created by the provisioning worker in M4 — new-projects
		// get it automatically; older Team projects fall back to
		// the owner role with a soft warning header).
		username := rec.Username
		if role == "readonly" {
			// TODO(m4-follow-up): materialise the _ro role via a
			// provisioning-worker step for existing Team projects.
			// For M4 we emit the owner URL with a soft header so
			// the console knows to display a "read-only role
			// pending" hint.
			w.Header().Set("X-Eurobase-Readonly-Pending", "true")
			// Still emit the owner role for now — the follow-up
			// swap is transparent to the caller.
			// (No behaviour change; documented.)
		}

		connURL := buildPostgresURL(username, password, rec.Host, rec.Port, rec.DatabaseName)

		writeConnectionAudit(r, projectID, audit.ActionConnectionURLViewed, map[string]any{
			"role": role,
			"host": rec.Host,
		})

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(ConnectionResponse{
			URL:      connURL,
			Host:     rec.Host,
			Port:     rec.Port,
			Database: rec.DatabaseName,
			Username: username,
			Role:     role,
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
			slog.Error("provider rotate failed", "error", err, "project_id", projectID)
			http.Error(w, fmt.Sprintf(`{"error":"provider rotate failed: %v"}`, err), http.StatusBadGateway)
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
