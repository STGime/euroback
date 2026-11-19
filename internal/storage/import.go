package storage

// Importing files from another service (the CLI's `import supabase
// storage`). Console routes, developer role:
//
//   POST …/storage/import           — for a batch of files: record each
//                                     one with its owner and hand out an
//                                     upload URL, or say it is already
//                                     there
//   POST …/storage/import/complete  — after the uploads: record each
//                                     file's real size, read from the
//                                     bucket
//
// The bytes never pass through the gateway: the CLI streams each file
// from the source straight to its upload URL. What the gateway adds is
// the record a file needs to be usable here (downloads, deletes and
// listings check storage_objects) and its owner — the end user who
// uploaded it on the other service, who keeps the access they had.
//
// Ownership rules, so an import can't take a file away from anyone:
//   - a new key is recorded with the requested owner (an end user of this
//     project, or none for a developer file);
//   - a key that is already recorded keeps its owner and its bytes. It
//     is "exists" when it is in the bucket with the same owner (and the
//     same size, when the caller knows the size). Only a file an earlier
//     import recorded and whose upload never arrived — not in the
//     bucket, size still 0 — gets its upload URL again. Everything else
//     is a "conflict": a file changed or replaced since the import is
//     never overwritten by running the import again;
//   - a file in the bucket without a record is not overwritten.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/query"
	"github.com/jackc/pgx/v5"
)

const (
	// importBatchMax bounds one request.
	importBatchMax = 100
	// importMaxObjectBytes is the most one upload URL can take (a single
	// S3 PUT). Larger files need a multipart upload.
	importMaxObjectBytes = 5 << 30
	// importURLExpiry leaves time for a large file on a slow line.
	importURLExpiry = time.Hour
)

var importUUIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ImportObject is one file to import.
type ImportObject struct {
	Key string `json:"key"`
	// Size in bytes, or -1 when the source doesn't know it.
	Size        int64  `json:"size"`
	ContentType string `json:"content_type,omitempty"`
	// Owner is the end user the file belongs to; empty for a developer
	// file.
	Owner string `json:"owner,omitempty"`
}

// ImportResult is the answer for one file.
type ImportResult struct {
	Key string `json:"key"`
	// Status: "upload" (PUT the bytes to URL with Headers), "exists"
	// (already imported: same owner, same size), "conflict" (the key is
	// taken by another file) or "error".
	Status  string            `json:"status"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// Code names the error for the caller: invalid_key, too_large,
	// unknown_owner, export_namespace, shared_folder.
	Code  string `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
}

type importRequest struct {
	Objects []ImportObject `json:"objects"`
}

