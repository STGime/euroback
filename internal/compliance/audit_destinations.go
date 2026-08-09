package compliance

// SIEM destinations (#353) — customer-facing per-tenant sinks that
// receive the audit_log stream. This file is the CRUD service and
// HTTP handlers; the actual delivery machinery for each kind lives
// in follow-up PRs (#354 webhook, #355 syslog). The `test` endpoint
// returns 501 until at least one deliverer ships — the console
// still gets a "Test" button that renders "coming soon" so the UX
// story stays consistent.
//
// Lives in compliance (not audit) because the handlers import auth
// for the ClaimsFromContext plumbing, and auth transitively imports
// audit — an audit→auth edge would cycle. retention_holds.go and
// storage_retention.go follow the same split for the same reason;
// the audit package stays pure DB-facing.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/plans"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DestinationKind discriminates the two supported sink types. Matches
// the DB CHECK constraint on audit_export_destinations.kind.
type DestinationKind string

const (
	DestinationWebhook DestinationKind = "webhook"
	DestinationSyslog  DestinationKind = "syslog"
)

// DestinationFormat is the wire format the deliverer renders each
// event in. json is the native shape; cef is Common Event Format for
// enterprise SIEMs (ArcSight, Splunk, Elastic).
type DestinationFormat string

const (
	FormatJSON DestinationFormat = "json"
	FormatCEF  DestinationFormat = "cef"
)

// Destination is the persisted view of an audit_export_destinations
// row. SecretRef is the vault key name (tenant-scoped by project_id);
// the deliverer looks it up on each tick.
type Destination struct {
	ID         string            `json:"id"`
	ProjectID  string            `json:"project_id"`
	Kind       DestinationKind   `json:"kind"`
	Endpoint   string            `json:"endpoint"`
	SecretRef  *string           `json:"secret_ref,omitempty"`
	Format     DestinationFormat `json:"format"`
	Enabled    bool              `json:"enabled"`
	LastCursor int64             `json:"last_cursor"`
	CreatedBy  *string           `json:"created_by,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

// DestinationService is the DB-facing side. Small on purpose — the
// interesting surface is validation + the HTTP layer below.
type DestinationService struct {
	pool *pgxpool.Pool
}

func NewDestinationService(pool *pgxpool.Pool) *DestinationService {
	return &DestinationService{pool: pool}
}

// Create inserts a new destination. Validation errors return a
// non-nil error and no row — the handler surfaces those as 400 rather
// than logging as a 500.
func (s *DestinationService) Create(ctx context.Context, projectID string, kind DestinationKind, endpoint string, secretRef *string, format DestinationFormat, enabled bool, createdBy string) (*Destination, error) {
	if err := validateKind(kind); err != nil {
		return nil, err
	}
	if err := validateEndpoint(kind, endpoint); err != nil {
		return nil, err
	}
	if format == "" {
		format = FormatJSON
	}
	if err := validateFormat(format); err != nil {
		return nil, err
	}

	var d Destination
	err := s.pool.QueryRow(ctx,
		`INSERT INTO public.audit_export_destinations
		    (project_id, kind, endpoint, secret_ref, format, enabled, created_by)
		 VALUES ($1::uuid, $2, $3, $4, $5, $6, NULLIF($7, '')::uuid)
		 RETURNING id, project_id, kind, endpoint, secret_ref, format, enabled,
		           last_cursor, created_by, created_at, updated_at`,
		projectID, string(kind), endpoint, secretRef, string(format), enabled, createdBy,
	).Scan(&d.ID, &d.ProjectID, &d.Kind, &d.Endpoint, &d.SecretRef, &d.Format,
		&d.Enabled, &d.LastCursor, &d.CreatedBy, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert audit export destination: %w", err)
	}
	return &d, nil
}

// List returns every destination for a project, enabled + disabled
// alike. Console UI needs the whole set so it can render disabled
// ones with a toggle back on.
func (s *DestinationService) List(ctx context.Context, projectID string) ([]Destination, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, project_id, kind, endpoint, secret_ref, format, enabled,
		        last_cursor, created_by, created_at, updated_at
		   FROM public.audit_export_destinations
		  WHERE project_id = $1::uuid
		  ORDER BY created_at ASC
		  LIMIT 200`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("list audit export destinations: %w", err)
	}
	defer rows.Close()
	out := make([]Destination, 0)
	for rows.Next() {
		var d Destination
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.Kind, &d.Endpoint, &d.SecretRef, &d.Format,
			&d.Enabled, &d.LastCursor, &d.CreatedBy, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan audit export destination: %w", err)
		}
		out = append(out, d)
	}
	return out, nil
}

