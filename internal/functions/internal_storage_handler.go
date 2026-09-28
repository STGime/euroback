package functions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	edb "github.com/eurobase/euroback/internal/db"
	"github.com/eurobase/euroback/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Internal endpoints that the function-runner pod calls back to the
// gateway when user code invokes ctx.storage.upload / createSignedUrl /
// delete. Closes #85.
//
// Auth: HMAC-SHA256 over a canonical message (see storage_hmac.go).
// Same shared secret as the gateway → runner direction
// (FUNCTIONS_RUNNER_HMAC_SECRET); the canonical and signature header
// names are different so signatures can't be replayed across
// directions.
//
// Routing: mounted under `/internal/functions/storage/*`. The cluster
// Ingress only exposes `/v1/*`, `/platform/*`, and `/health`, so this
// path is unreachable from outside the cluster. Only the runner pod
// has the HMAC secret.

// InternalStorageHandler bundles dependencies for the runner-facing
// storage RPC endpoints.
type InternalStorageHandler struct {
	pool       *pgxpool.Pool
	s3         *storage.S3Client
	hmacSecret []byte

	// TenantPool resolves the pool that holds the project's
	// storage_objects (#676): a Team project's dedicated database
	// (runtime pool) or an error — never the shared cluster, which has no
	// schema for a project created as Team and a stale copy for one
	// upgraded from Pro. Nil = every project is on the shared cluster
	// (h.pool). Wired by the gateway router with the same rules as the
	// SDK's tenant-pool middleware.
	TenantPool func(ctx context.Context, projectID string) (*pgxpool.Pool, error)
}

// ErrTenantPoolUnavailable: the project's storage metadata lives on a
// database that can't be reached right now.
var ErrTenantPoolUnavailable = errors.New("the project's database is not available right now")

// metaPool returns the pool for the project's storage metadata.
func (h *InternalStorageHandler) metaPool(ctx context.Context, projectID string) (*pgxpool.Pool, error) {
	if h.TenantPool == nil {
		return h.pool, nil
	}
	return h.TenantPool(ctx, projectID)
}

// refuseUnavailable answers 503 when the project's metadata pool can't be
// resolved — before anything reaches S3, so no object is left untracked.
func refuseUnavailable(w http.ResponseWriter, projectID string, err error) {
	slog.Error("internal storage: project database unavailable (no shared fallback)", "project_id", projectID, "error", err)
	http.Error(w, `{"error":"the project's database is not available right now; retry shortly"}`, http.StatusServiceUnavailable)
}

// NewInternalStorageHandler constructs the handler. Returns an error if
// the HMAC secret is too short (matches the existing functions.Signer
// minimum so configs are interchangeable).
func NewInternalStorageHandler(pool *pgxpool.Pool, s3 *storage.S3Client, hmacSecret string) (*InternalStorageHandler, error) {
	if len(hmacSecret) < minSecretLen {
		return nil, fmt.Errorf("functions runner HMAC secret must be at least %d bytes", minSecretLen)
	}
	return &InternalStorageHandler{pool: pool, s3: s3, hmacSecret: []byte(hmacSecret)}, nil
}

// Routes returns a chi.Router with the three storage RPC endpoints.
func (h *InternalStorageHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/upload", h.upload)
	r.Post("/signed-url", h.signedURL)
	r.Delete("/*", h.delete)
	return r
}

// refuseExportArchive answers 403 and returns true when key is in the
// project's compliance export namespace (#655). Edge functions never need
// the archives: they can't write or delete them (the export lifecycle
// owns them), and can't sign download URLs for them either — a function
// that signs whatever key a request names would otherwise hand the
// project's full data dump to any end user.
func refuseExportArchive(w http.ResponseWriter, projectID, key string) bool {
	if !storage.IsExportArchiveKey(projectID, key) {
		return false
	}
	http.Error(w, storage.ErrExportsReadOnlyJSON, http.StatusForbidden)
	return true
}