// ImportObjects handles POST …/storage/import.
func (h *StorageHandler) ImportObjects(w http.ResponseWriter, r *http.Request) {
	if !serviceCaller(r) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	bucket, err := bucketForRequest(r)
	if err != nil {
		http.Error(w, `{"error":"project context missing"}`, http.StatusBadRequest)
		return
	}
	schema := h.schemaForRequest(r)
	if schema == "" {
		http.Error(w, `{"error":"project context missing"}`, http.StatusBadRequest)
		return
	}
	var req importRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if len(req.Objects) == 0 || len(req.Objects) > importBatchMax {
		http.Error(w, fmt.Sprintf(`{"error":"send 1 to %d objects per request"}`, importBatchMax), http.StatusBadRequest)
		return
	}
	results := make([]ImportResult, len(req.Objects))
	for i, obj := range req.Objects {
		res, err := h.importOne(r, bucket, schema, obj)
		if err != nil {
			if errors.Is(err, query.ErrDedicatedPoolUnavailable) {
				writeTenantPoolError(w, err)
				return
			}
			slog.Error("storage import failed", "error", err, "key", obj.Key)
			res = ImportResult{Key: obj.Key, Status: "error", Code: "internal", Error: "internal error"}
		}
		results[i] = res
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (h *StorageHandler) importOne(r *http.Request, bucket, schema string, obj ImportObject) (ImportResult, error) {
	key := NormalizeStorageKey(obj.Key)
	fail := func(code, msg string) (ImportResult, error) {
		return ImportResult{Key: obj.Key, Status: "error", Code: code, Error: msg}, nil
	}
	if err := ValidateStorageKey(key); err != nil {
		return fail("invalid_key", err.Error())
	}
	if isExportKey(r, key) {
		return fail("export_namespace", "this folder holds the project's data exports")
	}
	if obj.Size < -1 {
		return fail("invalid_size", "size must be -1 (unknown) or more")
	}
	if obj.Size > importMaxObjectBytes {
		return fail("too_large", fmt.Sprintf("files larger than %d GB can't be imported yet", importMaxObjectBytes>>30))
	}
	var owner any
	if obj.Owner != "" {
		if !importUUIDRe.MatchString(obj.Owner) {
			return fail("unknown_owner", "owner must be a user id")
		}
		owner = strings.ToLower(obj.Owner)
	}
	ct := obj.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	esc := strings.ReplaceAll(schema, `"`, `""`)

	var (
		recorded      bool
		imported      bool
		recordedOwner *string
		recordedSize  *int64
		shared        bool
		userExists    = true
	)
	if err := h.runAsService(r.Context(), func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, fmt.Sprintf(
			`SELECT uploaded_by::text, size_bytes, coalesce(metadata->>'imported' = 'true', false)
			   FROM "%s".storage_objects WHERE key = $1`, esc), key).
			Scan(&recordedOwner, &recordedSize, &imported)
		switch {
		case err == nil:
			recorded = true
		case errors.Is(err, pgx.ErrNoRows):
		default:
			return err
		}
		if err := tx.QueryRow(ctx, fmt.Sprintf(
			`SELECT EXISTS(SELECT 1 FROM "%s".storage_shared_prefixes WHERE starts_with($1, prefix))`, esc), key).
			Scan(&shared); err != nil {
			return err
		}
		if owner != nil {
			return tx.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM "%s".users WHERE id = $1)`, esc), owner).
				Scan(&userExists)
		}
		return nil
	}); err != nil {
		return ImportResult{}, err
	}
	if !userExists {
		return fail("unknown_owner", "the owner is not a user of this project — import the users first, or import the file as a developer file")
	}
	// A shared folder holds developer files only (#697): an end user's
	// file there would be served to everyone it is shared with.
	if shared && owner != nil {
		return fail("shared_folder", "this folder is shared; it can hold developer files only")
	}

	sameOwner := func() bool {
		if owner == nil {
			return recordedOwner == nil
		}
		return recordedOwner != nil && strings.EqualFold(*recordedOwner, owner.(string))
	}
	if recorded {
		if !sameOwner() {
			return ImportResult{Key: obj.Key, Status: "conflict", Error: "a file with this key is already in the project, with another owner"}, nil
		}
		inS3, err := h.s3.ObjectExists(r.Context(), bucket, key)
		if err != nil {
			// Not "absent": an unanswered check never leads to an upload.
			return ImportResult{}, err
		}
		if inS3 {
			meta, err := h.s3.HeadObjectInfo(r.Context(), bucket, key)
			if err != nil || meta == nil {
				return ImportResult{}, fmt.Errorf("head object: %w", err)
			}
			if obj.Size >= 0 && meta.Size != obj.Size {
				return ImportResult{Key: obj.Key, Status: "conflict", Error: "the project's file with this key has another size (changed or replaced since it was imported); left as it is"}, nil
			}
			if recordedSize == nil || *recordedSize != meta.Size {
				h.recordImportedSize(r, esc, key, meta.Size)
			}
			return ImportResult{Key: obj.Key, Status: "exists"}, nil
		}
		// Recorded but not in the bucket. Only an import's own record
		// whose upload never arrived is picked up again.
		if !imported || (recordedSize != nil && *recordedSize != 0) {
			return ImportResult{Key: obj.Key, Status: "conflict", Error: "a different file with this key is recorded in the project"}, nil
		}
	} else {
		if inS3, err := h.s3.ObjectExists(r.Context(), bucket, key); err != nil {
			return ImportResult{}, err
		} else if inS3 {
			return ImportResult{Key: obj.Key, Status: "conflict", Error: "a file with this key is already in the project's bucket"}, nil
		}
		var inserted bool
		if err := h.runAsService(r.Context(), func(ctx context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, fmt.Sprintf(
				`INSERT INTO "%s".storage_objects (key, content_type, size_bytes, uploaded_by, metadata)
				 VALUES ($1, $2, 0, $3, '{"imported": true}') ON CONFLICT (key) DO NOTHING`, esc),
				key, ct, owner)
			inserted = err == nil && tag.RowsAffected() == 1
			return err
		}); err != nil {
			if isForeignKeyViolation(err) {
				return fail("unknown_owner", "the owner is not a user of this project")
			}
			return ImportResult{}, err
		}
		if !inserted {
			// Someone recorded it between the read and the insert.
			return ImportResult{Key: obj.Key, Status: "conflict", Error: "a file with this key was just added to the project"}, nil
		}
	}

	p, err := h.s3.GeneratePresignedUploadURLWithRetention(r.Context(), bucket, key, ct, importURLExpiry, h.retentionFor(r, key))
	if err != nil {
		return ImportResult{}, err
	}
	return ImportResult{Key: obj.Key, Status: "upload", URL: p.URL, Headers: p.SignedHeaders}, nil
}

func (h *StorageHandler) recordImportedSize(r *http.Request, esc, key string, size int64) {
	if err := h.runAsService(r.Context(), func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE "%s".storage_objects SET size_bytes = $2 WHERE key = $1`, esc), key, size)
		return err
	}); err != nil {
		slog.Warn("storage import: size not recorded", "error", err, "key", key)
	}
}

