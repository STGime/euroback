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
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/auth"
	edb "github.com/eurobase/euroback/internal/db"
	"github.com/eurobase/euroback/internal/query"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Console "Test" for an edge function (#712): the developer invokes a
// deployed function from the console. It goes through HandleInvoke — the
// same runner, HMAC, limits, plan timeouts and Team routing as public
// traffic — with a synthetic request built from the payload below, and
// returns the response plus this invocation's log lines.

// TestInvokeRequest is the console's test payload.
type TestInvokeRequest struct {
	Method  string            `json:"method"`  // default POST
	Query   string            `json:"query"`   // without '?'; forwarded like a public call's
	Headers map[string]string `json:"headers"` // forwarded like a public call's (gateway control headers are dropped by HandleInvoke)
	Body    string            `json:"body"`
	// UserID runs the function as that end user of the project (ctx.user,
	// RLS as the user). Empty = no user (service context).
	UserID string `json:"user_id"`
}

// TestInvokeResponse is what the console shows.
type TestInvokeResponse struct {
	Status        int               `json:"status"`
	Headers       map[string]string `json:"headers"`
	Body          string            `json:"body"`
	BodyTruncated bool              `json:"body_truncated,omitempty"`
	DurationMs    int64             `json:"duration_ms"`
	Logs          json.RawMessage   `json:"logs,omitempty"`
}

const (
	testMaxRequestBody  = 1 << 20 // like the console editors; public calls allow more
	testMaxResponseBody = 1 << 20
)

// testInFlight allows one console test per project at a time (per
// gateway replica — the gateway runs one).
var testInFlight sync.Map

// headerNameRe is an RFC 7230 token.
var headerNameRe = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

// HandleTestInvoke — POST /platform/projects/{id}/functions/{name}/test.
// Mounted behind PlatformTenantContext (project context; a Team project's
// dedicated pool for the end-user lookup) and RequireMinRole("developer").
func HandleTestInvoke(pool *pgxpool.Pool, svc *Service, runnerURL string, signer *Signer) http.HandlerFunc {
	invoke := HandleInvoke(pool, svc, runnerURL, signer)
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		name := chi.URLParam(r, "name")
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok || pc == nil || pc.ProjectID != projectID {
			jsonError(w, "missing project context", http.StatusInternalServerError)
			return
		}
		if _, busy := testInFlight.LoadOrStore(projectID, struct{}{}); busy {
			jsonError(w, "a test run for this project is already in progress", http.StatusTooManyRequests)
			return
		}
		defer testInFlight.Delete(projectID)

		var req TestInvokeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, testMaxRequestBody+64<<10)).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		method := strings.ToUpper(strings.TrimSpace(req.Method))
		if method == "" {
			method = http.MethodPost
		}
		switch method {
		case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		default:
			jsonError(w, "method must be GET, POST, PUT, PATCH or DELETE", http.StatusBadRequest)
			return
		}
		if len(req.Body) > testMaxRequestBody {
			jsonError(w, "request body too large (max 1 MB)", http.StatusRequestEntityTooLarge)
			return
		}
		for k, v := range req.Headers {
			if !headerNameRe.MatchString(k) || strings.ContainsAny(v, "\r\n\x00") {
				jsonError(w, fmt.Sprintf("invalid header %q", k), http.StatusBadRequest)
				return
			}
		}
		if req.UserID != "" {
			if _, err := uuid.Parse(req.UserID); err != nil {
				jsonError(w, "user_id must be a user id (uuid)", http.StatusBadRequest)
				return
			}
		}
		// Like the public chain's MaintenanceModeMiddleware: no test runs
		// while the project's data is being moved (upgrade cutover).
		var inMaintenance bool
		if err := pool.QueryRow(r.Context(), `SELECT maintenance_mode FROM public.projects WHERE id = $1`, projectID).Scan(&inMaintenance); err != nil {
			slog.Error("function test: maintenance check", "error", err, "project_id", projectID)
			jsonError(w, "could not start the test run", http.StatusServiceUnavailable)
			return
		}
		if inMaintenance {
			jsonError(w, "the project is in maintenance; try again shortly", http.StatusServiceUnavailable)
			return
		}

		ctx := r.Context()
		if req.UserID != "" {
			email, err := lookupEndUser(ctx, pool, pc.SchemaName, req.UserID)
			if errors.Is(err, pgx.ErrNoRows) {
				jsonError(w, "no such user in this project", http.StatusBadRequest)
				return
			}
			if err != nil {
				slog.Error("function test: end-user lookup", "error", err, "project_id", projectID)
				jsonError(w, "could not look up the user", http.StatusServiceUnavailable)
				return
			}
			ctx = auth.ContextWithEndUserClaims(ctx, &auth.EndUserClaims{UserID: req.UserID, Email: email, ProjectID: projectID})
		}
		// Audited before running — and not tied to the request, so a closed
		// tab can't leave an impersonated run unrecorded.
		if a := audit.FromContext(r.Context()); a != nil {
			aid, aemail := audit.ActorFromContext(r.Context())
			meta := map[string]any{"function": name, "method": method}
			if req.UserID != "" {
				meta["as_user"] = req.UserID
			}
			a.Log(context.WithoutCancel(r.Context()), projectID, aid, aemail, "function.test_invoked",
				audit.WithTarget("edge_function", name), audit.WithMetadata(meta))
		}

		var logs []byte
		ctx = context.WithValue(ctx, logSinkKey{}, func(b []byte) { logs = b })
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("name", name)
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)

		target := "/v1/functions/" + url.PathEscape(name)
		if q := strings.TrimPrefix(req.Query, "?"); q != "" {
			target += "?" + q
		}
		inner, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(req.Body))
		if err != nil {
			jsonError(w, "invalid method, path or query", http.StatusBadRequest)
			return
		}
		for k, v := range req.Headers {
			inner.Header.Set(k, v)
		}
		if inner.Header.Get("Content-Type") == "" && req.Body != "" {
			inner.Header.Set("Content-Type", "application/json")
		}

		rec := httptest.NewRecorder()
		start := time.Now()
		invoke(rec, inner)
		elapsed := time.Since(start)

		body, _ := io.ReadAll(io.LimitReader(rec.Result().Body, testMaxResponseBody+1))
		res := TestInvokeResponse{
			Status:     rec.Code,
			Headers:    map[string]string{},
			DurationMs: elapsed.Milliseconds(),
		}
		if len(body) > testMaxResponseBody {
			body, res.BodyTruncated = body[:testMaxResponseBody], true
		}
		res.Body = string(bytes.ToValidUTF8(body, []byte("�")))
		for k, v := range rec.Header() {
			res.Headers[k] = strings.Join(v, ", ")
		}
		if json.Valid(logs) {
			res.Logs = logs
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(res)
	}
}

// lookupEndUser returns the email of the project's end user id — from the
// request's tenant pool (a Team project's dedicated database) or the shared
// cluster.
func lookupEndUser(ctx context.Context, shared *pgxpool.Pool, schema, userID string) (string, error) {
	p := query.TenantPoolFromContext(ctx)
	if p == nil {
		if pc, ok := auth.ProjectFromContext(ctx); ok && pc != nil && pc.HasDedicatedDB {
			return "", fmt.Errorf("the project's dedicated database is not available")
		}
		p = shared
	}
	var email string
	// email is nullable (phone-only users, migrations 000032/000040).
	q := fmt.Sprintf(`SELECT coalesce(email, '') FROM "%s".users WHERE id = $1::uuid`, strings.ReplaceAll(schema, `"`, `""`))
	err := edb.RunAsService(ctx, p, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, userID).Scan(&email)
	})
	return email, err
}
