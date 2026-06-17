// Package breach implements the personal-data breach register required by
// GDPR Art. 33(5) and operationalises the 72h supervisory-authority and 24h
// customer notification SLAs the DPA commits to.
//
// Storage is public.breach_register (migration 000065). Append-only by the
// same pattern as audit_log (000058): every state change writes a NEW row
// keyed by the same incident_id. The most recent row by `seq` is the
// authoritative snapshot.
package breach

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Status values mirror the runbook state machine.
const (
	StatusOpen              = "open"
	StatusContained         = "contained"
	StatusNotifiedCustomers = "notified_customers"
	StatusNotifiedAuthority = "notified_authority"
	StatusClosed            = "closed"
	StatusNoAction          = "no_action"
)

// Entry is one row of the breach register. The latest seq per incident_id is
// the current snapshot.
type Entry struct {
	ID                   string          `json:"id"`
	IncidentID           string          `json:"incident_id"`
	Seq                  int64           `json:"seq"`
	ProjectID            *string         `json:"project_id,omitempty"`
	AffectsPlatform      bool            `json:"affects_platform"`
	Title                string          `json:"title"`
	Description          string          `json:"description"`
	LikelyConsequences   string          `json:"likely_consequences"`
	MeasuresTaken        string          `json:"measures_taken"`
	DataCategories       []string        `json:"data_categories"`
	SubjectCategories    []string        `json:"subject_categories"`
	RecordsAffected      *int64          `json:"records_affected,omitempty"`
	SubjectsAffected     *int64          `json:"subjects_affected,omitempty"`
	OccurredAt           *time.Time      `json:"occurred_at,omitempty"`
	OccurredUntil        *time.Time      `json:"occurred_until,omitempty"`
	AwarenessAt          time.Time       `json:"awareness_at"`
	ContainedAt          *time.Time      `json:"contained_at,omitempty"`
	ResolvedAt           *time.Time      `json:"resolved_at,omitempty"`
	NotifiedAuthority    bool            `json:"notified_authority"`
	NotifiedAuthorityAt  *time.Time      `json:"notified_authority_at,omitempty"`
	NotifiedCustomers    bool            `json:"notified_customers"`
	NotifiedCustomersAt  *time.Time      `json:"notified_customers_at,omitempty"`
	NotifiedSubjects     bool            `json:"notified_subjects"`
	NotifiedSubjectsAt   *time.Time      `json:"notified_subjects_at,omitempty"`
	LeadSA               *string         `json:"lead_sa,omitempty"`
	MTTDSeconds          *int64          `json:"mttd_seconds,omitempty"`
	MTTRSeconds          *int64          `json:"mttr_seconds,omitempty"`
	Status               string          `json:"status"`
	ActorID              *string         `json:"actor_id,omitempty"`
	ActorEmail           string          `json:"actor_email"`
	Note                 string          `json:"note"`
	Metadata             json.RawMessage `json:"metadata"`
	CreatedAt            time.Time       `json:"created_at"`
}

// OpenInput is the create-incident payload.
type OpenInput struct {
	ProjectID          *string    `json:"project_id,omitempty"`
	AffectsPlatform    bool       `json:"affects_platform"`
	Title              string     `json:"title"`
	Description        string     `json:"description"`
	LikelyConsequences string     `json:"likely_consequences"`
	MeasuresTaken      string     `json:"measures_taken"`
	DataCategories     []string   `json:"data_categories"`
	SubjectCategories  []string   `json:"subject_categories"`
	RecordsAffected    *int64     `json:"records_affected"`
	SubjectsAffected   *int64     `json:"subjects_affected"`
	OccurredAt         *time.Time `json:"occurred_at"`
	OccurredUntil      *time.Time `json:"occurred_until"`
	AwarenessAt        *time.Time `json:"awareness_at"`
	LeadSA             *string    `json:"lead_sa"`
	Note               string     `json:"note"`
}

