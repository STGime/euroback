package breach

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
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

// HandleOpen creates a new incident.
// POST /platform/projects/{id}/compliance/breaches
//
// The project_id in the URL is the scope check (the auth middleware already
// verified the caller has admin on it); if the breach truly affects the
// platform side, the handler also accepts `affects_platform: true` and a
// nil project_id payload — but we always anchor at least the URL project so
// every call has a tenant context for RBAC.
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
		if in.ProjectID == nil && !in.AffectsPlatform {
			pid := projectID
			in.ProjectID = &pid
		}

		entry, err := h.Svc.Open(r.Context(), in, claims.Subject, claims.Email)
		if err != nil {
			slog.Error("breach open failed", "project_id", projectID, "error", err)
			writeError(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, entry, http.StatusCreated)
	}
}

// HandleGet returns the full append-only history for one incident.
// GET /platform/projects/{id}/compliance/breaches/{incidentId}
func (h *Handler) HandleGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		incidentID := chi.URLParam(r, "incidentId")
		hist, err := h.Svc.History(r.Context(), incidentID)
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
		entry, err := h.Svc.Update(r.Context(), incidentID, in, claims.Subject, claims.Email)
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
		entry, err := h.Svc.Close(r.Context(), incidentID, body.Status, body.Note, claims.Subject, claims.Email)
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
// it via BCC to the supplied recipients (typically: the project's billing
// contact + the controller's data-protection contact, supplied by the DPO).
// POST /platform/projects/{id}/compliance/breaches/{incidentId}/notify-customers
func (h *Handler) HandleNotifyCustomers() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		incidentID := chi.URLParam(r, "incidentId")
		claims, _ := auth.ClaimsFromContext(r.Context())
		if claims == nil {
			writeError(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var body struct {
			Recipients []string `json:"recipients"`
			Note       string   `json:"note"`
			Preview    bool     `json:"preview"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, "invalid json body", http.StatusBadRequest)
			return
		}
		latest, err := h.Svc.Latest(r.Context(), incidentID)
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
				"subject":   subject,
				"html":      html,
				"recipient_count": len(body.Recipients),
			}, http.StatusOK)
			return
		}
		if h.Mailer == nil {
			writeError(w, "email service not configured", http.StatusServiceUnavailable)
			return
		}
		res, err := h.Mailer.SendBulkBCC(r.Context(), body.Recipients, subject, html)
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		entry, err := h.Svc.MarkNotification(r.Context(), incidentID, "customers", "", body.Note, claims.Subject, claims.Email)
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

// HandleAuthorityForm returns the Markdown paste-in for the supervisory
// authority and (optionally) records that the SA was notified.
// POST /platform/projects/{id}/compliance/breaches/{incidentId}/authority-form
func (h *Handler) HandleAuthorityForm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		incidentID := chi.URLParam(r, "incidentId")
		claims, _ := auth.ClaimsFromContext(r.Context())
		if claims == nil {
			writeError(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var body struct {
			Filed   bool   `json:"filed"`
			LeadSA  string `json:"lead_sa"`
			Note    string `json:"note"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		latest, err := h.Svc.Latest(r.Context(), incidentID)
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
			entry, err = h.Svc.MarkNotification(r.Context(), incidentID, "authority", body.LeadSA, body.Note, claims.Subject, claims.Email)
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
		incidentID := chi.URLParam(r, "incidentId")
		latest, err := h.Svc.Latest(r.Context(), incidentID)
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
