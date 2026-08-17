package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/auth"
	edb "github.com/eurobase/euroback/internal/db"
	"github.com/eurobase/euroback/internal/query"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)


// recordDownload emits a GDPR personal-data access-log event for a successful
// storage download. Fire-and-forget via the context recorder (nil-safe).
func recordDownload(r *http.Request, key string) {
	rec := audit.AccessRecorderFromContext(r.Context())
	if rec == nil {
		return
	}
	var projectID, endUserID, role string
	if eu, ok := auth.EndUserClaimsFromContext(r.Context()); ok && eu != nil {
		projectID, endUserID, role = eu.ProjectID, eu.UserID, "authenticated"
	} else if _, ok := auth.ClaimsFromContext(r.Context()); ok {
		role = "platform"
		if pc, ok := auth.ProjectFromContext(r.Context()); ok && pc != nil {
			projectID = pc.ProjectID
		}
	} else if pc, ok := auth.ProjectFromContext(r.Context()); ok && pc != nil {
		projectID, role = pc.ProjectID, audit.EffectiveRole(pc.KeyType, "")
	}
	rec.Record(audit.AccessEvent{
		ProjectID:   projectID,
		EndUserID:   endUserID,
		ActorRole:   role,
		Action:      audit.AccessActionDownload,
		TargetTable: "storage_objects",
		TargetKeys:  map[string]interface{}{"key": key},
		IP:          audit.ClientIPFromContext(r.Context()),
	})
}

// maxUploadSize is the gateway-enforced maximum for multipart uploads (50 MB).
const maxUploadSize = 50 << 20 // 50 MB

// RetentionResolver resolves the object-lock retention window for
// a given (project, key). Implemented by
// compliance.StorageRetentionService — kept as an interface here so
// storage doesn't import compliance (which would be a cycle) and so
// tests can stub it out.
//
// Resolve is the upload-path variant: measures retention from now.
// ResolveFromUpload is the delete-path variant: measures retention
// from the object's actual upload time so an already-expired lock
// doesn't over-block. A zero uploadedAt in ResolveFromUpload behaves
// like Resolve.
type RetentionResolver interface {
	Resolve(ctx context.Context, projectID, key string) (Retention, error)
	ResolveFromUpload(ctx context.Context, projectID, key string, uploadedAt time.Time) (Retention, error)
}

// HoldChecker reports whether a given storage object is under an
// active retention hold that predates the S3 Object Lock system.
// Implemented by compliance.HoldService.IsHeldObject — same
// no-import-cycle motivation as RetentionResolver.
//
// Two layers on purpose:
//   - S3 Object Lock (via RetentionResolver at upload time) covers
//     the *default* policy — every /invoices/* upload retained 10y.
//   - retention_holds (via HoldChecker at delete time) covers the
//     ad-hoc case: ops places a hold on a specific object mid-
//     lifetime under a legal-basis a customer only just cited.
//
// The delete path checks the hold first (cheap DB call) then hits
// S3 (which enforces the default policy). Belt-and-braces: a hold
// can protect an object that S3 wouldn't refuse, and Object Lock
// protects the default case even if a hold row is missing.
type HoldChecker interface {
	IsHeldObject(ctx context.Context, projectID, bucket, key string) (retentionUntil time.Time, held bool, err error)
}


// PoolResolver picks the pool that owns the caller's tenant schema
// (project's dedicated managed-PG instance for Team-tier; nil for
// Free/Pro so the caller falls back to shared). Same shape as
// enduser.PoolResolver / plans.PoolResolver — router.go hands each
// package the same closure over its PoolCache.
type PoolResolver func(ctx context.Context, projectID string) *pgxpool.Pool

// StorageHandler holds dependencies for the storage HTTP handlers.
type StorageHandler struct {
	s3        *S3Client
	pool      *pgxpool.Pool
	engine    *query.QueryEngine
	retention RetentionResolver
	holds     HoldChecker
	resolver  PoolResolver
}

// NewStorageHandler creates a new StorageHandler backed by the given S3Client
// and database pool (used to track uploads in storage_objects). The query
// engine is used to run RLS-aware ownership checks before S3 fetches so an
// end-user can't download another end-user's files by guessing the key.
//
// A retention resolver is optional (nil-safe) — Free/Pro/Team projects
// pass nil and uploads land without any Object Lock retention. Legal-
// Team projects pass a resolver that looks up per-prefix policies from
// public.storage_retention_policies.
func NewStorageHandler(s3 *S3Client, pool *pgxpool.Pool, engine *query.QueryEngine) *StorageHandler {
	return &StorageHandler{s3: s3, pool: pool, engine: engine}
}

