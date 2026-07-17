package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// MailingCategories enumerates every mailing_preferences.category
// value the console UI exposes. Order matches how the console renders
// them (most-frequently-relevant first). `all` is deliberately last —
// it's the nuclear opt-out that suppresses every other category.
var MailingCategories = []string{"onboarding", "beta_updates", "usage_alerts", "all"}

// MailingCategoryLabel is what the console/API surfaces as human copy
// alongside the raw category. Kept server-side so it's consistent
// across the drip-mail unsubscribe page + the account UI.
func MailingCategoryLabel(category string) string {
	switch category {
	case "onboarding":
		return "Onboarding drip"
	case "beta_updates":
		return "Beta updates"
	case "usage_alerts":
		return "Usage alerts"
	case "all":
		return "All platform mail"
	default:
		return category
	}
}

// MailingCategoryDescription is the one-line explanation shown under
// each toggle in the console. Server-owned for the same reason as the
// label — one source of truth for what each category actually gates.
func MailingCategoryDescription(category string) string {
	switch category {
	case "onboarding":
		return "The 6-mail welcome series over your first 10 days."
	case "beta_updates":
		return "Occasional platform + roadmap updates during beta."
	case "usage_alerts":
		return "Alerts when a project hits 80% or 95% of a plan cap."
	case "all":
		return "Suppress every category above. Transactional mail (verification, password reset, magic link) is separate and always sent."
	default:
		return ""
	}
}

// MailingPreference is one row exposed to the console. `OptedOutAt`
// is nil when the user is opted in (either no row exists or a row
// exists with opted_out_at IS NULL because they resubscribed).
type MailingPreference struct {
	Category    string     `json:"category"`
	Label       string     `json:"label"`
	Description string     `json:"description"`
	OptedOut    bool       `json:"opted_out"`
	OptedOutAt  *time.Time `json:"opted_out_at,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
}

// HandleListMailingPreferences returns the current user's opt-out
// state for every known category. Missing rows are surfaced as
// `opted_out=false` so the console can render every category with an
// explicit toggle rather than "unknown".
//
// GET /platform/auth/account/mailing-preferences
func (s *PlatformAuthService) ListMailingPreferences(ctx context.Context, userID string) ([]MailingPreference, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT category, opted_out_at, updated_at
		   FROM mailing_preferences
		  WHERE user_id = $1`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list mailing preferences: %w", err)
	}
	defer rows.Close()

	stored := make(map[string]MailingPreference, len(MailingCategories))
	for rows.Next() {
		var p MailingPreference
		if err := rows.Scan(&p.Category, &p.OptedOutAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan mailing preference: %w", err)
		}
		p.OptedOut = p.OptedOutAt != nil
		stored[p.Category] = p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mailing preferences: %w", err)
	}

	out := make([]MailingPreference, 0, len(MailingCategories))
	for _, cat := range MailingCategories {
		if p, ok := stored[cat]; ok {
			p.Label = MailingCategoryLabel(cat)
			p.Description = MailingCategoryDescription(cat)
			out = append(out, p)
			continue
		}
		out = append(out, MailingPreference{
			Category:    cat,
			Label:       MailingCategoryLabel(cat),
			Description: MailingCategoryDescription(cat),
		})
	}
	return out, nil
}

// SetMailingPreference upserts one (user, category) row. `optedOut`
// = true sets opted_out_at = now(); false clears it (resubscribe).
// Unknown categories are rejected so a client typo can't slip past
// the DB CHECK constraint with a less-legible error.
func (s *PlatformAuthService) SetMailingPreference(ctx context.Context, userID, category string, optedOut bool) error {
	valid := false
	for _, c := range MailingCategories {
		if c == category {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("unknown mailing category")
	}

	var query string
	if optedOut {
		query = `INSERT INTO mailing_preferences (user_id, category, opted_out_at, updated_at)
		         VALUES ($1, $2, now(), now())
		         ON CONFLICT (user_id, category) DO UPDATE
		            SET opted_out_at = now(), updated_at = now()`
	} else {
		// Resubscribe: keep the row (audit trail) but clear the
		// opted_out_at marker. If no row exists yet, insert an
		// explicit opted-in marker so a later audit shows "user
		// explicitly opted in on X" rather than "absent = default".
		query = `INSERT INTO mailing_preferences (user_id, category, opted_out_at, updated_at)
		         VALUES ($1, $2, NULL, now())
		         ON CONFLICT (user_id, category) DO UPDATE
		            SET opted_out_at = NULL, updated_at = now()`
	}
	if _, err := s.pool.Exec(ctx, query, userID, category); err != nil {
		return fmt.Errorf("set mailing preference: %w", err)
	}
	return nil
}

// HandleListMailingPreferences is the HTTP handler.
func HandleListMailingPreferences(svc *PlatformAuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			writeJSONError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		prefs, err := svc.ListMailingPreferences(r.Context(), claims.Subject)
		if err != nil {
			slog.Warn("list mailing preferences failed", "error", err)
			writeJSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(prefs)
	}
}

// HandleSetMailingPreference is the HTTP handler for PUT.
//
// PUT /platform/auth/account/mailing-preferences
// body: { "category": "onboarding", "opted_out": true }
func HandleSetMailingPreference(svc *PlatformAuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			writeJSONError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			Category string `json:"category"`
			OptedOut bool   `json:"opted_out"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := svc.SetMailingPreference(r.Context(), claims.Subject, req.Category, req.OptedOut); err != nil {
			status := http.StatusInternalServerError
			if isUserError(err) {
				status = http.StatusBadRequest
			}
			slog.Warn("set mailing preference failed", "error", err)
			writeJSONError(w, err.Error(), status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}
