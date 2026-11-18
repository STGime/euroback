package sqllog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func listOptions(r *http.Request) (ListOptions, bool) {
	q := r.URL.Query()
	o := ListOptions{Outcome: q.Get("outcome")}
	switch o.Outcome {
	case "", OutcomeOK, OutcomeError, OutcomeRefused, OutcomeNotRun:
	default:
		return o, false
	}
	o.Limit, _ = strconv.Atoi(q.Get("limit"))
	o.BeforeID, _ = strconv.ParseInt(q.Get("before"), 10, 64)
	return o, true
}

func writeList(w http.ResponseWriter, entries []Entry) {
	resp := map[string]any{
		"entries":        entries,
		"retention_days": int(Retention.Hours() / 24),
	}
	if n := len(entries); n > 0 {
		resp["next_before"] = entries[n-1].ID
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func writeError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// HandleProjectLog serves GET /platform/projects/{id}/sql-log
// (?outcome=ok|error|refused, ?before=<id>, ?limit=). pool must be the
// developer pool. Mount behind the admin role: entries contain statement
// text, which can contain the project's data.
func HandleProjectLog(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		o, ok := listOptions(r)
		if !ok {
			writeError(w, "outcome must be ok, error, refused or not_run", http.StatusBadRequest)
			return
		}
		projectID := chi.URLParam(r, "id")
		entries, err := List(r.Context(), pool, projectID, o)
		if err != nil {
			slog.Error("list sql log failed", "project_id", projectID, "error", err)
			writeError(w, "failed to load the SQL log", http.StatusInternalServerError)
			return
		}
		writeList(w, entries)
	}
}

// HandleAdminLog serves GET /platform/admin/sql-log: entries across all
// projects (superadmin), same filters. Developer pool.
func HandleAdminLog(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		o, ok := listOptions(r)
		if !ok {
			writeError(w, "outcome must be ok, error, refused or not_run", http.StatusBadRequest)
			return
		}
		entries, err := ListAll(r.Context(), pool, o)
		if err != nil {
			slog.Error("list sql log (admin) failed", "error", err)
			writeError(w, "failed to load the SQL log", http.StatusInternalServerError)
			return
		}
		writeList(w, entries)
	}
}

// DiscordRefusalAlert returns an OnRefusals hook that posts to a Discord
// webhook. An empty URL returns nil (no alert; the warning is still logged).
func DiscordRefusalAlert(webhookURL, consoleURL string) func(projectID string, count int, reasons []string) {
	if webhookURL == "" {
		return nil
	}
	return func(projectID string, count int, reasons []string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		body, _ := json.Marshal(map[string]any{
			// Never ping anyone from text in the alert.
			"allowed_mentions": map[string]any{"parse": []string{}},
			"embeds": []map[string]any{{
				"title":       "⚠️ Refused SQL statements",
				"description": refusalAlertDescription(projectID, count, reasons),
				"url":         consoleURL + "/admin/sql",
				"color":       0xf59e0b, // amber-500
				"timestamp":   time.Now().UTC().Format(time.RFC3339),
			}},
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			slog.Warn("sql log: Discord alert failed", "error", err)
			return
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			slog.Warn("sql log: Discord alert rejected", "status", resp.StatusCode)
		}
	}
}

// refusalAlertDescription is the alert text: the count, then the most
// frequent refusal reasons, so ops can tell a developer's agent
// inspecting its own schema from probing without opening the log.
func refusalAlertDescription(projectID string, count int, reasons []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Project `%s` had **%d** SQL statements refused by the platform's checks within %s.", projectID, count, RefusalAlertWindow)
	if len(reasons) > 0 {
		sb.WriteString("\n\nTop reasons:")
		for _, r := range reasons {
			// alertReason has already dropped developer-typed text and
			// backticks; a code span renders the rest literally (no
			// markdown, links or mentions).
			sb.WriteString("\n• `")
			sb.WriteString(strings.ReplaceAll(r, "`", ""))
			sb.WriteString("`")
		}
	}
	sb.WriteString("\n\nDetails: SQL log in /admin.")
	return sb.String()
}
