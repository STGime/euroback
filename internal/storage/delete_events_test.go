package storage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/events"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// #845 / #848 review: a storage delete emits storage.delete only when a
// tracked file was removed — S3 answers a delete of a missing key with
// success, so that answer alone would report deletes that never happened.
//
// EVENTS_TEST_ADMIN_URL (provisions a tenant) and EVENTS_TEST_GATEWAY_URL
// (the handler's pool); skipped when unset.
func TestDeleteFileEmitsOnlyForTrackedFiles(t *testing.T) {
	adminURL, gwURL := os.Getenv("EVENTS_TEST_ADMIN_URL"), os.Getenv("EVENTS_TEST_GATEWAY_URL")
	if adminURL == "" || gwURL == "" {
		t.Skip("EVENTS_TEST_ADMIN_URL / EVENTS_TEST_GATEWAY_URL not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	gw, err := pgxpool.New(ctx, gwURL)
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()

	u := strings.ReplaceAll(uuid.NewString()[:12], "-", "")
	var owner, project, schema string
	if err := admin.QueryRow(ctx, `INSERT INTO public.platform_users (email) VALUES ($1) RETURNING id`, "stdel-"+u+"@test.invalid").Scan(&owner); err != nil {
		t.Fatal(err)
	}
	project = uuid.NewString()
	schema = "tenant_" + strings.ReplaceAll(project, "-", "_")
	slug := "stdel-" + u
	if _, err := admin.Exec(ctx,
		`INSERT INTO public.projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		 VALUES ($1, $2, 'x', $3, $4, $5, 'fr-par', 'free', 'active')`, project, owner, slug, schema, "eurobase-"+slug); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `SELECT provision_tenant($1, 'x', 'free')`, project); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = admin.Exec(ctx, `SELECT deprovision_tenant($1)`, project)
		_, _ = admin.Exec(ctx, `DELETE FROM public.projects WHERE id = $1`, project)
		_, _ = admin.Exec(ctx, `DELETE FROM public.platform_users WHERE id = $1`, owner)
	}()
	if _, err := admin.Exec(ctx, `INSERT INTO public.webhooks (project_id, url, events, secret) VALUES ($1, 'https://example.com/h', '{storage.delete}', 's')`, project); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO "`+schema+`".storage_objects (key, content_type, size_bytes) VALUES ('docs/a.txt', 'text/plain', 3)`); err != nil {
		t.Fatal(err)
	}

	s3srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("unexpected S3 call %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent) // S3 says the same for a missing key
	}))
	defer s3srv.Close()
	s3c, err := NewS3Client(s3srv.URL, "fr-par", "k", "s")
	if err != nil {
		t.Fatal(err)
	}
	h := NewStorageHandler(s3c, gw, nil)

	var mu sync.Mutex
	var queued []jobs.DeliverProjectWebhookArgs
	em := events.NewEmitter(gw, func(_ context.Context, p []river.InsertManyParams) error {
		mu.Lock()
		defer mu.Unlock()
		for _, x := range p {
			queued = append(queued, x.Args.(jobs.DeliverProjectWebhookArgs))
		}
		return nil
	})
	del := func(key string) int {
		r := httptest.NewRequest("DELETE", "/"+key, nil)
		c := auth.ContextWithProject(r.Context(), &auth.ProjectContext{ProjectID: project, Slug: slug, SchemaName: schema})
		c = auth.ContextWithClaims(c, &auth.Claims{Subject: owner, Email: "dev@example.com"})
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("*", key)
		c = context.WithValue(events.WithEmitter(c, em), chi.RouteCtxKey, rctx)
		w := httptest.NewRecorder()
		h.DeleteFile(w, r.WithContext(c))
		_ = em.Wait(ctx)
		return w.Code
	}

	if code := del("docs/a.txt"); code != http.StatusNoContent {
		t.Fatalf("delete tracked file: %d", code)
	}
	if code := del("docs/never-uploaded.txt"); code != http.StatusNoContent {
		t.Fatalf("delete missing key: %d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(queued) != 1 || queued[0].EventType != events.StorageDelete || !strings.Contains(string(queued[0].Body), `docs/a.txt`) {
		t.Fatalf("queued = %+v, want one storage.delete for docs/a.txt", queued)
	}
}
