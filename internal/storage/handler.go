package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/auth"
	edb "github.com/eurobase/euroback/internal/db"
	"github.com/eurobase/euroback/internal/query"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// maxUploadSize is the gateway-enforced maximum for multipart uploads (50 MB).
const maxUploadSize = 50 << 20 // 50 MB

// StorageHandler holds dependencies for the storage HTTP handlers.
type StorageHandler struct {
	s3     *S3Client
	pool   *pgxpool.Pool
	engine *query.QueryEngine
}

// NewStorageHandler creates a new StorageHandler backed by the given S3Client
// and database pool (used to track uploads in storage_objects). The query
// engine is used to run RLS-aware ownership checks before S3 fetches so an
// end-user can't download another end-user's files by guessing the key.
func NewStorageHandler(s3 *S3Client, pool *pgxpool.Pool, engine *query.QueryEngine) *StorageHandler {
	return &StorageHandler{s3: s3, pool: pool, engine: engine}
}

// assertObjectVisible runs a short SELECT against storage_objects under the
// caller's RLS context. End-user JWT requests are filtered by the
// storage_owner_access policy so only the caller's own files pass the check.
// Platform-admin / secret-key requests see every row (is_service_role
// bypass). An empty/missing context falls through to the anon role which
// should never see a row.
//
// Returns true if the caller may act on the object, false otherwise.
// If the engine or schema is unset (e.g. in tests) the check is skipped
// and true is returned — the gateway still requires isAuthenticated().
func (h *StorageHandler) assertObjectVisible(r *http.Request, key string) (bool, error) {
	if h.engine == nil {
		return true, nil
	}
	schema := h.schemaForRequest(r)
	if schema == "" {
		return true, nil
	}
	var exists bool
	err := h.engine.WithTenantTx(r.Context(), schema, func(tx pgx.Tx) error {
		q := fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM "%s".storage_objects WHERE key = $1)`,
			strings.ReplaceAll(schema, `"`, `""`))
		return tx.QueryRow(r.Context(), q, key).Scan(&exists)
	})
	return exists, err
}

// isAuthenticated checks whether the request has valid auth claims —
// either platform claims (console/platform access) or end-user claims
// (SDK access with end-user JWT). Returns the user ID and true if authenticated.
func isAuthenticated(r *http.Request) (string, bool) {
	// Check end-user claims first (SDK path: /v1/storage).
	if eu, ok := auth.EndUserClaimsFromContext(r.Context()); ok && eu != nil {
		return eu.UserID, true
	}
	// Fall back to platform claims (console path: /platform/.../storage).
	if pc, ok := auth.ClaimsFromContext(r.Context()); ok && pc != nil {
		return pc.Subject, true
	}
	return "", false
}

// bucketForRequest derives the tenant's S3 bucket name from the request
// context. The bucket naming convention is "eurobase-{slug}". The slug is
// provided via the X-Project-Slug header.
func bucketForRequest(r *http.Request) (string, error) {
	slug := r.Header.Get("X-Project-Slug")
	if slug == "" {
		return "", fmt.Errorf("missing X-Project-Slug header")
	}
	return "eurobase-" + slug, nil
}

// Routes returns a chi.Router with all storage sub-routes mounted.
func (h *StorageHandler) Routes() chi.Router {
	r := chi.NewRouter()

	r.Post("/upload", h.UploadFile)
	r.Post("/signed-url", h.GenerateSignedURL)
	r.Get("/", h.ListFiles)

	// Wildcard routes for object keys that may contain slashes.
	r.Get("/*", h.DownloadFile)
	r.Delete("/*", h.DeleteFile)

	return r
}

// ---------- Upload ----------

