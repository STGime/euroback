package breach

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/email"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MailerAdapter adapts internal/email.EmailService to the breach Mailer
// interface so the breach package doesn't import internal/email (which
// would create an audit/email/breach diamond).
type MailerAdapter struct {
	Svc *email.EmailService
}

func (m *MailerAdapter) SendBulkBCC(ctx context.Context, recipients []string, subject, htmlBody string) (BulkSendResult, error) {
	if m == nil || m.Svc == nil {
		return BulkSendResult{}, nil
	}
	r, err := m.Svc.SendBulkBCC(ctx, recipients, subject, htmlBody)
	return BulkSendResult{Sent: r.Sent, Failed: r.Failed}, err
}

func (m *MailerAdapter) SendRaw(ctx context.Context, to, subject, htmlBody string) error {
	if m == nil || m.Svc == nil {
		return nil
	}
	return m.Svc.SendRaw(ctx, to, subject, htmlBody)
}

// Handler is the HTTP surface for the breach register.
type Handler struct {
	Svc      *Service
	Pool     *pgxpool.Pool
	Mailer   Mailer
	DPOEmail string // populated from env (DPO_EMAIL) by the router wiring.
}

func writeJSON(w http.ResponseWriter, data interface{}, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, msg string, status int) {
	writeJSON(w, map[string]string{"error": msg}, status)
}

// HandleList lists the latest snapshot per incident for a project.
// GET /platform/projects/{id}/compliance/breaches
func (h *Handler) HandleList() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		entries, err := h.Svc.ListLatest(r.Context(), projectID, limit)
		if err != nil {
			slog.Error("breach list failed", "project_id", projectID, "error", err)
			writeError(w, "failed to list breaches", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]interface{}{"breaches": entries}, http.StatusOK)
	}
}

// HandleOpen creates a new incident scoped to the URL project.
// POST /platform/projects/{id}/compliance/breaches
//
// PR #219 review: this route always anchors the incident at the URL
// `{id}` regardless of what the body asks for. `affects_platform` is a
// tag (true if the incident bleeds into platform infrastructure too),
// not a scope override — a tenant admin can't open a `project_id=NULL`
// incident that would then be reachable from any other project.
// Platform-only incidents are opened by platform admins through a
// future platform-admin endpoint, not here.
func (h *Handler) HandleOpen() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		claims, _ := auth.ClaimsFromContext(r.Context())
		if claims == nil {
			writeError(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var in OpenInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, "invalid json body", http.StatusBadRequest)
			return
		}
		pid := projectID
		in.ProjectID = &pid

		entry, err := h.Svc.Open(r.Context(), in, claims.Subject, claims.Email)
		if err != nil {
			slog.Error("breach open failed", "project_id", projectID, "error", err)
			writeError(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, entry, http.StatusCreated)
	}
}

// HandleGet returns the full append-only history for one incident, scoped
// to the URL project — cross-tenant lookups return 404 (PR #219 review).
// GET /platform/projects/{id}/compliance/breaches/{incidentId}
func (h *Handler) HandleGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		incidentID := chi.URLParam(r, "incidentId")
		hist, err := h.Svc.History(r.Context(), projectID, incidentID)
		if err != nil {
			slog.Error("breach history failed", "incident_id", incidentID, "error", err)
			writeError(w, "failed to load incident", http.StatusInternalServerError)
			return
		}
		if len(hist) == 0 {
			writeError(w, "incident not found", http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]interface{}{
			"latest":  hist[len(hist)-1],
			"history": hist,
		}, http.StatusOK)
	}
}

// HandleUpdate applies a partial change as a new register row.
// PATCH /platform/projects/{id}/compliance/breaches/{incidentId}
func (h *Handler) HandleUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		incidentID := chi.URLParam(r, "incidentId")
		claims, _ := auth.ClaimsFromContext(r.Context())
		if claims == nil {
			writeError(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var in UpdateInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, "invalid json body", http.StatusBadRequest)
			return
		}
		entry, err := h.Svc.Update(r.Context(), projectID, incidentID, in, claims.Subject, claims.Email)
		if err != nil {
			writeError(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, entry, http.StatusOK)
	}
}

