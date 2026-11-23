package tenant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

type fakeBetaMailer struct {
	mu       sync.Mutex
	subjects []string
}

func (f *fakeBetaMailer) SendRaw(_ context.Context, _ string, subject, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subjects = append(f.subjects, subject)
	return nil
}

func (f *fakeBetaMailer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subjects)
}

// TestTeamBetaWindow_DB: a project gets no beta end unless it is Team;
// the job reminds once inside the window, suspends at the end (data kept,
// ops alerted), and an admin extension reactivates only a suspension the
// window caused.
func TestTeamBetaWindow_DB(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	svc := &TenantService{pool: pool}

	uid := insertTestPlatformUser(t, pool, "team-beta-window@test.eurobase.local")
	project, err := svc.CreateProject(ctx, uid, "team-beta-window@test.eurobase.local", CreateProjectRequest{
		Name: "Beta Window", Slug: "test-team-beta-window", Region: "fr-par", Plan: "free",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	t.Cleanup(func() { cleanupProject(t, pool, project.ID) })
	if _, err := pool.Exec(ctx, `UPDATE projects SET status = 'active' WHERE id = $1`, project.ID); err != nil {
		t.Fatalf("activate: %v", err)
	}

	var ends *time.Time
	if err := pool.QueryRow(ctx, `SELECT beta_ends_at FROM projects WHERE id = $1`, project.ID).Scan(&ends); err != nil {
		t.Fatal(err)
	}
	if ends != nil {
		t.Fatalf("free project got a beta end: %v", ends)
	}

	extend := func() (int, map[string]any) {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", project.ID)
		req := httptest.NewRequest(http.MethodPost, "/platform/admin/projects/"+project.ID+"/beta-extend", nil)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()
		AdminExtendTeamBeta(pool)(rec, req)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body
	}
	if c, _ := extend(); c != http.StatusConflict {
		t.Fatalf("extend without a beta end: %d, want 409", c)
	}

	// Simulate a Team beta project three days before its end.
	if _, err := pool.Exec(ctx, `UPDATE projects SET plan = 'team', beta_ends_at = now() + interval '3 days' WHERE id = $1`, project.ID); err != nil {
		t.Fatal(err)
	}
	mailer := &fakeBetaMailer{}
	var alerts int
	job := &TeamBetaExpiry{
		Pool: pool, Mailer: mailer, OpsWebhook: "https://example.invalid/hook", ConsoleURL: "https://console.test",
		SendDiscord: func(_, _ string, embed map[string]any) {
			if !strings.Contains(embed["description"].(string), project.ID) {
				t.Errorf("alert without the project id: %v", embed)
			}
			alerts++
		},
	}
	status := func() (string, bool, bool) {
		var s string
		var reminded, expired bool
		if err := pool.QueryRow(ctx,
			`SELECT status, beta_reminded_at IS NOT NULL, beta_expired_at IS NOT NULL FROM projects WHERE id = $1`,
			project.ID).Scan(&s, &reminded, &expired); err != nil {
			t.Fatal(err)
		}
		return s, reminded, expired
	}

	job.RunOnce(ctx)
	job.RunOnce(ctx)
	if s, reminded, _ := status(); s != "active" || !reminded || mailer.count() != 1 {
		t.Fatalf("after reminder: status %s reminded %v mails %d; want active, true, 1", s, reminded, mailer.count())
	}

	// The end passes: suspended, data kept, owner told, ops alerted.
	if _, err := pool.Exec(ctx, `UPDATE projects SET beta_ends_at = now() - interval '1 minute' WHERE id = $1`, project.ID); err != nil {
		t.Fatal(err)
	}
	job.RunOnce(ctx)
	job.RunOnce(ctx)
	if s, _, expired := status(); s != "suspended" || !expired || mailer.count() != 2 || alerts != 1 {
		t.Fatalf("after end: status %s expired %v mails %d alerts %d; want suspended, true, 2, 1", s, expired, mailer.count(), alerts)
	}
	if !strings.Contains(mailer.subjects[1], "has ended") {
		t.Fatalf("second mail: %q", mailer.subjects[1])
	}

	// Extending reactivates a suspension the window caused.
	c, body := extend()
	if c != http.StatusOK || body["reactivated"] != true || body["status"] != "active" {
		t.Fatalf("extend after end: %d %v", c, body)
	}
	if s, reminded, expired := status(); s != "active" || reminded || expired {
		t.Fatalf("after extend: status %s reminded %v expired %v", s, reminded, expired)
	}
	if err := pool.QueryRow(ctx, `SELECT beta_ends_at FROM projects WHERE id = $1`, project.ID).Scan(&ends); err != nil {
		t.Fatal(err)
	}
	if d := time.Until(*ends); d < 13*24*time.Hour || d > 15*24*time.Hour {
		t.Fatalf("extended end in %v, want ~14 days", d)
	}

	// A suspension for another reason stays when the beta is extended.
	if _, err := pool.Exec(ctx, `UPDATE projects SET status = 'suspended' WHERE id = $1`, project.ID); err != nil {
		t.Fatal(err)
	}
	if c, body := extend(); c != http.StatusOK || body["reactivated"] != false || body["status"] != "suspended" {
		t.Fatalf("extend of an admin-suspended project: %d %v", c, body)
	}
	job.RunOnce(ctx)
	if mailer.count() != 2 || alerts != 1 {
		t.Fatalf("job acted on a project suspended for another reason: mails %d alerts %d", mailer.count(), alerts)
	}
}

func TestRenderTeamBetaMail(t *testing.T) {
	p := betaProject{id: "11111111-1111-1111-1111-111111111111", name: `<b>Acme</b>`, endsAt: time.Date(2026, 12, 7, 10, 0, 0, 0, time.UTC)}
	subj, body := renderTeamBetaMail(p, false, "https://console.eurobase.app/")
	if !strings.Contains(subj, "7 December 2026") || strings.Contains(body, "<b>Acme</b>") || !strings.Contains(body, "&lt;b&gt;Acme&lt;/b&gt;") {
		t.Fatalf("reminder: subject %q, name not escaped", subj)
	}
	if !strings.Contains(body, "https://console.eurobase.app/p/"+p.id) || !strings.Contains(body, "contact@eurobase.app") {
		t.Fatal("reminder misses the project link or the contact address")
	}
	subj, body = renderTeamBetaMail(p, true, "https://console.eurobase.app")
	if !strings.Contains(subj, "has ended") || !strings.Contains(body, "Nothing is deleted") {
		t.Fatalf("ended mail: %q", subj)
	}
}