// GetByID fetches a single destination scoped to a project. Returns
// (nil, nil) when not found (caller decides 404 vs 500).
func (s *DestinationService) GetByID(ctx context.Context, projectID, destID string) (*Destination, error) {
	var d Destination
	err := s.pool.QueryRow(ctx,
		`SELECT id, project_id, kind, endpoint, secret_ref, format, enabled,
		        last_cursor, created_by, created_at, updated_at
		   FROM public.audit_export_destinations
		  WHERE project_id = $1::uuid AND id = $2::uuid`,
		projectID, destID,
	).Scan(&d.ID, &d.ProjectID, &d.Kind, &d.Endpoint, &d.SecretRef, &d.Format,
		&d.Enabled, &d.LastCursor, &d.CreatedBy, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get audit export destination: %w", err)
	}
	return &d, nil
}

// UpdateFields is the shape PATCH accepts. Pointer fields = "leave
// as-is when nil"; concrete assignments = "set to this value." Only
// endpoint, secret_ref, format, and enabled are mutable — kind and
// last_cursor are immutable via the API (kind because it defines the
// deliverer; last_cursor because it's the deliverer's own bookkeeping
// and an operator resetting it would double-deliver history).
type UpdateFields struct {
	Endpoint  *string
	SecretRef *string
	Format    *DestinationFormat
	Enabled   *bool
}

// Update applies partial changes. Returns the updated row.
func (s *DestinationService) Update(ctx context.Context, projectID, destID string, u UpdateFields) (*Destination, error) {
	// Resolve the current row so we can validate the effective
	// (kind, endpoint) combination in one place, and so we return a
	// clean 404 when the row doesn't exist rather than a Postgres
	// "0 rows affected" surprise.
	cur, err := s.GetByID(ctx, projectID, destID)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, ErrDestinationNotFound
	}

	endpoint := cur.Endpoint
	if u.Endpoint != nil {
		endpoint = *u.Endpoint
	}
	if err := validateEndpoint(cur.Kind, endpoint); err != nil {
		return nil, err
	}
	format := cur.Format
	if u.Format != nil {
		format = *u.Format
		if err := validateFormat(format); err != nil {
			return nil, err
		}
	}
	secretRef := cur.SecretRef
	if u.SecretRef != nil {
		v := *u.SecretRef
		secretRef = &v
	}
	enabled := cur.Enabled
	if u.Enabled != nil {
		enabled = *u.Enabled
	}

	var d Destination
	err = s.pool.QueryRow(ctx,
		`UPDATE public.audit_export_destinations
		    SET endpoint   = $3,
		        secret_ref = $4,
		        format     = $5,
		        enabled    = $6,
		        updated_at = now()
		  WHERE project_id = $1::uuid AND id = $2::uuid
		 RETURNING id, project_id, kind, endpoint, secret_ref, format, enabled,
		           last_cursor, created_by, created_at, updated_at`,
		projectID, destID, endpoint, secretRef, string(format), enabled,
	).Scan(&d.ID, &d.ProjectID, &d.Kind, &d.Endpoint, &d.SecretRef, &d.Format,
		&d.Enabled, &d.LastCursor, &d.CreatedBy, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("update audit export destination: %w", err)
	}
	return &d, nil
}