// HandleClose terminates an incident with a "closed" or "no_action" row.
// POST /platform/projects/{id}/compliance/breaches/{incidentId}/close
func (h *Handler) HandleClose() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		incidentID := chi.URLParam(r, "incidentId")
		claims, _ := auth.ClaimsFromContext(r.Context())
		if claims == nil {
			writeError(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var body struct {
			Status string `json:"status"`
			Note   string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, "invalid json body", http.StatusBadRequest)
			return
		}
		if body.Status == "" {
			body.Status = StatusClosed
		}
		entry, err := h.Svc.Close(r.Context(), projectID, incidentID, body.Status, body.Note, claims.Subject, claims.Email)
		if err != nil {
			writeError(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, entry, http.StatusOK)
	}
}

// HandleSubjects identifies the affected end-users for an incident's scope.
// POST /platform/projects/{id}/compliance/breaches/{incidentId}/subjects
//
// The request body is a SubjectQuery JSON; the response is a count + a
// capped sample of IDs the DPO can spot-check. The handler also audits the
// identification call because pulling a list of affected subjects is itself
// a privacy-sensitive operation.
func (h *Handler) HandleSubjects() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		incidentID := chi.URLParam(r, "incidentId")
		claims, _ := auth.ClaimsFromContext(r.Context())
		if claims == nil {
			writeError(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var q SubjectQuery
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&q)
		}
		result, err := IdentifySubjects(r.Context(), h.Pool, projectID, q)
		if err != nil {
			slog.Error("breach subject identification failed", "incident_id", incidentID, "error", err)
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if h.Svc.auditSvc != nil {
			h.Svc.auditSvc.Log(r.Context(), projectID, claims.Subject, claims.Email,
				audit.ActionBreachSubjectsIdentified,
				audit.WithTarget("breach", incidentID),
				audit.WithMetadata(map[string]any{
					"count":         result.Count,
					"tables_probed": result.TablesProbed,
				}),
				audit.WithIP(r.RemoteAddr))
		}
		writeJSON(w, result, http.StatusOK)
	}
}

