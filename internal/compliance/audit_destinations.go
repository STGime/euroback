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
	"net"
	"net/http"
	"net/url"
	"strconv"
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

// ErrValidation wraps user-actionable input errors so the HTTP layer
// can safely surface the message as a 400 body. Everything else
// (DB failures, UNIQUE collisions, connection drops) falls through
// as a generic 500 with the details in slog — a bare error
// pass-through would leak constraint names / connection strings /
// query fragments into a client-visible JSON.
type ErrValidation struct{ err error }

func (e *ErrValidation) Error() string { return e.err.Error() }
func (e *ErrValidation) Unwrap() error { return e.err }

// invalid is the small constructor. Kept short so validators read
// naturally: `return invalid("kind must be webhook or syslog")`.
func invalid(format string, a ...any) error {
	return &ErrValidation{err: fmt.Errorf(format, a...)}
}

// ── validation helpers ─────────────────────────────────────────────

func validateKind(k DestinationKind) error {
	switch k {
	case DestinationWebhook, DestinationSyslog:
		return nil
	default:
		return invalid("kind must be webhook or syslog, got %q", k)
	}
}

func validateFormat(f DestinationFormat) error {
	switch f {
	case FormatJSON, FormatCEF:
		return nil
	default:
		return invalid("format must be json or cef, got %q", f)
	}
}

// validateEndpoint shape-checks per kind, and — critically —
// rejects hosts that would let a tenant turn the deliverers into
// an internal port-scanner / metadata-service proxy (SSRF-via-
// integration). This is the layer whose job that check is; the
// deliverers (#354/#355) inherit the contract we set here.
//
// Rejected literals: loopback, RFC1918, link-local (169.254.*),
// Unique Local Addresses (fc00::/7), unspecified (0.0.0.0/::), and
// the "localhost" hostname. https-only for webhook keeps audit
// traffic off plaintext.
//
// **Necessary but not sufficient**: this only defends against
// literals + `localhost`. A tenant can still register
// `attacker.com` whose DNS resolves to `10.0.0.1` at delivery time
// (DNS rebinding). The deliverers (#354 webhook, #355 syslog) MUST
// re-check the resolved IP at dial time via a custom `DialContext`
// or an egress proxy — this file only enforces the registration-
// time half. Do not remove that requirement when implementing the
// deliverers.
func validateEndpoint(kind DestinationKind, endpoint string) error {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return invalid("endpoint is required")
	}
	switch kind {
	case DestinationWebhook:
		u, err := url.Parse(endpoint)
		if err != nil {
			return invalid("endpoint is not a valid URL: %v", err)
		}
		if u.Scheme != "https" {
			return invalid("webhook endpoint must use https (plaintext http rejected — audit traffic must not travel unencrypted)")
		}
		host := u.Hostname()
		if host == "" {
			return invalid("webhook endpoint is missing host")
		}
		if err := ValidateHostNotInternal(host); err != nil {
			return err
		}
	case DestinationSyslog:
		host, port, err := splitHostPort(endpoint)
		if err != nil {
			return invalid("syslog endpoint must be host:port: %v", err)
		}
		if err := ValidateHostNotInternal(host); err != nil {
			return err
		}
		if p, err := parsePort(port); err != nil || p < 1 || p > 65535 {
			return invalid("syslog endpoint port must be 1..65535")
		}
	}
	return nil
}

// ValidateHostNotInternal rejects hosts that would let a tenant
// direct the deliverer at an internal target. Applied identically to
// both kinds — the SSRF surface is symmetric.
//
// Registration-time (called from validateEndpoint) enforces:
//   - Literal "localhost" (case-insensitively) — the common footgun.
//   - Parseable IPs: loopback, private, link-local, ULA (v6),
//     unspecified, multicast.
//
// The deliverers (#354 webhook, #355 syslog) MUST re-invoke this on
// the RESOLVED IP at dial time — a hostname can pass here and later
// resolve to 10.0.0.1 (DNS rebinding). Exported for that reuse.
//
// Extra classes the reviewer named on #356 that IsPrivate() alone
// misses, handled here so registration + dial-time apply the same
// policy:
//   - **IPv4-mapped IPv6** (::ffff:10.0.0.1): IP.To4() unpacks to
//     the 4-byte form before the IsPrivate/IsLoopback chain, so
//     IPv6-form literals get the same treatment as their v4 twins.
//   - **CGNAT 100.64.0.0/10**: not RFC1918, not in IsPrivate. A
//     tenant could point a webhook at a CGNAT-space host and reach
//     other tenants sharing the same NAT — explicit range check.
func ValidateHostNotInternal(host string) error {
	if strings.EqualFold(host, "localhost") {
		return invalid("endpoint host may not be localhost — internal targets are not allowed to prevent SSRF into cluster services")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}
	// IPv4-mapped IPv6 → normalise to 4-byte form so the classifier
	// methods below see it as 10.0.0.1 rather than ::ffff:10.0.0.1
	// (IsPrivate on the 16-byte form returns false).
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	switch {
	case ip.IsLoopback():
		return invalid("endpoint host may not be a loopback address (SSRF prevention)")
	case ip.IsUnspecified():
		return invalid("endpoint host may not be 0.0.0.0 / :: (SSRF prevention)")
	case ip.IsPrivate():
		return invalid("endpoint host may not be a private/RFC1918/ULA address (SSRF prevention)")
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
		return invalid("endpoint host may not be a link-local address (SSRF prevention — includes cloud metadata service)")
	case ip.IsInterfaceLocalMulticast() || ip.IsMulticast():
		return invalid("endpoint host may not be a multicast address")
	case isCGNAT(ip):
		return invalid("endpoint host may not be in the CGNAT range 100.64.0.0/10 (SSRF prevention)")
	}
	return nil
}