// WithPoolResolver attaches a Team-tier-aware pool resolver so
// storage_objects reads / writes route to the project's dedicated
// instance instead of the shared platform DB (where the schema does
// not exist for Team-tier post-PR-A). Chainable; nil-safe (Free/Pro
// or test builds skip routing and keep the shared pool).
func (h *StorageHandler) WithPoolResolver(r PoolResolver) *StorageHandler {
	h.resolver = r
	return h
}

// tenantPool returns the dedicated pool for the caller's project when
// the resolver is wired and the project has one, else the shared
// platform pool. Prefers the ProjectContext (set by
// PlatformStorageContext for console traffic) over the query-package
// context helpers so this works for both the SDK path (ProjectContext
// set by APIKeyMiddleware) and console (set by
// PlatformStorageContext).
func (h *StorageHandler) tenantPool(ctx context.Context) *pgxpool.Pool {
	if h.resolver == nil {
		return h.pool
	}
	var projectID string
	if pc, ok := auth.ProjectFromContext(ctx); ok && pc != nil {
		projectID = pc.ProjectID
	} else {
		projectID = query.ProjectIDFromContext(ctx)
	}
	if projectID == "" {
		return h.pool
	}
	if p := h.resolver(ctx, projectID); p != nil {
		return p
	}
	return h.pool
}

// WithRetentionResolver attaches a retention resolver so upload paths
// derive WORM retention from per-prefix policies. Chainable; safe to
// omit for non-Legal-Team gateway builds.
func (h *StorageHandler) WithRetentionResolver(r RetentionResolver) *StorageHandler {
	h.retention = r
	return h
}

// WithHoldChecker attaches a hold checker so the delete path refuses
// with 409 object_locked when the object is under an active
// retention_holds row. Chainable; nil-safe for non-Legal-Team builds.
func (h *StorageHandler) WithHoldChecker(c HoldChecker) *StorageHandler {
	h.holds = c
	return h
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

// ValidateStorageKey rejects object keys that would be unsafe to round-
// trip through any future code path that joins them into a filesystem
// path or local URL. Closes #61. Today's S3 client treats keys as opaque
// blobs so traversal is bounded, but the moment any handler or signed-
// URL path-prefix logic joins these, untrusted keys become a real
// path-traversal vector.
//
// Exported because the functions package's internal storage RPC handler
// (added in #85) reuses these same rules so user code that calls
// ctx.storage.upload sees identical validation as the SDK path.
//
// Rules:
//   - non-empty
//   - ≤ 1024 chars (S3 key spec is 1024; we mirror)
//   - no leading "/" — that flips path-join semantics
//   - no ".." segment — classic traversal
//   - no NUL or control bytes (< 0x20, 0x7f)
func ValidateStorageKey(key string) error {
	if key == "" {
		return fmt.Errorf("key is required")
	}
	if len(key) > 1024 {
		return fmt.Errorf("key too long (max 1024 chars)")
	}
	if strings.HasPrefix(key, "/") {
		return fmt.Errorf("key must not start with /")
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == ".." {
			return fmt.Errorf("key must not contain .. segment")
		}
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("key must not contain control characters")
		}
	}
	return nil
}