type completeRequest struct {
	Keys []string `json:"keys"`
}

// CompleteResult is the answer for one uploaded file.
type CompleteResult struct {
	Key string `json:"key"`
	// Status: "done" (in the bucket; size recorded) or "missing".
	Status string `json:"status"`
	Size   int64  `json:"size,omitempty"`
}

// CompleteImport handles POST …/storage/import/complete: it records the
// size of each uploaded file as the bucket reports it. Only files the
// import recorded are touched.
func (h *StorageHandler) CompleteImport(w http.ResponseWriter, r *http.Request) {
	if !serviceCaller(r) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	bucket, err := bucketForRequest(r)
	if err != nil {
		http.Error(w, `{"error":"project context missing"}`, http.StatusBadRequest)
		return
	}
	schema := h.schemaForRequest(r)
	if schema == "" {
		http.Error(w, `{"error":"project context missing"}`, http.StatusBadRequest)
		return
	}
	var req completeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if len(req.Keys) == 0 || len(req.Keys) > importBatchMax {
		http.Error(w, fmt.Sprintf(`{"error":"send 1 to %d keys per request"}`, importBatchMax), http.StatusBadRequest)
		return
	}
	esc := strings.ReplaceAll(schema, `"`, `""`)
	results := make([]CompleteResult, len(req.Keys))
	for i, raw := range req.Keys {
		key := NormalizeStorageKey(raw)
		results[i] = CompleteResult{Key: raw, Status: "missing"}
		if ValidateStorageKey(key) != nil {
			continue
		}
		meta, err := h.s3.HeadObjectInfo(r.Context(), bucket, key)
		if err != nil || meta == nil {
			continue
		}
		var updated bool
		if err := h.runAsService(r.Context(), func(ctx context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, fmt.Sprintf(
				`UPDATE "%s".storage_objects SET size_bytes = $2 WHERE key = $1 AND metadata->>'imported' = 'true'`, esc),
				key, meta.Size)
			updated = err == nil && tag.RowsAffected() == 1
			return err
		}); err != nil {
			if errors.Is(err, query.ErrDedicatedPoolUnavailable) {
				writeTenantPoolError(w, err)
				return
			}
			slog.Error("storage import complete failed", "error", err, "key", key)
			continue
		}
		if updated {
			results[i] = CompleteResult{Key: raw, Status: "done", Size: meta.Size}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}