// uploadResponse is returned on successful file upload.
type uploadResponse struct {
	Key         string `json:"key"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

// UploadFile handles POST /v1/storage/upload.
// Accepts multipart/form-data with a "file" field and an optional "key" field.
// Streams directly to S3 without buffering the entire file in memory.
func (h *StorageHandler) UploadFile(w http.ResponseWriter, r *http.Request) {
	userID, ok := isAuthenticated(r)
	if !ok {
		slog.Warn("storage upload called without auth claims")
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	bucket, err := bucketForRequest(r)
	if err != nil {
		slog.Warn("storage upload missing project slug", "error", err)
		http.Error(w, `{"error":"missing X-Project-Slug header"}`, http.StatusBadRequest)
		return
	}

	// Enforce max upload size on the entire request body.
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		slog.Warn("storage upload: failed to parse multipart form", "error", err)
		http.Error(w, `{"error":"request must be multipart/form-data (max 50MB)"}`, http.StatusBadRequest)
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			r.MultipartForm.RemoveAll()
		}
	}()

	file, header, err := r.FormFile("file")
	if err != nil {
		slog.Warn("storage upload: missing file field", "error", err)
		http.Error(w, `{"error":"file field is required"}`, http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Determine the storage key.
	key := strings.TrimSpace(r.FormValue("key"))
	if key == "" {
		key = header.Filename
	}
	if key == "" {
		http.Error(w, `{"error":"file name or key is required"}`, http.StatusBadRequest)
		return
	}

	// Determine content type.
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	size := header.Size

	slog.Info("storage upload starting",
		"bucket", bucket,
		"key", key,
		"content_type", contentType,
		"size", size,
		"user", userID,
	)

	if err := h.s3.UploadObject(r.Context(), bucket, key, file, contentType, size); err != nil {
		slog.Error("storage upload failed", "error", err, "bucket", bucket, "key", key)
		http.Error(w, `{"error":"failed to upload file"}`, http.StatusInternalServerError)
		return
	}

	// Record the upload in storage_objects so usage tracking works.
	if schema := h.schemaForRequest(r); schema != "" && h.pool != nil {
		escSchema := strings.ReplaceAll(schema, `"`, `""`)
		q := fmt.Sprintf(
			`INSERT INTO "%s".storage_objects (key, content_type, size_bytes, uploaded_by)
			 VALUES ($1, $2, $3, $4)
			 ON CONFLICT (key) DO UPDATE SET content_type = $2, size_bytes = $3, uploaded_by = $4`,
			escSchema,
		)
		if err := edb.RunAsService(r.Context(), h.pool, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, q, key, contentType, size, userID)
			return err
		}); err != nil {
			// Non-fatal: the file is already in S3, just log the tracking failure.
			slog.Error("storage: failed to record upload in storage_objects",
				"error", err, "schema", schema, "key", key)
		}
	}

	resp := uploadResponse{
		Key:         key,
		ContentType: contentType,
		Size:        size,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

// ---------- Download ----------

// DownloadFile handles GET /v1/storage/{key...}.
// Streams the file back to the client with the proper Content-Type and
// Content-Length headers.
func (h *StorageHandler) DownloadFile(w http.ResponseWriter, r *http.Request) {
	_, ok := isAuthenticated(r)
	if !ok {
		slog.Warn("storage download called without auth claims")
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	bucket, err := bucketForRequest(r)
	if err != nil {
		slog.Warn("storage download missing project slug", "error", err)
		http.Error(w, `{"error":"missing X-Project-Slug header"}`, http.StatusBadRequest)
		return
	}

	key := extractWildcardKey(r)
	if key == "" {
		http.Error(w, `{"error":"object key is required"}`, http.StatusBadRequest)
		return
	}

	// Ownership check: RLS filters storage_objects so an end-user only
	// sees their own rows. If this returns false, either the object
	// doesn't exist or it belongs to someone else — either way, 404.
	visible, err := h.assertObjectVisible(r, key)
	if err != nil {
		slog.Error("storage download: ownership check failed", "error", err, "key", key)
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	if !visible {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}

	body, contentType, size, err := h.s3.DownloadObject(r.Context(), bucket, key)
	if err != nil {
		if strings.Contains(err.Error(), "object not found") {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		slog.Error("storage download failed", "error", err, "bucket", bucket, "key", key)
		http.Error(w, `{"error":"failed to download file"}`, http.StatusInternalServerError)
		return
	}
	defer body.Close()

	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}

	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, body); err != nil {
		slog.Error("storage download: error streaming response", "error", err, "bucket", bucket, "key", key)
	}
}

// ---------- Delete ----------

// DeleteFile handles DELETE /v1/storage/{key...}.
func (h *StorageHandler) DeleteFile(w http.ResponseWriter, r *http.Request) {
	_, ok := isAuthenticated(r)
	if !ok {
		slog.Warn("storage delete called without auth claims")
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	bucket, err := bucketForRequest(r)
	if err != nil {
		slog.Warn("storage delete missing project slug", "error", err)
		http.Error(w, `{"error":"missing X-Project-Slug header"}`, http.StatusBadRequest)
		return
	}

	key := extractWildcardKey(r)
	if key == "" {
		http.Error(w, `{"error":"object key is required"}`, http.StatusBadRequest)
		return
	}

	// Ownership check: same RLS-based filter as DownloadFile. Stops one
	// end-user from deleting another's file by guessing the key.
	visible, err := h.assertObjectVisible(r, key)
	if err != nil {
		slog.Error("storage delete: ownership check failed", "error", err, "key", key)
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	if !visible {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}

	if err := h.s3.DeleteObject(r.Context(), bucket, key); err != nil {
		slog.Error("storage delete failed", "error", err, "bucket", bucket, "key", key)
		http.Error(w, `{"error":"failed to delete file"}`, http.StatusInternalServerError)
		return
	}

	// Remove the tracking row from storage_objects.
	if schema := h.schemaForRequest(r); schema != "" && h.pool != nil {
		escSchema := strings.ReplaceAll(schema, `"`, `""`)
		q := fmt.Sprintf(`DELETE FROM "%s".storage_objects WHERE key = $1`, escSchema)
		if err := edb.RunAsService(r.Context(), h.pool, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, q, key)
			return err
		}); err != nil {
			slog.Error("storage: failed to delete from storage_objects",
				"error", err, "schema", schema, "key", key)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---------- List ----------

// listResponse is the JSON envelope for the list endpoint.
type listResponse struct {
	Objects    []ObjectInfo `json:"objects"`
	NextCursor string       `json:"next_cursor,omitempty"`
	HasMore    bool         `json:"has_more"`
}

// ListFiles handles GET /v1/storage?prefix=...&limit=...&cursor=...
func (h *StorageHandler) ListFiles(w http.ResponseWriter, r *http.Request) {
	_, ok := isAuthenticated(r)
	if !ok {
		slog.Warn("storage list called without auth claims")
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	bucket, err := bucketForRequest(r)
	if err != nil {
		slog.Warn("storage list missing project slug", "error", err)
		http.Error(w, `{"error":"missing X-Project-Slug header"}`, http.StatusBadRequest)
		return
	}

	prefix := r.URL.Query().Get("prefix")
	cursor := r.URL.Query().Get("cursor")

	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if parsed, parseErr := strconv.Atoi(v); parseErr == nil && parsed > 0 && parsed <= 1000 {
			limit = parsed
		}
	}

	result, err := h.s3.ListObjects(r.Context(), bucket, prefix, limit, cursor)
	if err != nil {
		slog.Error("storage list failed", "error", err, "bucket", bucket, "prefix", prefix)
		http.Error(w, `{"error":"failed to list files"}`, http.StatusInternalServerError)
		return
	}

	resp := listResponse{
		Objects:    result.Objects,
		NextCursor: result.NextToken,
		HasMore:    result.IsTruncated,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// ---------- Signed URL ----------

// signedURLRequest is the JSON body for generating a pre-signed URL.
type signedURLRequest struct {
	Key         string `json:"key"`
	Operation   string `json:"operation"`    // "upload" or "download"
	ContentType string `json:"content_type"` // required for upload
	ExpiresIn   int    `json:"expires_in"`   // seconds; 0 means default
}

// signedURLResponse is the JSON response with the generated URL.
type signedURLResponse struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// GenerateSignedURL handles POST /v1/storage/signed-url.
func (h *StorageHandler) GenerateSignedURL(w http.ResponseWriter, r *http.Request) {
	_, ok := isAuthenticated(r)
	if !ok {
		slog.Warn("storage signed-url called without auth claims")
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	bucket, err := bucketForRequest(r)
	if err != nil {
		slog.Warn("storage signed-url missing project slug", "error", err)
		http.Error(w, `{"error":"missing X-Project-Slug header"}`, http.StatusBadRequest)
		return
	}

	var req signedURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Warn("storage signed-url: invalid request body", "error", err)
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	if req.Key == "" {
		http.Error(w, `{"error":"key is required"}`, http.StatusBadRequest)
		return
	}
	if req.Operation != "upload" && req.Operation != "download" {
		http.Error(w, `{"error":"operation must be upload or download"}`, http.StatusBadRequest)
		return
	}

	// Ownership check for download signed URLs. Upload URLs are for files
	// the caller is about to create — no existing row to check; the upload
	// tracking INSERT still records uploaded_by so subsequent downloads are
	// gated correctly. A signed URL handed to a different user after it's
	// generated is a trust-the-URL scenario (unguessable token); that's
	// acceptable per the design of signed URLs.
	if req.Operation == "download" {
		visible, err := h.assertObjectVisible(r, req.Key)
		if err != nil {
			slog.Error("storage signed-url: ownership check failed", "error", err, "key", req.Key)
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		if !visible {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
	}

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
			expiry = 15 * time.Minute // default for upload
		}
		url, err = h.s3.GeneratePresignedUploadURL(r.Context(), bucket, req.Key, req.ContentType, expiry)

	case "download":
		if req.ExpiresIn > 0 {
			expiry = time.Duration(req.ExpiresIn) * time.Second
		} else {
			expiry = 1 * time.Hour // default for download
		}
		url, err = h.s3.GeneratePresignedDownloadURL(r.Context(), bucket, req.Key, expiry)
	}

	if err != nil {
		slog.Error("storage signed-url generation failed", "error", err, "bucket", bucket, "key", req.Key)
		http.Error(w, `{"error":"failed to generate signed URL"}`, http.StatusInternalServerError)
		return
	}

	resp := signedURLResponse{
		URL:       url,
		ExpiresAt: time.Now().Add(expiry),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// ---------- Helpers ----------

// extractWildcardKey extracts the object key from chi's wildcard route param.
// The key is everything after /v1/storage/ and may contain slashes.
func extractWildcardKey(r *http.Request) string {
	key := chi.URLParam(r, "*")
	key = strings.TrimPrefix(key, "/")
	return key
}

// schemaForRequest resolves the tenant schema name from the authenticated
// ProjectContext set by upstream middleware (API key middleware for SDK
// routes, PlatformStorageContext for console routes). The schema is NEVER
// derived from client-supplied headers — that would let a caller spoof which
// tenant's tracking rows are written.
func (h *StorageHandler) schemaForRequest(r *http.Request) string {
	if pc, ok := auth.ProjectFromContext(r.Context()); ok {
		return pc.SchemaName
	}
	return ""
}