// UpdateInput is the patch payload. Nil pointer = leave unchanged; setting
// an empty string explicitly is treated as "clear" (caller intent is clear
// from JSON-present-vs-absent in the handler).
type UpdateInput struct {
	Title              *string    `json:"title,omitempty"`
	Description        *string    `json:"description,omitempty"`
	LikelyConsequences *string    `json:"likely_consequences,omitempty"`
	MeasuresTaken      *string    `json:"measures_taken,omitempty"`
	DataCategories     []string   `json:"data_categories,omitempty"`
	SubjectCategories  []string   `json:"subject_categories,omitempty"`
	RecordsAffected    *int64     `json:"records_affected,omitempty"`
	SubjectsAffected   *int64     `json:"subjects_affected,omitempty"`
	ContainedAt        *time.Time `json:"contained_at,omitempty"`
	LeadSA             *string    `json:"lead_sa,omitempty"`
	Status             *string    `json:"status,omitempty"`
	Note               string     `json:"note"`
}

// Metrics is the interface the breach service uses to emit MTTD/MTTR
// observations. The metrics package implements it.
type Metrics interface {
	ObserveBreachMTTD(seconds float64)
	ObserveBreachMTTR(seconds float64)
	IncBreachOpened()
	IncBreachClosed(status string)
}

// Service is the breach-register service.
type Service struct {
	pool     *pgxpool.Pool
	auditSvc *audit.Service
	metrics  Metrics
}

// NewService creates a breach service. metrics may be nil in dev/test.
func NewService(pool *pgxpool.Pool, auditSvc *audit.Service, m Metrics) *Service {
	return &Service{pool: pool, auditSvc: auditSvc, metrics: m}
}

// Open inserts the first row for a new incident and returns the snapshot.
// The caller (handler) supplies actor identity from auth context.
func (s *Service) Open(ctx context.Context, in OpenInput, actorID, actorEmail string) (*Entry, error) {
	if in.Title == "" {
		return nil, fmt.Errorf("title is required")
	}
	awareness := time.Now().UTC()
	if in.AwarenessAt != nil {
		awareness = in.AwarenessAt.UTC()
	}

	var incidentID string
	if err := s.pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&incidentID); err != nil {
		return nil, fmt.Errorf("allocate incident id: %w", err)
	}
	e := Entry{
		IncidentID:         incidentID,
		ProjectID:          in.ProjectID,
		AffectsPlatform:    in.AffectsPlatform,
		Title:              in.Title,
		Description:        in.Description,
		LikelyConsequences: in.LikelyConsequences,
		MeasuresTaken:      in.MeasuresTaken,
		DataCategories:     nonNil(in.DataCategories),
		SubjectCategories:  nonNil(in.SubjectCategories),
		RecordsAffected:    in.RecordsAffected,
		SubjectsAffected:   in.SubjectsAffected,
		OccurredAt:         in.OccurredAt,
		OccurredUntil:      in.OccurredUntil,
		AwarenessAt:        awareness,
		LeadSA:             in.LeadSA,
		Status:             StatusOpen,
		ActorID:            optStr(actorID),
		ActorEmail:         actorEmail,
		Note:               in.Note,
		Metadata:           []byte("{}"),
	}
	if in.OccurredAt != nil {
		// MTTD is "time from breach occurrence to awareness". The DPO
		// confirms the breach window; we record what we know now.
		secs := int64(awareness.Sub(in.OccurredAt.UTC()).Seconds())
		if secs >= 0 {
			e.MTTDSeconds = &secs
		}
	}
	if err := s.insert(ctx, &e); err != nil {
		return nil, err
	}
	if s.metrics != nil {
		s.metrics.IncBreachOpened()
		if e.MTTDSeconds != nil {
			s.metrics.ObserveBreachMTTD(float64(*e.MTTDSeconds))
		}
	}
	s.auditLog(ctx, &e, audit.ActionBreachOpened)
	return &e, nil
}