// Remove deletes a destination by ID. Not-found is not an error —
// same convention as retention_holds.Revoke.
func (s *DestinationService) Remove(ctx context.Context, projectID, destID string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM public.audit_export_destinations WHERE project_id = $1::uuid AND id = $2::uuid`,
		projectID, destID,
	)
	if err != nil {
		return fmt.Errorf("remove audit export destination: %w", err)
	}
	return nil
}

// ErrDestinationNotFound lets the HTTP layer map "no such row" to 404
// without conflating it with "DB error" or "validation error."
var ErrDestinationNotFound = errors.New("audit export destination not found")

// ── validation helpers ─────────────────────────────────────────────

func validateKind(k DestinationKind) error {
	switch k {
	case DestinationWebhook, DestinationSyslog:
		return nil
	default:
		return fmt.Errorf("kind must be webhook or syslog, got %q", k)
	}
}

func validateFormat(f DestinationFormat) error {
	switch f {
	case FormatJSON, FormatCEF:
		return nil
	default:
		return fmt.Errorf("format must be json or cef, got %q", f)
	}
}

// validateEndpoint shape-checks per kind. Webhook = https URL only
// (plaintext http rejected — SIEM traffic must not travel
// unencrypted). Syslog = host:port with a numeric port.
func validateEndpoint(kind DestinationKind, endpoint string) error {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return errors.New("endpoint is required")
	}
	switch kind {
	case DestinationWebhook:
		u, err := url.Parse(endpoint)
		if err != nil {
			return fmt.Errorf("endpoint is not a valid URL: %w", err)
		}
		if u.Scheme != "https" {
			return errors.New("webhook endpoint must use https (plaintext http rejected — audit traffic must not travel unencrypted)")
		}
		if u.Host == "" {
			return errors.New("webhook endpoint is missing host")
		}
	case DestinationSyslog:
		// host:port. url.Parse doesn't like bare host:port; use the
		// SplitHostPort helper via net after prepending a scheme so
		// the parser has something to chew on.
		host, port, err := parseHostPort(endpoint)
		if err != nil {
			return fmt.Errorf("syslog endpoint must be host:port: %w", err)
		}
		if host == "" || port == "" {
			return errors.New("syslog endpoint must be host:port")
		}
	}
	return nil
}

func parseHostPort(s string) (string, string, error) {
	// Prepend a scheme so url.Parse fills Host correctly. Any scheme
	// works — we throw it away.
	u, err := url.Parse("proto://" + s)
	if err != nil {
		return "", "", err
	}
	return u.Hostname(), u.Port(), nil
}

// ── HTTP handlers ─────────────────────────────────────────────────

// createDestinationRequest is the JSON body for
// POST /platform/projects/{id}/compliance/audit-export.
type createDestinationRequest struct {
	Kind      DestinationKind   `json:"kind"`
	Endpoint  string            `json:"endpoint"`
	SecretRef *string           `json:"secret_ref,omitempty"`
	Format    DestinationFormat `json:"format,omitempty"`
	Enabled   *bool             `json:"enabled,omitempty"`
}

// updateDestinationRequest is the JSON body for PATCH.
type updateDestinationRequest struct {
	Endpoint  *string            `json:"endpoint,omitempty"`
	SecretRef *string            `json:"secret_ref,omitempty"`
	Format    *DestinationFormat `json:"format,omitempty"`
	Enabled   *bool              `json:"enabled,omitempty"`
}

// HandleCreateDestination — POST /platform/projects/{id}/compliance/audit-export.
// Legal-Team gate.
func HandleCreateDestination(pool *pgxpool.Pool, limits *plans.LimitsService, svc *DestinationService, auditSvc *audit.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if projectID == "" {
			httpError(w, http.StatusBadRequest, "project id required")
			return
		}
		if err := limits.CheckLegalTeamTier(r.Context(), projectID); err != nil {
			legalTeamGate(w, err)
			return
		}
		var req createDestinationRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, "invalid body")
			return
		}
		enabled := true
		if req.Enabled != nil {
			enabled = *req.Enabled
		}

		var actorID string
		if claims, ok := auth.ClaimsFromContext(r.Context()); ok && claims != nil {
			actorID = claims.Subject
		}

		d, err := svc.Create(r.Context(), projectID, req.Kind, req.Endpoint, req.SecretRef, req.Format, enabled, actorID)
		if err != nil {
			slog.Warn("create audit export destination failed", "error", err, "project_id", projectID)
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		if auditSvc != nil {
			auditSvc.Log(r.Context(), projectID, actorID, actorEmail(r), audit.ActionAuditExportDestinationCreated,
				audit.WithTarget("audit_export_destination", d.ID),
				audit.WithMetadata(map[string]any{"kind": string(d.Kind), "endpoint": d.Endpoint, "format": string(d.Format)}),
				audit.WithIP(r.RemoteAddr))
		}
		writeJSON(w, http.StatusCreated, d)
	}
}

// HandleListDestinations — GET /platform/projects/{id}/compliance/audit-export.
func HandleListDestinations(pool *pgxpool.Pool, limits *plans.LimitsService, svc *DestinationService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if projectID == "" {
			httpError(w, http.StatusBadRequest, "project id required")
			return
		}
		if err := limits.CheckLegalTeamTier(r.Context(), projectID); err != nil {
			legalTeamGate(w, err)
			return
		}
		items, err := svc.List(r.Context(), projectID)
		if err != nil {
			slog.Error("list audit export destinations failed", "error", err, "project_id", projectID)
			httpError(w, http.StatusInternalServerError, "list failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"destinations": items, "total": len(items)})
	}
}

// HandleUpdateDestination — PATCH /platform/projects/{id}/compliance/audit-export/{destID}.
func HandleUpdateDestination(pool *pgxpool.Pool, limits *plans.LimitsService, svc *DestinationService, auditSvc *audit.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		destID := chi.URLParam(r, "destID")
		if projectID == "" || destID == "" {
			httpError(w, http.StatusBadRequest, "project id and destination id required")
			return
		}
		if err := limits.CheckLegalTeamTier(r.Context(), projectID); err != nil {
			legalTeamGate(w, err)
			return
		}
		var req updateDestinationRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, "invalid body")
			return
		}
		d, err := svc.Update(r.Context(), projectID, destID, UpdateFields{
			Endpoint:  req.Endpoint,
			SecretRef: req.SecretRef,
			Format:    req.Format,
			Enabled:   req.Enabled,
		})
		if err != nil {
			if errors.Is(err, ErrDestinationNotFound) {
				httpError(w, http.StatusNotFound, "not found")
				return
			}
			slog.Warn("update audit export destination failed", "error", err, "project_id", projectID)
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}

		var actorID string
		if claims, ok := auth.ClaimsFromContext(r.Context()); ok && claims != nil {
			actorID = claims.Subject
		}
		if auditSvc != nil {
			auditSvc.Log(r.Context(), projectID, actorID, actorEmail(r), audit.ActionAuditExportDestinationUpdated,
				audit.WithTarget("audit_export_destination", d.ID),
				audit.WithMetadata(map[string]any{"endpoint": d.Endpoint, "format": string(d.Format), "enabled": d.Enabled}),
				audit.WithIP(r.RemoteAddr))
		}
		writeJSON(w, http.StatusOK, d)
	}
}

// HandleRemoveDestination — DELETE /platform/projects/{id}/compliance/audit-export/{destID}.
func HandleRemoveDestination(pool *pgxpool.Pool, limits *plans.LimitsService, svc *DestinationService, auditSvc *audit.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		destID := chi.URLParam(r, "destID")
		if projectID == "" || destID == "" {
			httpError(w, http.StatusBadRequest, "project id and destination id required")
			return
		}
		if err := limits.CheckLegalTeamTier(r.Context(), projectID); err != nil {
			legalTeamGate(w, err)
			return
		}
		if err := svc.Remove(r.Context(), projectID, destID); err != nil {
			slog.Error("remove audit export destination failed", "error", err, "project_id", projectID, "dest_id", destID)
			httpError(w, http.StatusInternalServerError, "remove failed")
			return
		}

		var actorID string
		if claims, ok := auth.ClaimsFromContext(r.Context()); ok && claims != nil {
			actorID = claims.Subject
		}
		if auditSvc != nil {
			auditSvc.Log(r.Context(), projectID, actorID, actorEmail(r), audit.ActionAuditExportDestinationRemoved,
				audit.WithTarget("audit_export_destination", destID),
				audit.WithIP(r.RemoteAddr))
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleTestDestination — POST /platform/projects/{id}/compliance/audit-export/{destID}/test.
//
// The CRUD ships in #353 but the deliverers (webhook #354, syslog
// #355) are follow-ups. Rather than route the endpoint away entirely
// (which would give the console a "route does not exist" error and
// no clue why), return HTTP 501 Not Implemented with a body that
// names the missing dependency. The console can render a
// "Test (coming soon)" affordance without a broken-looking failure.
func HandleTestDestination(pool *pgxpool.Pool, limits *plans.LimitsService, svc *DestinationService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		destID := chi.URLParam(r, "destID")
		if projectID == "" || destID == "" {
			httpError(w, http.StatusBadRequest, "project id and destination id required")
			return
		}
		if err := limits.CheckLegalTeamTier(r.Context(), projectID); err != nil {
			legalTeamGate(w, err)
			return
		}
		// Confirm the destination exists so an operator poking a
		// random ID gets 404, not the generic 501.
		d, err := svc.GetByID(r.Context(), projectID, destID)
		if err != nil {
			slog.Error("test audit export destination: lookup failed", "error", err)
			httpError(w, http.StatusInternalServerError, "lookup failed")
			return
		}
		if d == nil {
			httpError(w, http.StatusNotFound, "not found")
			return
		}
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":   "test delivery is not yet implemented",
			"code":    "deliverer_not_available",
			"kind":    string(d.Kind),
			"tracking_issue": destinationTrackingIssue(d.Kind),
		})
	}
}

func destinationTrackingIssue(k DestinationKind) string {
	switch k {
	case DestinationWebhook:
		return "https://github.com/STGime/euroback/issues/354"
	case DestinationSyslog:
		return "https://github.com/STGime/euroback/issues/355"
	}
	return ""
}

// httpError is a local shim — the compliance package's shared
// writeJSON helper (storage_retention.go) doesn't have an error
// counterpart, and reusing it directly for error envelopes would
// tangle the two shapes. Kept small and colocated.
func httpError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// legalTeamGate emits the 402 upgrade envelope the console's
// APIError typed-check catches (compliance/+page.svelte uses the
// same code:legal_team_required contract).
func legalTeamGate(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPaymentRequired)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": err.Error(),
		"code":  "legal_team_required",
	})
}

// actorEmail plucks the caller's email off the claims chain for the
// audit-log actor_email column. Empty when we don't have it — the
// audit_log NOT NULL constraint gets a blank rather than the write
// failing, matching every other Log() call in this package.
func actorEmail(r *http.Request) string {
	if claims, ok := auth.ClaimsFromContext(r.Context()); ok && claims != nil {
		return claims.Email
	}
	return ""
}