// projectMeta resolves project_id → (slug, schema_name). Both are
// derived server-side and never trusted from a header.
func (h *InternalStorageHandler) projectMeta(ctx context.Context, projectID string) (slug, schema string, err error) {
	err = h.pool.QueryRow(ctx,
		`SELECT slug, schema_name FROM public.projects WHERE id = $1`,
		projectID,
	).Scan(&slug, &schema)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", fmt.Errorf("project not found")
		}
		return "", "", fmt.Errorf("project lookup: %w", err)
	}
	return slug, schema, nil
}

// recordUpload mirrors what storage.UploadFile does after a successful
// S3 PUT — write to <schema>.storage_objects so usage tracking and
// download visibility checks see the file. Unlike SDK uploads (which
// never change an existing owner), a function sets uploaded_by
// outright: function code is the project developer's and runs as
// service role, so it may assign or reassign ownership deliberately.
func (h *InternalStorageHandler) recordUpload(ctx context.Context, pool *pgxpool.Pool, schema, key, contentType, userID string, size int64) {
	if schema == "" || pool == nil {
		return
	}
	escSchema := strings.ReplaceAll(schema, `"`, `""`)
	q := fmt.Sprintf(
		`INSERT INTO "%s".storage_objects (key, content_type, size_bytes, uploaded_by)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (key) DO UPDATE SET content_type = $2, size_bytes = $3, uploaded_by = $4`,
		escSchema,
	)
	// A user-less invocation (service context) uploads a developer file:
	// owner NULL — not '' (not a uuid: the insert failed and every such
	// upload was left untracked in S3).
	var owner any
	if userID != "" {
		owner = userID
	}
	if err := edb.RunAsService(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, q, key, contentType, size, owner)
		return err
	}); err != nil {
		slog.Error("internal storage: failed to record upload in storage_objects",
			"error", err, "schema", schema, "key", key)
	}
}

// upload accepts a raw body PUT; key is in X-Storage-Key, content type
// in Content-Type. HMAC covers project / schema / user / key / body
// hash.
func (h *InternalStorageHandler) upload(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 50<<20))
	if err != nil {
		http.Error(w, `{"error":"body too large or read failure"}`, http.StatusBadRequest)
		return
	}
	if err := VerifyStorage(h.hmacSecret, r.Header, StorageOpUpload, SHA256Hex(body), VerifyOptions{}); err != nil {
		slog.Warn("internal storage upload: HMAC verify failed", "error", err)
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	projectID := r.Header.Get("X-Project-ID")
	userID := r.Header.Get("X-User-ID")
	key := r.Header.Get("X-Storage-Key")
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if projectID == "" || key == "" {
		http.Error(w, `{"error":"missing project or key"}`, http.StatusBadRequest)
		return
	}
	if err := storage.ValidateStorageKey(key); err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	if refuseExportArchive(w, projectID, key) {
		return
	}

	slug, schema, err := h.projectMeta(r.Context(), projectID)
	if err != nil {
		slog.Warn("internal storage upload: project lookup failed", "error", err, "project_id", projectID)
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	bucket := "eurobase-" + slug
	metaPool, err := h.metaPool(r.Context(), projectID)
	if err != nil {
		refuseUnavailable(w, projectID, err)
		return
	}

	if err := h.s3.UploadObject(r.Context(), bucket, key, bytes.NewReader(body), contentType, int64(len(body))); err != nil {
		slog.Error("internal storage upload: S3 put failed", "error", err, "bucket", bucket, "key", key)
		http.Error(w, `{"error":"upload failed"}`, http.StatusBadGateway)
		return
	}

	h.recordUpload(r.Context(), metaPool, schema, key, contentType, userID, int64(len(body)))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"key":          key,
		"content_type": contentType,
		"size":         len(body),
	})
}