// Update writes a new snapshot row for an incident with the requested
// changes applied. Status may transition forward only via this call;
// terminal transitions (closed / no_action) go through Close().
func (s *Service) Update(ctx context.Context, incidentID string, in UpdateInput, actorID, actorEmail string) (*Entry, error) {
	latest, err := s.Latest(ctx, incidentID)
	if err != nil {
		return nil, err
	}
	if latest == nil {
		return nil, fmt.Errorf("incident not found")
	}
	if latest.Status == StatusClosed || latest.Status == StatusNoAction {
		return nil, fmt.Errorf("incident is closed; reopen requires a new incident")
	}

	next := *latest
	next.ID = ""
	next.Seq = 0
	next.ActorID = optStr(actorID)
	next.ActorEmail = actorEmail
	next.Note = in.Note

	if in.Title != nil {
		next.Title = *in.Title
	}
	if in.Description != nil {
		next.Description = *in.Description
	}
	if in.LikelyConsequences != nil {
		next.LikelyConsequences = *in.LikelyConsequences
	}
	if in.MeasuresTaken != nil {
		next.MeasuresTaken = *in.MeasuresTaken
	}
	if in.DataCategories != nil {
		next.DataCategories = in.DataCategories
	}
	if in.SubjectCategories != nil {
		next.SubjectCategories = in.SubjectCategories
	}
	if in.RecordsAffected != nil {
		next.RecordsAffected = in.RecordsAffected
	}
	if in.SubjectsAffected != nil {
		next.SubjectsAffected = in.SubjectsAffected
	}
	if in.ContainedAt != nil {
		next.ContainedAt = in.ContainedAt
	}
	if in.LeadSA != nil {
		next.LeadSA = in.LeadSA
	}
	if in.Status != nil {
		if *in.Status == StatusClosed || *in.Status == StatusNoAction {
			return nil, fmt.Errorf("use /close to terminate an incident")
		}
		next.Status = *in.Status
	}

	if err := s.insert(ctx, &next); err != nil {
		return nil, err
	}
	s.auditLog(ctx, &next, audit.ActionBreachUpdated)
	return &next, nil
}

// MarkNotification stamps either the customer or authority notification on
// a new register row. `kind` is "customers", "authority", or "subjects".
func (s *Service) MarkNotification(ctx context.Context, incidentID, kind, leadSA, note, actorID, actorEmail string) (*Entry, error) {
	latest, err := s.Latest(ctx, incidentID)
	if err != nil {
		return nil, err
	}
	if latest == nil {
		return nil, fmt.Errorf("incident not found")
	}

	next := *latest
	next.ID = ""
	next.Seq = 0
	next.ActorID = optStr(actorID)
	next.ActorEmail = actorEmail
	next.Note = note
	now := time.Now().UTC()

	switch kind {
	case "customers":
		next.NotifiedCustomers = true
		next.NotifiedCustomersAt = &now
		next.Status = StatusNotifiedCustomers
	case "authority":
		next.NotifiedAuthority = true
		next.NotifiedAuthorityAt = &now
		if leadSA != "" {
			next.LeadSA = &leadSA
		}
		next.Status = StatusNotifiedAuthority
	case "subjects":
		next.NotifiedSubjects = true
		next.NotifiedSubjectsAt = &now
	default:
		return nil, fmt.Errorf("unknown notification kind: %s", kind)
	}

	if err := s.insert(ctx, &next); err != nil {
		return nil, err
	}
	action := audit.ActionBreachNotifiedCustomers
	if kind == "authority" {
		action = audit.ActionBreachNotifiedAuthority
	} else if kind == "subjects" {
		action = audit.ActionBreachNotifiedSubjects
	}
	s.auditLog(ctx, &next, action)
	return &next, nil
}