// isCGNAT reports whether ip is inside 100.64.0.0/10 (RFC 6598
// Carrier-Grade NAT space). Not covered by IP.IsPrivate; explicit
// check keeps registration and dial-time in lockstep.
func isCGNAT(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	// 100.64.0.0/10 → first octet 100, second octet in [64, 127].
	return v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
}

// splitHostPort accepts "host:port" (or "[v6]:port"). Uses
// net.SplitHostPort so trailing garbage and userinfo can't ride
// along undetected the way parse-with-fake-scheme allowed.
func splitHostPort(s string) (string, string, error) {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "", "", err
	}
	if host == "" {
		return "", "", errors.New("host is empty")
	}
	if port == "" {
		return "", "", errors.New("port is empty")
	}
	return host, port, nil
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	return n, nil
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
			var v *ErrValidation
			if errors.As(err, &v) {
				httpError(w, http.StatusBadRequest, v.Error())
				return
			}
			// Real infrastructure error — don't leak SQL / connection
			// details through the response. slog carries the detail.
			slog.Error("create audit export destination failed", "error", err, "project_id", projectID)
			httpError(w, http.StatusInternalServerError, "create failed")
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
			var v *ErrValidation
			if errors.As(err, &v) {
				httpError(w, http.StatusBadRequest, v.Error())
				return
			}
			slog.Error("update audit export destination failed", "error", err, "project_id", projectID)
			httpError(w, http.StatusInternalServerError, "update failed")
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

// TestDeliverer is the export-side surface the test handler needs.
// Kept as an interface (not a *export.Deliverer) so this file stays
// free of an audit/export → compliance import cycle (compliance
// imports audit for action codes; audit/export is fine as a leaf).
type TestDeliverer interface {
	PostEnvelope(ctx context.Context, endpoint string, secret []byte, body any) (statusCode int, err error)
}

// HandleTestDestination — POST /platform/projects/{id}/compliance/audit-export/{destID}/test.
//
// #354 wires the webhook path to actually deliver a synthetic
// audit_export.test event; syslog stays 501 until #355 ships. The
// 501 response keeps the 'deliverer_not_available' + tracking-issue
// shape the console already understands, so the UI's transient note
// still works.
func HandleTestDestination(pool *pgxpool.Pool, limits *plans.LimitsService, svc *DestinationService, webhook TestDeliverer, vaultLookup func(ctx context.Context, schemaName, name string) (string, error)) http.HandlerFunc {
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

		// Syslog deliverer lands in #355 — until then, keep the
		// 501+code:deliverer_not_available shape so the console UI
		// stays consistent.
		if d.Kind != DestinationWebhook || webhook == nil {
			writeJSON(w, http.StatusNotImplemented, map[string]any{
				"error":          "test delivery is not yet implemented",
				"code":           "deliverer_not_available",
				"kind":           string(d.Kind),
				"tracking_issue": destinationTrackingIssue(d.Kind),
			})
			return
		}

		// Resolve schema for the vault lookup.
		var schemaName string
		if err := pool.QueryRow(r.Context(),
			`SELECT schema_name FROM public.projects WHERE id = $1::uuid`, projectID,
		).Scan(&schemaName); err != nil {
			slog.Error("test audit export destination: schema lookup failed", "error", err)
			httpError(w, http.StatusInternalServerError, "schema lookup failed")
			return
		}

		var secret []byte
		if d.SecretRef != nil && *d.SecretRef != "" && vaultLookup != nil {
			v, err := vaultLookup(r.Context(), schemaName, *d.SecretRef)
			if err != nil {
				slog.Warn("test audit export destination: secret resolve failed", "error", err)
				httpError(w, http.StatusBadRequest, fmt.Sprintf("secret %q could not be resolved: %v", *d.SecretRef, err))
				return
			}
			secret = []byte(v)
		}

		// Synthetic event. Not persisted to audit_log — this is the
		// tenant's dry-run against their sink, not a real audit
		// event. cursor=0 so the sink can tell it apart from a real
		// batch and skip cursor-advancement asserts.
		body := map[string]any{
			"events": []map[string]any{
				{
					"id":           "00000000-0000-0000-0000-000000000000",
					"project_id":   projectID,
					"actor_email":  "test@eurobase.app",
					"action":       "audit_export.test",
					"created_at":   time.Now().UTC().Format(time.RFC3339),
					"seq":          0,
					"row_hash":     "",
				},
			},
			"cursor":       0,
			"delivered_at": time.Now().UTC().Format(time.RFC3339),
			"test":         true,
		}
		status, err := webhook.PostEnvelope(r.Context(), d.Endpoint, secret, body)
		if err != nil {
			slog.Warn("test audit export destination: delivery failed", "dest_id", d.ID, "status", status, "error", err)
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error":       "sink did not accept the test event",
				"code":        "delivery_failed",
				"status_code": status,
				"detail":      err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"delivered":   true,
			"status_code": status,
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