type signedURLReq struct {
	Key         string `json:"key"`
	Operation   string `json:"operation"` // "upload" or "download"
	ExpiresIn   int    `json:"expires_in,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

// signedURL issues a presigned S3 URL. HMAC covers project / schema /
// user / key / body hash. The body itself is the JSON request — its
// content-sha256 is what's signed (so swapping operation or expiresIn
// invalidates the signature).
func (h *InternalStorageHandler) signedURL(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil {
		http.Error(w, `{"error":"body read failure"}`, http.StatusBadRequest)
		return
	}
	if err := VerifyStorage(h.hmacSecret, r.Header, StorageOpSignedURL, SHA256Hex(body), VerifyOptions{}); err != nil {
		slog.Warn("internal storage signed-url: HMAC verify failed", "error", err)
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	var req signedURLReq
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if err := storage.ValidateStorageKey(req.Key); err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	if req.Operation != "upload" && req.Operation != "download" {
		http.Error(w, `{"error":"operation must be upload or download"}`, http.StatusBadRequest)
		return
	}
	// Storage-key in the canonical message must match the JSON body's
	// key — otherwise a runner could sign one key and request another.
	if r.Header.Get("X-Storage-Key") != req.Key {
		http.Error(w, `{"error":"key mismatch between header and body"}`, http.StatusBadRequest)
		return
	}

	projectID := r.Header.Get("X-Project-ID")
	if projectID == "" {
		http.Error(w, `{"error":"missing project"}`, http.StatusBadRequest)
		return
	}
	if refuseExportArchive(w, projectID, req.Key) {
		return
	}
	slug, _, err := h.projectMeta(r.Context(), projectID)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	bucket := "eurobase-" + slug

	var (
		url    string
		expiry time.Duration
	)
	switch req.Operation {
	case "upload":
		if req.ContentType == "" {
			req.ContentType = "application/octet-stream"
		}
		if req.ExpiresIn > 0 {
			expiry = time.Duration(req.ExpiresIn) * time.Second
		} else {
			expiry = 15 * time.Minute
		}
		url, err = h.s3.GeneratePresignedUploadURL(r.Context(), bucket, req.Key, req.ContentType, expiry)
	case "download":
		if req.ExpiresIn > 0 {
			expiry = time.Duration(req.ExpiresIn) * time.Second
		} else {
			expiry = 1 * time.Hour
		}
		url, err = h.s3.GeneratePresignedDownloadURL(r.Context(), bucket, req.Key, expiry)
	}
	if err != nil {
		slog.Error("internal storage signed-url generation failed", "error", err, "bucket", bucket, "key", req.Key)
		http.Error(w, `{"error":"failed to generate signed URL"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"url":        url,
		"expires_at": time.Now().Add(expiry),
	})
}

// delete removes an object. Body is empty (SHA-256 of empty string).
func (h *InternalStorageHandler) delete(w http.ResponseWriter, r *http.Request) {
	if err := VerifyStorage(h.hmacSecret, r.Header, StorageOpDelete, SHA256Hex(nil), VerifyOptions{}); err != nil {
		slog.Warn("internal storage delete: HMAC verify failed", "error", err)
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	key := r.Header.Get("X-Storage-Key")
	projectID := r.Header.Get("X-Project-ID")
	if projectID == "" || key == "" {
		http.Error(w, `{"error":"missing project or key"}`, http.StatusBadRequest)
		return
	}
	if err := storage.ValidateStorageKey(key); err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	if refuseExportArchive(w, projectID, key) {
		return
	}
	slug, schema, err := h.projectMeta(r.Context(), projectID)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	bucket := "eurobase-" + slug
	metaPool, err := h.metaPool(r.Context(), projectID)
	if err != nil {
		refuseUnavailable(w, projectID, err)
		return
	}

	if err := h.s3.DeleteObject(r.Context(), bucket, key); err != nil {
		slog.Error("internal storage delete: S3 delete failed", "error", err, "bucket", bucket, "key", key)
		http.Error(w, `{"error":"delete failed"}`, http.StatusBadGateway)
		return
	}
	if schema != "" && metaPool != nil {
		escSchema := strings.ReplaceAll(schema, `"`, `""`)
		q := fmt.Sprintf(`DELETE FROM "%s".storage_objects WHERE key = $1`, escSchema)
		_ = edb.RunAsService(r.Context(), metaPool, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, q, key)
			return err
		})
	}

	w.WriteHeader(http.StatusNoContent)
}
