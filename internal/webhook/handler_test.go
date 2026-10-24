package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func serve(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func mount(pool *pgxpool.Pool) http.Handler {
	r := chi.NewRouter()
	r.Mount("/projects/{id}/webhooks", Routes(pool))
	return r
}

// Refusals happen before the database is touched (nil pool).
func TestCreateWebhookValidation(t *testing.T) {
	h := mount(nil)
	for name, body := range map[string]string{
		"http":          `{"url":"http://example.com/h","events":["db.insert"]}`,
		"localhost":     `{"url":"https://localhost/h","events":["db.insert"]}`,
		"private ip":    `{"url":"https://10.1.2.3/h","events":["db.insert"]}`,
		"metadata":      `{"url":"https://169.254.169.254/latest","events":["db.insert"]}`,
		"cluster name":  `{"url":"https://gateway.eurobase.svc.cluster.local/x","events":["db.insert"]}`,
		"single label":  `{"url":"https://gateway/x","events":["db.insert"]}`,
		"credentials":   `{"url":"https://u:p@example.com/h","events":["db.insert"]}`,
		"relative":      `{"url":"/h","events":["db.insert"]}`,
		"unknown event": `{"url":"https://example.com/h","events":["db.insert","auth.signin"]}`,
		"no events":     `{"url":"https://example.com/h","events":[]}`,
	} {
		if rec := serve(h, "POST", "/projects/"+uuid.NewString()+"/webhooks/", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, rec.Code, rec.Body)
		}
	}
	for name, body := range map[string]string{
		"http":          `{"url":"http://example.com/h"}`,
		"unknown event": `{"events":["db.*"]}`,
		"empty events":  `{"events":[]}`,
	} {
		if rec := serve(h, "PATCH", "/projects/"+uuid.NewString()+"/webhooks/"+uuid.NewString(), body); rec.Code != http.StatusBadRequest {
			t.Errorf("update %s: %d %s, want 400", name, rec.Code, rec.Body)
		}
	}
}

// Deliveries of another project's webhook aren't listed under this
// project's path. EVENTS_TEST_GATEWAY_URL; skipped when unset.
func TestDeliveriesScopedToProject(t *testing.T) {
	dsn := os.Getenv("EVENTS_TEST_GATEWAY_URL")
	if dsn == "" {
		t.Skip("EVENTS_TEST_GATEWAY_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	u := strings.ReplaceAll(uuid.NewString()[:12], "-", "")
	var owner string
	if err := pool.QueryRow(ctx, `INSERT INTO public.platform_users (email) VALUES ($1) RETURNING id`, "whscope-"+u+"@test.invalid").Scan(&owner); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.platform_users WHERE id = $1`, owner) }()
	projects := make([]string, 2)
	for i := range projects {
		if err := pool.QueryRow(ctx,
			`INSERT INTO public.projects (owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
			 VALUES ($1, 'x', $2, $3, $4, 'fr-par', 'free', 'active') RETURNING id`,
			owner, "whscope-"+u+string(rune('a'+i)), "tenant_whscope_"+u+string(rune('a'+i)), "b-whscope-"+u+string(rune('a'+i))).Scan(&projects[i]); err != nil {
			t.Fatal(err)
		}
		defer func(id string) { _, _ = pool.Exec(ctx, `DELETE FROM public.projects WHERE id = $1`, id) }(projects[i])
	}

	h := mount(pool)
	rec := serve(h, "POST", "/projects/"+projects[1]+"/webhooks/", `{"url":"https://example.com/h","events":["db.insert"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var hookID string
	if err := pool.QueryRow(ctx, `SELECT id FROM public.webhooks WHERE project_id = $1`, projects[1]).Scan(&hookID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.webhook_deliveries (webhook_id, event, payload) VALUES ($1, 'db.insert', '{"secret":"of project b"}')`, hookID); err != nil {
		t.Fatal(err)
	}
	if rec := serve(h, "GET", "/projects/"+projects[1]+"/webhooks/"+hookID+"/deliveries", ""); !strings.Contains(rec.Body.String(), "of project b") {
		t.Fatalf("own project: %d %s", rec.Code, rec.Body)
	}
	if rec := serve(h, "GET", "/projects/"+projects[0]+"/webhooks/"+hookID+"/deliveries", ""); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("other project: %d %s, want []", rec.Code, rec.Body)
	}
}