// Close writes a terminal row. `terminalStatus` must be "closed" or
// "no_action". MTTR is computed from awareness_at → resolved_at (now).
func (s *Service) Close(ctx context.Context, incidentID, terminalStatus, note, actorID, actorEmail string) (*Entry, error) {
	if terminalStatus != StatusClosed && terminalStatus != StatusNoAction {
		return nil, fmt.Errorf("terminal status must be 'closed' or 'no_action'")
	}
	latest, err := s.Latest(ctx, incidentID)
	if err != nil {
		return nil, err
	}
	if latest == nil {
		return nil, fmt.Errorf("incident not found")
	}
	if latest.Status == StatusClosed || latest.Status == StatusNoAction {
		return latest, nil
	}

	now := time.Now().UTC()
	next := *latest
	next.ID = ""
	next.Seq = 0
	next.ActorID = optStr(actorID)
	next.ActorEmail = actorEmail
	next.Note = note
	next.Status = terminalStatus
	next.ResolvedAt = &now

	mttr := int64(now.Sub(latest.AwarenessAt).Seconds())
	if mttr < 0 {
		mttr = 0
	}
	next.MTTRSeconds = &mttr

	if err := s.insert(ctx, &next); err != nil {
		return nil, err
	}
	if s.metrics != nil {
		s.metrics.IncBreachClosed(terminalStatus)
		s.metrics.ObserveBreachMTTR(float64(mttr))
	}
	s.auditLog(ctx, &next, audit.ActionBreachClosed)
	return &next, nil
}