// HandleNotifyCustomers renders the customer-notification email and sends
// it via BCC to the project's known controller contacts. Recipients are
// resolved server-side from the project's owner + admin team members
// (PR #219 review — caller-supplied recipients are no longer accepted to
// prevent an admin from BCC'ing an official-looking breach notice to
// arbitrary addresses from platform email infra).
// POST /platform/projects/{id}/compliance/breaches/{incidentId}/notify-customers
func (h *Handler) HandleNotifyCustomers() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		incidentID := chi.URLParam(r, "incidentId")
		claims, _ := auth.ClaimsFromContext(r.Context())
		if claims == nil {
			writeError(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var body struct {
			ExtraRecipients []string `json:"extra_recipients"`
			Note            string   `json:"note"`
			Preview         bool     `json:"preview"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, "invalid json body", http.StatusBadRequest)
			return
		}
		recipients, err := h.resolveCustomerRecipients(r.Context(), projectID, body.ExtraRecipients)
		if err != nil {
			slog.Error("breach customer recipient resolve failed", "project_id", projectID, "error", err)
			writeError(w, "failed to resolve recipients", http.StatusInternalServerError)
			return
		}
		if len(recipients) == 0 {
			writeError(w, "project has no controller contacts on file", http.StatusBadRequest)
			return
		}
		latest, err := h.Svc.Latest(r.Context(), projectID, incidentID)
		if err != nil || latest == nil {
			writeError(w, "incident not found", http.StatusNotFound)
			return
		}
		subject, html, err := RenderCustomerEmail(latest, h.DPOEmail)
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if body.Preview {
			writeJSON(w, map[string]interface{}{
				"subject":         subject,
				"html":            html,
				"recipient_count": len(recipients),
				"recipients":      recipients,
			}, http.StatusOK)
			return
		}
		if h.Mailer == nil {
			writeError(w, "email service not configured", http.StatusServiceUnavailable)
			return
		}
		res, err := h.Mailer.SendBulkBCC(r.Context(), recipients, subject, html)
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		entry, err := h.Svc.MarkNotification(r.Context(), projectID, incidentID, "customers", "", body.Note, claims.Subject, claims.Email)
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]interface{}{
			"sent":   res.Sent,
			"failed": res.Failed,
			"entry":  entry,
		}, http.StatusOK)
	}
}

// resolveCustomerRecipients returns the email addresses that should
// receive the breach notice for a project: every owner + admin team
// member. Optional `extra` addresses (DPO-supplied controller DPO
// contacts that aren't already team members) are unioned in only if
// they parse as a plausible email address. Duplicates are de-duped.
// PR #219 review: replaces caller-supplied recipients so an admin
// can't BCC arbitrary addresses from platform email infra.
func (h *Handler) resolveCustomerRecipients(ctx context.Context, projectID string, extra []string) ([]string, error) {
	rows, err := h.Pool.Query(ctx, `
		SELECT DISTINCT lower(u.email)
		  FROM public.project_members pm
		  JOIN public.platform_users u ON u.id = pm.user_id
		 WHERE pm.project_id = $1 AND pm.role IN ('owner','admin')`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seen := map[string]bool{}
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		if e != "" && !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, e := range extra {
		e = strings.ToLower(strings.TrimSpace(e))
		if isPlausibleEmail(e) && !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out, nil
}

// isPlausibleEmail is a deliberately-cheap sanity check; it is not RFC-5322
// compliant. The downstream TEM client rejects malformed addresses with a
// 4xx, this just keeps the BCC list short and predictable.
func isPlausibleEmail(s string) bool {
	if len(s) < 5 || len(s) > 254 {
		return false
	}
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 || strings.Count(s, "@") != 1 {
		return false
	}
	if strings.ContainsAny(s, " \t\n\r,;<>\"'()") {
		return false
	}
	if !strings.Contains(s[at+1:], ".") {
		return false
	}
	return true
}

// HandleAuthorityForm returns the Markdown paste-in for the supervisory
// authority and (optionally) records that the SA was notified.
// POST /platform/projects/{id}/compliance/breaches/{incidentId}/authority-form
func (h *Handler) HandleAuthorityForm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		incidentID := chi.URLParam(r, "incidentId")
		claims, _ := auth.ClaimsFromContext(r.Context())
		if claims == nil {
			writeError(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var body struct {
			Filed  bool   `json:"filed"`
			LeadSA string `json:"lead_sa"`
			Note   string `json:"note"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		latest, err := h.Svc.Latest(r.Context(), projectID, incidentID)
		if err != nil || latest == nil {
			writeError(w, "incident not found", http.StatusNotFound)
			return
		}
		form, err := RenderAuthorityForm(latest, h.DPOEmail)
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var entry *Entry
		if body.Filed {
			entry, err = h.Svc.MarkNotification(r.Context(), projectID, incidentID, "authority", body.LeadSA, body.Note, claims.Subject, claims.Email)
			if err != nil {
				writeError(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		writeJSON(w, map[string]interface{}{
			"form":  form,
			"entry": entry,
		}, http.StatusOK)
	}
}

// HandleSLAStatus reports per-incident SLA status (DPA §10 24h, Art. 33
// 72h) so the console can render a banner when one is about to elapse.
// GET /platform/projects/{id}/compliance/breaches/{incidentId}/sla
func (h *Handler) HandleSLAStatus() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		incidentID := chi.URLParam(r, "incidentId")
		latest, err := h.Svc.Latest(r.Context(), projectID, incidentID)
		if err != nil || latest == nil {
			writeError(w, "incident not found", http.StatusNotFound)
			return
		}
		now := time.Now().UTC()
		customerSLA := latest.AwarenessAt.Add(24 * time.Hour)
		authoritySLA := latest.AwarenessAt.Add(72 * time.Hour)
		writeJSON(w, map[string]interface{}{
			"incident_id":       incidentID,
			"awareness_at":      latest.AwarenessAt,
			"now":               now,
			"customer_sla_at":   customerSLA,
			"customer_notified": latest.NotifiedCustomers,
			"customer_breach":   !latest.NotifiedCustomers && now.After(customerSLA),
			"authority_sla_at":  authoritySLA,
			"authority_notified": latest.NotifiedAuthority,
			"authority_breach":  !latest.NotifiedAuthority && now.After(authoritySLA),
		}, http.StatusOK)
	}
}