// bucketForRequest derives the tenant's S3 bucket name from the
// authenticated ProjectContext. The bucket naming convention is
// "eurobase-{slug}".
//
// The slug is read from auth.ProjectContext only — never from a request
// header. Reading it from a header (as a previous version did) let any
// authenticated SDK caller choose another tenant's bucket by sending
// `X-Project-Slug: victim` (advisory GHSA-gvrg-vq6j-j647).
//
// Both the SDK path (apiKeyMiddleware sets ProjectContext.Slug from
// the API-key → project lookup) and the platform path
// (PlatformStorageContext sets it after the membership check) populate
// Slug server-side.
func bucketForRequest(r *http.Request) (string, error) {
	pc, ok := auth.ProjectFromContext(r.Context())
	if !ok || pc == nil || pc.Slug == "" {
		return "", fmt.Errorf("project context missing — storage requires authenticated project")
	}
	return "eurobase-" + pc.Slug, nil
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
	if err := ValidateStorageKey(key); err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
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

	retention := h.retentionFor(r, key)
	if err := h.s3.UploadObjectWithRetention(r.Context(), bucket, key, file, contentType, size, retention); err != nil {
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
		if err := edb.RunAsService(r.Context(), h.tenantPool(r.Context()), func(ctx context.Context, tx pgx.Tx) error {
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
	if err := ValidateStorageKey(key); err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
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

	// GDPR access log: a personal-data object is being downloaded.
	recordDownload(r, key)

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
	if err := ValidateStorageKey(key); err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
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

	// Retention checks BEFORE the S3 call. Two reasons this has to
	// happen pre-S3 for the policy layer too, not just the hold layer:
	//
	// 1. Object Lock at bucket-create force-enables versioning. On a
	//    versioned bucket, DeleteObject *without* VersionId writes a
	//    delete marker and returns success — Object Lock only refuses
	//    a specific locked version. So the S3-side lock protects data
	//    at rest, but a bare DeleteObject silently succeeds and hides
	//    the object from future listings even though the underlying
	//    version is retained. The app then deletes the tracking row
	//    and forgets it entirely.
	// 2. We can catch the delete before the tracking row goes, so the
	//    caller sees a clean 409 with retention_until instead of
	//    "success + object vanished from listings."
	//
	// Ordering: hold layer first (an ad-hoc hold overrides the
	// default), policy layer second (bucket-wide default).
	pc, _ := auth.ProjectFromContext(r.Context())
	projectID := ""
	if pc != nil {
		projectID = pc.ProjectID
	}

	if h.holds != nil && projectID != "" {
		until, held, herr := h.holds.IsHeldObject(r.Context(), projectID, bucket, key)
		if herr != nil {
			slog.Warn("storage delete: hold check failed", "error", herr, "key", key)
		} else if held {
			writeObjectLockedResponse(w, until)
			return
		}
	}

	if h.retention != nil && projectID != "" {
		uploadedAt := h.lookupUploadedAt(r, key) // zero if unknown
		ret, rerr := h.retention.ResolveFromUpload(r.Context(), projectID, key, uploadedAt)
		if rerr != nil {
			slog.Warn("storage delete: retention resolver failed", "error", rerr, "key", key)
		} else if ret.Mode != "" && time.Now().Before(ret.RetainUntil) {
			writeObjectLockedResponse(w, ret.RetainUntil)
			return
		}
	}

	if err := h.s3.DeleteObject(r.Context(), bucket, key); err != nil {
		// Backstop: if S3 refuses (e.g. a version-scoped delete we
		// don't issue here, or a future code path that does), still
		// translate to 409 rather than leaking as a 500.
		var locked *ErrObjectLocked
		if errors.As(err, &locked) {
			writeObjectLockedResponse(w, locked.RetainUntil)
			return
		}
		slog.Error("storage delete failed", "error", err, "bucket", bucket, "key", key)
		http.Error(w, `{"error":"failed to delete file"}`, http.StatusInternalServerError)
		return
	}

	// Remove the tracking row from storage_objects.
	if schema := h.schemaForRequest(r); schema != "" && h.pool != nil {
		escSchema := strings.ReplaceAll(schema, `"`, `""`)
		q := fmt.Sprintf(`DELETE FROM "%s".storage_objects WHERE key = $1`, escSchema)
		if err := edb.RunAsService(r.Context(), h.tenantPool(r.Context()), func(ctx context.Context, tx pgx.Tx) error {
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
//
// Headers is populated only for retention-aware upload URLs (Legal-
// Team projects, key under a per-prefix policy). The client MUST
// echo every listed header on the PUT — S3 baked their values into
// the SigV4 signature so a missing one fails with
// SignatureDoesNotMatch. Preserving the lock's non-droppable
// property while still telling the client what to send.
type signedURLResponse struct {
	URL       string            `json:"url"`
	ExpiresAt time.Time         `json:"expires_at"`
	Headers   map[string]string `json:"headers,omitempty"`
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

	if err := ValidateStorageKey(req.Key); err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
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
		url            string
		signedHeaders  map[string]string
		expiry         time.Duration
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
		retention := h.retentionFor(r, req.Key)
		p, perr := h.s3.GeneratePresignedUploadURLWithRetention(r.Context(), bucket, req.Key, req.ContentType, expiry, retention)
		if p != nil {
			url = p.URL
			signedHeaders = p.SignedHeaders
		}
		err = perr

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
		Headers:   signedHeaders,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// ---------- Helpers ----------

// extractWildcardKey extracts the object key from chi's wildcard route param.
// The key is everything after /v1/storage/ and may contain slashes.
//
// URL-decoding is conditional on `r.URL.RawPath`, mirroring chi's own
// branch at mux.go:433-434. The subtlety that a first attempt missed
// (unconditional decode) and PR #421 review caught:
//
//   * When the request URL contains a byte Go's default path-escape
//     WOULDN'T produce (comma, `+`, `;`, `:`, `@`, `$`, `!`, `*`, `'`,
//     `(`, `)`), net/url populates `RawPath` and chi routes on it —
//     so `chi.URLParam(r, "*")` comes back **still URL-encoded**. This
//     is the case that motivated the fix: a console request to delete
//     `ChatGPT Image 28. Okt. 2025, 12_13_05.png` arrives as
//     `…%2C…`, chi returns `…%2C…`, and `WHERE key = '…%2C…'` doesn't
//     match the row `storage_objects` holds under the decoded name.
//
//   * When the URL's characters all round-trip through default
//     escaping (pure non-ASCII, spaces, a literal `%`), `RawPath` is
//     empty and chi returns `URL.Path` — **already decoded**.
//     Unconditional PathUnescape would double-decode: `report_50%20pct.pdf`
//     (a real filename containing a literal `%20`) is sent as
//     `report_50%2520pct.pdf`, chi returns `report_50%20pct.pdf`
//     (single-decoded), and a second decode corrupts it to
//     `report_50 pct.pdf` — the exact 404-on-lookup bug this fix is
//     supposed to close, reintroduced in the opposite direction.
//
// So: decode only when chi routed on RawPath. Every downstream consumer
// (assertObjectVisible SQL, S3 DeleteObject, retention lookup) sees the
// canonical decoded key without any double-decode risk.
//
// Security note: decoding BEFORE ValidateStorageKey is intentional. Prior
// to this fix, validation ran on the encoded string, so a caller could
// smuggle `..` traversal via `%2E%2E%2F` past the `strings.Split(...,
// "/")` "..-segment" check. Post-decode the check catches it (RawPath is
// set for that input — `%2E` is a byte default-escape wouldn't produce).
// The ValidateStorageKey rules (no leading `/`, no `..` segment, no
// control bytes) only work on the decoded form.
//
// PathUnescape failure is treated as "leave the value as-is": chi already
// matched the route so the wildcard is a well-formed URL segment.
func extractWildcardKey(r *http.Request) string {
	key := chi.URLParam(r, "*")
	key = strings.TrimPrefix(key, "/")
	if r.URL.RawPath != "" {
		if decoded, err := url.PathUnescape(key); err == nil {
			key = decoded
		}
	}
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

// lookupUploadedAt returns the created_at from storage_objects for
// the given key, or zero if the row can't be found. The zero is a
// signal (not an error) — ResolveFromUpload treats it as "measure
// from now" (defensive over-block), so a missing tracking row for a
// currently-policied object still gets refused. Best-effort by
// design: a DB blip here shouldn't block a delete for a Free-tier
// project that has no policy anyway (the retention check itself
// only runs when h.retention != nil).
func (h *StorageHandler) lookupUploadedAt(r *http.Request, key string) time.Time {
	schema := h.schemaForRequest(r)
	if schema == "" || h.pool == nil {
		return time.Time{}
	}
	esc := strings.ReplaceAll(schema, `"`, `""`)
	q := fmt.Sprintf(`SELECT created_at FROM "%s".storage_objects WHERE key = $1`, esc)
	var t time.Time
	if err := edb.RunAsService(r.Context(), h.tenantPool(r.Context()), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, key).Scan(&t)
	}); err != nil {
		// No row / RLS filter / query error — leave zero so the
		// resolver defaults to over-block. slog is intentionally
		// debug: a missing tracking row for an untracked object
		// (uploaded pre-tracking, or via a path that doesn't
		// record) is normal, not an error.
		return time.Time{}
	}
	return t
}

// writeObjectLockedResponse writes the shared 409 JSON envelope for
// a delete refused by either the hold layer, the policy layer, or
// the S3 backstop. Kept in one place so the shape ({error, code,
// retention_until?}) stays consistent across the three call sites.
func writeObjectLockedResponse(w http.ResponseWriter, retentionUntil time.Time) {
	resp := map[string]any{
		"error": "object is under retention hold",
		"code":  "object_locked",
	}
	if !retentionUntil.IsZero() {
		resp["retention_until"] = retentionUntil.Format(time.RFC3339)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(resp)
}

// retentionFor returns the S3 Object Lock retention window that
// applies to (project, key), or a zero-value Retention if either the
// resolver is unwired (non-Legal-Team gateway build), the project has
// no matching policy, or the resolver errors. A resolver failure is
// logged but does not block the upload — the fall-open behaviour is
// intentional: a broken policy lookup shouldn't take down the write
// path for the whole tenant, and the object goes in with no lock
// rather than none-at-all being written.
func (h *StorageHandler) retentionFor(r *http.Request, key string) Retention {
	if h.retention == nil {
		return Retention{}
	}
	pc, ok := auth.ProjectFromContext(r.Context())
	if !ok || pc == nil || pc.ProjectID == "" {
		return Retention{}
	}
	ret, err := h.retention.Resolve(r.Context(), pc.ProjectID, key)
	if err != nil {
		slog.Warn("storage: retention resolver failed, uploading without lock",
			"error", err, "project_id", pc.ProjectID, "key", key)
		return Retention{}
	}
	return ret
}
