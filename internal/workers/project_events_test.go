package workers

// #845: webhook delivery and function-trigger invocation against a real
// database as eurobase_gateway (the worker's pool), with local stand-ins
// for the customer endpoint and the functions runner.
//
// EVENTS_TEST_GATEWAY_URL: a migrated database (scripts/db/apply-migrations.sh)
// as eurobase_gateway. Skipped when unset.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/cron"
	"github.com/eurobase/euroback/internal/events"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/eurobase/euroback/internal/netguard"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func eventsTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("EVENTS_TEST_GATEWAY_URL")
	if dsn == "" {
		t.Skip("EVENTS_TEST_GATEWAY_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// eventsTestProject creates a platform user + project; returns the
// project id and schema name.
func eventsTestProject(t *testing.T, pool *pgxpool.Pool) (string, string) {
	t.Helper()
	ctx := context.Background()
	u := strings.ReplaceAll(uuid.NewString()[:12], "-", "")
	var owner, project string
	if err := pool.QueryRow(ctx, `INSERT INTO public.platform_users (email) VALUES ($1) RETURNING id`, "events-"+u+"@test.invalid").Scan(&owner); err != nil {
		t.Fatal(err)
	}
	schema := "tenant_events_" + u
	if err := pool.QueryRow(ctx,
		`INSERT INTO public.projects (owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		 VALUES ($1, 'events test', $2, $3, $4, 'fr-par', 'pro', 'active') RETURNING id`,
		owner, "events-"+u, schema, "b-events-"+u).Scan(&project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.projects WHERE id = $1`, project)
		_, _ = pool.Exec(ctx, `DELETE FROM public.platform_users WHERE id = $1`, owner)
	})
	return project, schema
}

// receiver is the customer endpoint: a TLS server reachable as
// https://example.com (the httptest certificate covers example.com)
// through client, which dials it whatever the host.
type receiver struct {
	srv    *httptest.Server
	client *http.Client
	mu     sync.Mutex
	reqs   []receivedRequest
	status []int // answers in order; 200 after the list runs out
}

type receivedRequest struct {
	header http.Header
	body   []byte
	path   string
}

func newReceiver(t *testing.T, status ...int) *receiver {
	rc := &receiver{status: status}
	rc.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rc.mu.Lock()
		rc.reqs = append(rc.reqs, receivedRequest{header: r.Header.Clone(), body: b, path: r.URL.Path})
		code := http.StatusOK
		if len(rc.status) > 0 {
			code, rc.status = rc.status[0], rc.status[1:]
		}
		rc.mu.Unlock()
		if code == http.StatusFound {
			http.Redirect(w, r, "https://example.com/elsewhere", code)
			return
		}
		w.WriteHeader(code)
		fmt.Fprintf(w, "answer %d", code)
	}))
	t.Cleanup(rc.srv.Close)
	rc.client = rc.srv.Client()
	tr := rc.client.Transport.(*http.Transport).Clone()
	addr := rc.srv.Listener.Addr().String()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	rc.client = &http.Client{Transport: tr, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return rc
}

func (rc *receiver) requests() []receivedRequest {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]receivedRequest(nil), rc.reqs...)
}

func insertWebhook(t *testing.T, pool *pgxpool.Pool, projectID, url string, enabled bool) (id, secret string) {
	t.Helper()
	secret = "whsec_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO public.webhooks (project_id, url, events, secret, enabled) VALUES ($1, $2, '{db.insert}', $3, $4) RETURNING id`,
		projectID, url, secret, enabled).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id, secret
}

func webhookJob(webhookID string, attempt int, env events.Envelope) *river.Job[jobs.DeliverProjectWebhookArgs] {
	body, _ := json.Marshal(env)
	return &river.Job[jobs.DeliverProjectWebhookArgs]{
		JobRow: &rivertype.JobRow{Attempt: attempt},
		Args:   jobs.DeliverProjectWebhookArgs{WebhookID: webhookID, EventID: env.ID, EventType: env.Type, Body: body},
	}
}

func testEnvelope(projectID string) events.Envelope {
	return events.Envelope{
		ID: uuid.NewString(), Type: events.DBInsert, CreatedAt: time.Now().UTC().Truncate(time.Second),
		ProjectID: projectID, Table: "todos", Data: map[string]any{"id": "r1", "title": "hello"},
	}
}

type deliveryRow struct {
	status   *int
	response string
	attempts int
	success  bool
	count    int
}

func readDelivery(t *testing.T, pool *pgxpool.Pool, webhookID, eventID string) deliveryRow {
	t.Helper()
	var d deliveryRow
	err := pool.QueryRow(context.Background(),
		`SELECT count(*) OVER (), status_code, COALESCE(response, ''), attempts, success
		   FROM public.webhook_deliveries WHERE webhook_id = $1 AND event_id = $2`, webhookID, eventID,
	).Scan(&d.count, &d.status, &d.response, &d.attempts, &d.success)
	if err != nil {
		t.Fatalf("read delivery: %v", err)
	}
	return d
}

func TestDeliverProjectWebhook(t *testing.T) {
	pool := eventsTestPool(t)
	ctx := context.Background()
	projectID, _ := eventsTestProject(t, pool)

	t.Run("signed delivery, recorded", func(t *testing.T) {
		rc := newReceiver(t)
		hookID, secret := insertWebhook(t, pool, projectID, "https://example.com/hooks/eurobase", true)
		env := testEnvelope(projectID)
		job := webhookJob(hookID, 1, env)
		w := &DeliverProjectWebhookWorker{Pool: pool, Client: rc.client}
		if err := w.Work(ctx, job); err != nil {
			t.Fatalf("Work: %v", err)
		}
		reqs := rc.requests()
		if len(reqs) != 1 || reqs[0].path != "/hooks/eurobase" {
			t.Fatalf("requests = %+v", reqs)
		}
		r := reqs[0]
		// Verified exactly as the docs' Node example does: HMAC-SHA256
		// over the raw body with the secret string as the key, hex.
		if got, want := r.header.Get("X-Eurobase-Signature"), events.SignWebhookBody(secret, r.body); got != want || len(got) != 64 {
			t.Fatalf("signature %q, want %q", got, want)
		}
		if r.header.Get("X-Eurobase-Event") != "db.insert" || r.header.Get("X-Eurobase-Delivery") != env.ID || r.header.Get("Content-Type") != "application/json" {
			t.Fatalf("headers = %v", r.header)
		}
		var got events.Envelope
		if err := json.Unmarshal(r.body, &got); err != nil {
			t.Fatal(err)
		}
		if got.Type != "db.insert" || got.Data["title"] != "hello" || got.ID != env.ID || got.Table != "todos" {
			t.Fatalf("body = %s", r.body)
		}
		d := readDelivery(t, pool, hookID, env.ID)
		if !d.success || d.status == nil || *d.status != 200 || d.attempts != 1 || d.response != "answer 200" {
			t.Fatalf("delivery row = %+v", d)
		}
	})

	t.Run("failure retried, one row per event", func(t *testing.T) {
		rc := newReceiver(t, http.StatusInternalServerError)
		hookID, _ := insertWebhook(t, pool, projectID, "https://example.com/h", true)
		env := testEnvelope(projectID)
		w := &DeliverProjectWebhookWorker{Pool: pool, Client: rc.client}
		err := w.Work(ctx, webhookJob(hookID, 1, env))
		var cancel *rivertype.JobCancelError
		if err == nil || errors.As(err, &cancel) {
			t.Fatalf("Work on a 500 = %v, want a retryable error", err)
		}
		if d := readDelivery(t, pool, hookID, env.ID); d.success || d.status == nil || *d.status != 500 || d.attempts != 1 {
			t.Fatalf("after attempt 1: %+v", d)
		}
		if err := w.Work(ctx, webhookJob(hookID, 2, env)); err != nil {
			t.Fatalf("attempt 2: %v", err)
		}
		if d := readDelivery(t, pool, hookID, env.ID); !d.success || *d.status != 200 || d.attempts != 2 || d.count != 1 {
			t.Fatalf("after attempt 2: %+v", d)
		}
	})

	t.Run("redirect not followed", func(t *testing.T) {
		rc := newReceiver(t, http.StatusFound)
		hookID, _ := insertWebhook(t, pool, projectID, "https://example.com/r", true)
		env := testEnvelope(projectID)
		if err := (&DeliverProjectWebhookWorker{Pool: pool, Client: rc.client}).Work(ctx, webhookJob(hookID, 1, env)); err == nil {
			t.Fatal("a 302 counted as delivered")
		}
		if n := len(rc.requests()); n != 1 {
			t.Fatalf("requests = %d, want 1 (redirect not followed)", n)
		}
	})

	t.Run("disabled or deleted webhook: nothing sent", func(t *testing.T) {
		rc := newReceiver(t)
		hookID, _ := insertWebhook(t, pool, projectID, "https://example.com/d", false)
		w := &DeliverProjectWebhookWorker{Pool: pool, Client: rc.client}
		if err := w.Work(ctx, webhookJob(hookID, 1, testEnvelope(projectID))); err != nil {
			t.Fatalf("disabled: %v", err)
		}
		if err := w.Work(ctx, webhookJob(uuid.NewString(), 1, testEnvelope(projectID))); err != nil {
			t.Fatalf("deleted: %v", err)
		}
		if n := len(rc.requests()); n != 0 {
			t.Fatalf("requests = %d, want 0", n)
		}
	})

	t.Run("refused URL: cancelled, recorded", func(t *testing.T) {
		rc := newReceiver(t)
		for _, u := range []string{"http://example.com/plain", "https://localhost/x", "https://10.0.0.5/x", "https://user:pw@example.com/x"} {
			hookID, _ := insertWebhook(t, pool, projectID, u, true)
			env := testEnvelope(projectID)
			err := (&DeliverProjectWebhookWorker{Pool: pool, Client: rc.client}).Work(ctx, webhookJob(hookID, 1, env))
			var cancel *rivertype.JobCancelError
			if !errors.As(err, &cancel) {
				t.Fatalf("%s: err = %v, want JobCancel", u, err)
			}
			if d := readDelivery(t, pool, hookID, env.ID); d.success || d.response == "" {
				t.Fatalf("%s: row = %+v", u, d)
			}
		}
		if n := len(rc.requests()); n != 0 {
			t.Fatalf("requests = %d, want 0", n)
		}
	})
}

// The production client never connects to a non-public address, even
// for a URL that passed CheckWebhookURL's name check.
func TestWebhookDeliveryClientRefusesInternalAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("internal server reached")
	}))
	defer srv.Close()
	c := NewWebhookDeliveryClient()
	for _, u := range []string{srv.URL, strings.Replace(srv.URL, "127.0.0.1", "localhost", 1), "http://169.254.169.254/latest/meta-data"} {
		resp, err := c.Get(u)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("%s: connected", u)
		}
		if !errors.Is(err, netguard.ErrNotPublic) {
			t.Fatalf("%s: err = %v, want netguard.ErrNotPublic", u, err)
		}
	}
}