// Latest returns the most recent snapshot row for an incident, or nil if
// the incident does not exist.
func (s *Service) Latest(ctx context.Context, incidentID string) (*Entry, error) {
	const q = `SELECT ` + entryCols + `
	             FROM public.breach_register
	            WHERE incident_id = $1
	         ORDER BY seq DESC LIMIT 1`
	row := s.pool.QueryRow(ctx, q, incidentID)
	e, err := scanEntry(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return e, err
}

// History returns every row written for an incident, oldest first.
func (s *Service) History(ctx context.Context, incidentID string) ([]Entry, error) {
	const q = `SELECT ` + entryCols + `
	             FROM public.breach_register
	            WHERE incident_id = $1
	         ORDER BY seq ASC`
	rows, err := s.pool.Query(ctx, q, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEntries(rows)
}

// ListLatest returns the latest snapshot per incident, optionally scoped to
// a project. When projectID is empty, returns platform-wide incidents.
func (s *Service) ListLatest(ctx context.Context, projectID string, limit int) ([]Entry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	// DISTINCT ON (incident_id) ORDER BY incident_id, seq DESC picks the
	// latest row per incident; the outer query reorders by awareness_at.
	const tmpl = `SELECT * FROM (
	                SELECT DISTINCT ON (incident_id) ` + entryCols + `
	                  FROM public.breach_register
	                 WHERE ($1::uuid IS NULL OR project_id = $1::uuid)
	              ORDER BY incident_id, seq DESC
	              ) latest
	              ORDER BY awareness_at DESC
	              LIMIT $2`
	var arg interface{}
	if projectID != "" {
		arg = projectID
	}
	rows, err := s.pool.Query(ctx, tmpl, arg, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEntries(rows)
}

// ── internal ──────────────────────────────────────────────────────────────

const entryCols = `id, incident_id, seq, project_id, affects_platform,
       title, description, likely_consequences, measures_taken,
       data_categories, subject_categories, records_affected, subjects_affected,
       occurred_at, occurred_until, awareness_at, contained_at, resolved_at,
       notified_authority, notified_authority_at, notified_customers, notified_customers_at,
       notified_subjects, notified_subjects_at, lead_sa,
       mttd_seconds, mttr_seconds, status,
       actor_id, actor_email, note, metadata, created_at`

func (s *Service) insert(ctx context.Context, e *Entry) error {
	meta := e.Metadata
	if len(meta) == 0 {
		meta = []byte("{}")
	}
	const q = `INSERT INTO public.breach_register
		(incident_id, project_id, affects_platform,
		 title, description, likely_consequences, measures_taken,
		 data_categories, subject_categories, records_affected, subjects_affected,
		 occurred_at, occurred_until, awareness_at, contained_at, resolved_at,
		 notified_authority, notified_authority_at, notified_customers, notified_customers_at,
		 notified_subjects, notified_subjects_at, lead_sa,
		 mttd_seconds, mttr_seconds, status,
		 actor_id, actor_email, note, metadata)
		VALUES ($1, $2, $3,
		        $4, $5, $6, $7,
		        $8, $9, $10, $11,
		        $12, $13, $14, $15, $16,
		        $17, $18, $19, $20,
		        $21, $22, $23,
		        $24, $25, $26,
		        $27, $28, $29, $30)
		RETURNING id, seq, created_at`
	return s.pool.QueryRow(ctx, q,
		e.IncidentID, e.ProjectID, e.AffectsPlatform,
		e.Title, e.Description, e.LikelyConsequences, e.MeasuresTaken,
		e.DataCategories, e.SubjectCategories, e.RecordsAffected, e.SubjectsAffected,
		e.OccurredAt, e.OccurredUntil, e.AwarenessAt, e.ContainedAt, e.ResolvedAt,
		e.NotifiedAuthority, e.NotifiedAuthorityAt, e.NotifiedCustomers, e.NotifiedCustomersAt,
		e.NotifiedSubjects, e.NotifiedSubjectsAt, e.LeadSA,
		e.MTTDSeconds, e.MTTRSeconds, e.Status,
		e.ActorID, e.ActorEmail, e.Note, meta,
	).Scan(&e.ID, &e.Seq, &e.CreatedAt)
}

// rowScanner accepts both pgx.Row and pgx.Rows so scanEntry/scanEntries share
// the same field list.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanEntry(r rowScanner) (*Entry, error) {
	var e Entry
	var meta []byte
	if err := r.Scan(
		&e.ID, &e.IncidentID, &e.Seq, &e.ProjectID, &e.AffectsPlatform,
		&e.Title, &e.Description, &e.LikelyConsequences, &e.MeasuresTaken,
		&e.DataCategories, &e.SubjectCategories, &e.RecordsAffected, &e.SubjectsAffected,
		&e.OccurredAt, &e.OccurredUntil, &e.AwarenessAt, &e.ContainedAt, &e.ResolvedAt,
		&e.NotifiedAuthority, &e.NotifiedAuthorityAt, &e.NotifiedCustomers, &e.NotifiedCustomersAt,
		&e.NotifiedSubjects, &e.NotifiedSubjectsAt, &e.LeadSA,
		&e.MTTDSeconds, &e.MTTRSeconds, &e.Status,
		&e.ActorID, &e.ActorEmail, &e.Note, &meta, &e.CreatedAt,
	); err != nil {
		return nil, err
	}
	if len(meta) == 0 {
		meta = []byte("{}")
	}
	e.Metadata = meta
	return &e, nil
}

func scanEntries(rows pgx.Rows) ([]Entry, error) {
	out := make([]Entry, 0)
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// auditLog mirrors compliance/export's pattern: the breach service emits its
// own audit-log entry on every register write so a tampered register can be
// cross-checked against the chained audit_log.
func (s *Service) auditLog(ctx context.Context, e *Entry, action string) {
	if s.auditSvc == nil {
		return
	}
	projectID := ""
	if e.ProjectID != nil {
		projectID = *e.ProjectID
	}
	actorID := ""
	if e.ActorID != nil {
		actorID = *e.ActorID
	}
	meta := map[string]any{
		"incident_id": e.IncidentID,
		"status":      e.Status,
	}
	if e.MTTDSeconds != nil {
		meta["mttd_seconds"] = *e.MTTDSeconds
	}
	if e.MTTRSeconds != nil {
		meta["mttr_seconds"] = *e.MTTRSeconds
	}
	s.auditSvc.Log(ctx, projectID, actorID, e.ActorEmail, action,
		audit.WithTarget("breach", e.IncidentID),
		audit.WithMetadata(meta))
	slog.Info("breach register write", "incident_id", e.IncidentID, "action", action, "status", e.Status)
}

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
