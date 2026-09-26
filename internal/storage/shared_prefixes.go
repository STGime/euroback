package storage

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/query"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Shared storage folders (#697). A rule in <schema>.storage_shared_prefixes
// makes every object under its prefix readable by all signed-in end users
// ('authenticated') or with just the public key ('public'); the
// storage_read RLS policy (migration 000130) applies it. Writes stay owner /
// service only. The routes run behind tenant.PlatformTenantContext, so the
// engine reaches a Team project's dedicated database and runs as the
// service role (console traffic).

const (
	ShareAuthenticated = "authenticated"
	SharePublic        = "public"
)

// SharedPrefix is one rule.
type SharedPrefix struct {
	Prefix     string    `json:"prefix"`
	Visibility string    `json:"visibility"`
	CreatedAt  time.Time `json:"created_at"`
}

// NormalizeSharedPrefix validates and normalizes a folder prefix: NFC like
// object keys, trailing "/" added, no leading "/", never under exports/.
func NormalizeSharedPrefix(p string) (string, error) {
	p = NormalizeStorageKey(strings.TrimSpace(p))
	if p == "" || p == "/" {
		return "", fmt.Errorf("prefix is required (a folder, e.g. \"themes/\")")
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("prefix must not start with \"/\"")
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	if len(p) > 1024 {
		return "", fmt.Errorf("prefix too long")
	}
	if strings.HasPrefix(p, "exports/") {
		return "", fmt.Errorf("the exports/ folder holds compliance archives and can't be shared")
	}
	return p, nil
}

// SharedPrefixRoutes mounts GET / (list), PUT / (set) and DELETE / (remove,
// ?prefix=) for the console. engine must resolve the tenant pool from the
// request (query.TenantPoolFromContext).
func SharedPrefixRoutes(engine *query.QueryEngine, requireWrite func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()
	r.Get("/", handleListSharedPrefixes(engine))
	r.With(requireWrite).Put("/", handlePutSharedPrefix(engine))
	r.With(requireWrite).Delete("/", handleDeleteSharedPrefix(engine))
	return r
}

func sharedPrefixTable(schema string) string {
	return fmt.Sprintf(`"%s".storage_shared_prefixes`, strings.ReplaceAll(schema, `"`, `""`))
}

func handleListSharedPrefixes(engine *query.QueryEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := query.SchemaFromContext(r.Context())
		out := make([]SharedPrefix, 0)
		err := engine.WithTenantTx(r.Context(), schema, func(tx pgx.Tx) error {
			rows, err := tx.Query(r.Context(),
				`SELECT prefix, visibility, created_at FROM `+sharedPrefixTable(schema)+` ORDER BY prefix`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var p SharedPrefix
				if err := rows.Scan(&p.Prefix, &p.Visibility, &p.CreatedAt); err != nil {
					return err
				}
				out = append(out, p)
			}
			return rows.Err()
		})
		if err != nil {
			writeSharedPrefixError(w, "list shared folders", err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func handlePutSharedPrefix(engine *query.QueryEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Prefix     string `json:"prefix"`
			Visibility string `json:"visibility"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		prefix, err := NormalizeSharedPrefix(req.Prefix)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		if req.Visibility != ShareAuthenticated && req.Visibility != SharePublic {
			http.Error(w, `{"error":"visibility must be \"authenticated\" or \"public\""}`, http.StatusBadRequest)
			return
		}
		schema := query.SchemaFromContext(r.Context())
		var out SharedPrefix
		err = engine.WithTenantTx(r.Context(), schema, func(tx pgx.Tx) error {
			return tx.QueryRow(r.Context(),
				`INSERT INTO `+sharedPrefixTable(schema)+` (prefix, visibility) VALUES ($1, $2)
				 ON CONFLICT (prefix) DO UPDATE SET visibility = EXCLUDED.visibility
				 RETURNING prefix, visibility, created_at`, prefix, req.Visibility,
			).Scan(&out.Prefix, &out.Visibility, &out.CreatedAt)
		})
		if err != nil {
			writeSharedPrefixError(w, "set shared folder", err)
			return
		}
		auditSharedPrefix(r, "storage_shared_prefix.set", prefix, req.Visibility)
		writeJSON(w, http.StatusOK, out)
	}
}

func handleDeleteSharedPrefix(engine *query.QueryEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		prefix, err := NormalizeSharedPrefix(r.URL.Query().Get("prefix"))
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		schema := query.SchemaFromContext(r.Context())
		var n int64
		err = engine.WithTenantTx(r.Context(), schema, func(tx pgx.Tx) error {
			tag, err := tx.Exec(r.Context(), `DELETE FROM `+sharedPrefixTable(schema)+` WHERE prefix = $1`, prefix)
			n = tag.RowsAffected()
			return err
		})
		if err != nil {
			writeSharedPrefixError(w, "remove shared folder", err)
			return
		}
		if n == 0 {
			http.Error(w, `{"error":"no such shared folder"}`, http.StatusNotFound)
			return
		}
		auditSharedPrefix(r, "storage_shared_prefix.removed", prefix, "")
		w.WriteHeader(http.StatusNoContent)
	}
}

func auditSharedPrefix(r *http.Request, action, prefix, visibility string) {
	a := audit.FromContext(r.Context())
	if a == nil {
		return
	}
	aid, aemail := audit.ActorFromContext(r.Context())
	meta := map[string]any{"prefix": prefix}
	if visibility != "" {
		meta["visibility"] = visibility
	}
	a.Log(r.Context(), query.ProjectIDFromContext(r.Context()), aid, aemail, action,
		audit.WithTarget("storage_shared_prefix", prefix), audit.WithMetadata(meta))
}

func writeSharedPrefixError(w http.ResponseWriter, op string, err error) {
	slog.Error("storage: "+op+" failed", "error", err)
	writeTenantPoolError(w, err)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