func TestInvokeFunctionTrigger(t *testing.T) {
	pool := eventsTestPool(t)
	ctx := context.Background()
	projectID, schema := eventsTestProject(t, pool)

	var (
		mu      sync.Mutex
		calls   []receivedRequest
		answers []int
	)
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, receivedRequest{header: r.Header.Clone(), body: b, path: r.URL.Path})
		code := 200
		if len(answers) > 0 {
			code, answers = answers[0], answers[1:]
		}
		mu.Unlock()
		w.WriteHeader(code)
	}))
	defer runner.Close()
	inv := &cron.FunctionInvoker{RunnerURL: runner.URL, Client: runner.Client()}

	var fnID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO public.edge_functions (project_id, name, code, status, version) VALUES ($1, 'on-todo', 'export default () => {}', 'active', 3) RETURNING id`,
		projectID).Scan(&fnID); err != nil {
		t.Fatal(err)
	}
	var trigID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO public.function_triggers (function_id, project_id, table_name, events) VALUES ($1, $2, 'todos', '{INSERT}') RETURNING id`,
		fnID, projectID).Scan(&trigID); err != nil {
		t.Fatal(err)
	}
	env := testEnvelope(projectID)
	body, _ := json.Marshal(env)
	job := &river.Job[jobs.InvokeFunctionTriggerArgs]{JobRow: &rivertype.JobRow{Attempt: 1},
		Args: jobs.InvokeFunctionTriggerArgs{TriggerID: trigID, EventID: env.ID, Body: body}}
	w := &InvokeFunctionTriggerWorker{Pool: pool, Invoker: inv}

	if err := w.Work(ctx, job); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("runner calls = %d", len(calls))
	}
	c := calls[0]
	want := map[string]string{
		"X-Project-ID": projectID, "X-Schema-Name": schema, "X-Function-Name": "on-todo", "X-Function-ID": fnID,
		"X-Function-Version": "3", "X-Plan": "pro", "X-Function-Trigger-ID": trigID, "X-Eurobase-Delivery": env.ID,
	}
	for k, v := range want {
		if got := c.header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if c.path != "/invoke" || string(c.body) != string(body) {
		t.Fatalf("path %s body %s", c.path, c.body)
	}

	mu.Lock()
	answers = []int{500, 400}
	mu.Unlock()
	var cancel *rivertype.JobCancelError
	if err := w.Work(ctx, job); err == nil || errors.As(err, &cancel) {
		t.Fatalf("runner 500: err = %v, want retryable", err)
	}
	if err := w.Work(ctx, job); !errors.As(err, &cancel) {
		t.Fatalf("runner 400: err = %v, want JobCancel", err)
	}

	// Disabled trigger or inactive function: not invoked.
	_, _ = pool.Exec(ctx, `UPDATE public.function_triggers SET enabled = false WHERE id = $1`, trigID)
	before := len(calls)
	if err := w.Work(ctx, job); err != nil || len(calls) != before {
		t.Fatalf("disabled trigger: err %v, calls %d → %d", err, before, len(calls))
	}
	_, _ = pool.Exec(ctx, `UPDATE public.function_triggers SET enabled = true WHERE id = $1`, trigID)
	_, _ = pool.Exec(ctx, `UPDATE public.edge_functions SET status = 'disabled' WHERE id = $1`, fnID)
	if err := w.Work(ctx, job); err != nil || len(calls) != before {
		t.Fatalf("inactive function: err %v, calls %d → %d", err, before, len(calls))
	}

	// No runner configured: cancelled, not retried.
	_, _ = pool.Exec(ctx, `UPDATE public.edge_functions SET status = 'active' WHERE id = $1`, fnID)
	if err := (&InvokeFunctionTriggerWorker{Pool: pool}).Work(ctx, job); !errors.As(err, &cancel) {
		t.Fatalf("no runner: err = %v, want JobCancel", err)
	}
}
