package sqllog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The SQL log under the production roles (000139): the gateway may only
// insert; the developer pool lists and cleans up.
//
// Needs a database migrated by scripts/db/apply-migrations.sh:
//
//	SQL_LOG_TEST_ADMIN_URL=postgres://postgres@localhost:5432/eurobase \
//	SQL_LOG_TEST_GATEWAY_URL=postgres://eurobase_gateway:localdev@localhost:5432/eurobase \
//	SQL_LOG_TEST_DEVELOPER_URL=postgres://eurobase_developer:localdev@localhost:5432/eurobase \
//	go test ./internal/sqllog/ -run SQLLog
func TestSQLLog_ProductionRoles(t *testing.T) {
	adminURL, gwURL, devURL := os.Getenv("SQL_LOG_TEST_ADMIN_URL"), os.Getenv("SQL_LOG_TEST_GATEWAY_URL"), os.Getenv("SQL_LOG_TEST_DEVELOPER_URL")
	if adminURL == "" || gwURL == "" || devURL == "" {
		t.Skip("SQL_LOG_TEST_{ADMIN,GATEWAY,DEVELOPER}_URL not set")
	}
	ctx := context.Background()
	open := func(u string) *pgxpool.Pool {
		p, err := pgxpool.New(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	admin, gw, dev := open(adminURL), open(gwURL), open(devURL)

	b := make([]byte, 6)
	_, _ = rand.Read(b)
	suffix := hex.EncodeToString(b)
	projectID := "5e1a0c9d-0000-4000-8000-" + suffix
	if _, err := admin.Exec(ctx, `INSERT INTO platform_users (id, email) VALUES ($1, $2)`, projectID, "sqllog-"+suffix+"@test.local"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		VALUES ($1, $1, 'sqllog', $2, $3, $4, 'fr-par', 'free', 'active')`,
		projectID, "sqllog-"+suffix, "tenant_sqllog_"+suffix, "b-sqllog-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = admin.Exec(context.Background(), `DELETE FROM platform_users WHERE id = $1`, projectID)
	})

	// The gateway writes (Record swallows errors, so check the rows).
	l := New(gw)
	l.Record(ctx, projectID, "SELECT * FROM todos", Entry{
		ActorID: projectID, ActorEmail: "dev@example.com", Via: ViaConsole, Source: SourceSQL,
		Outcome: OutcomeOK, DurationMs: Ms(0), RowCount: Int(3), IP: "203.0.113.7",
	})
	l.Record(ctx, projectID, "DO $$ BEGIN PERFORM 1; END $$\x00", Entry{
		ActorID: projectID, PATID: "22222222-2222-4222-8222-222222222222", Via: ViaToken, Source: SourceSQL,
		Outcome: OutcomeRefused, Detail: "refused by a check",
	})
	l.Record(ctx, projectID, strings.Repeat("x", MaxStatementLen+100), Entry{Via: ViaConsole, Source: SourceMigration, Outcome: OutcomeError, Detail: "syntax error"})

	// …but can't read, change or delete.
	for _, q := range []string{
		`SELECT count(*) FROM public.platform_sql_log`,
		`SELECT last_value FROM public.platform_sql_log_id_seq`,
		`UPDATE public.platform_sql_log SET detail = 'x'`,
		`DELETE FROM public.platform_sql_log`,
	} {
		if _, err := gw.Exec(ctx, q); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("gateway %q: want permission denied, got %v", q, err)
		}
	}

	// The developer pool lists, newest first.
	entries, err := List(ctx, dev, projectID, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("want 3 entries, got %d: %+v", len(entries), entries)
	}
	if e := entries[0]; e.Source != SourceMigration || e.StatementLen != MaxStatementLen+100 || len(e.Statement) > MaxStatementLen+len("…") {
		t.Errorf("long entry: source %q len %d stored %d", e.Source, e.StatementLen, len(e.Statement))
	}
	if e := entries[1]; e.Via != ViaToken || e.PATID != "22222222-2222-4222-8222-222222222222" || e.Outcome != OutcomeRefused || !strings.Contains(e.Statement, `\0`) {
		t.Errorf("refused entry: %+v", e)
	}
	if e := entries[2]; e.ActorEmail != "dev@example.com" || e.RowCount == nil || *e.RowCount != 3 || e.IP != "203.0.113.7" || len(e.SHA256) != 64 {
		t.Errorf("ok entry: %+v", e)
	}

	// Filters: outcome, and keyset pagination.
	refused, err := List(ctx, dev, projectID, ListOptions{Outcome: OutcomeRefused})
	if err != nil || len(refused) != 1 {
		t.Errorf("outcome filter: %v %+v", err, refused)
	}
	older, err := List(ctx, dev, projectID, ListOptions{BeforeID: entries[0].ID, Limit: 1})
	if err != nil || len(older) != 1 || older[0].ID != entries[1].ID {
		t.Errorf("pagination: %v %+v", err, older)
	}
	all, err := ListAll(ctx, dev, ListOptions{Outcome: OutcomeRefused, Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range all {
		found = found || (e.ProjectID == projectID && e.Outcome == OutcomeRefused)
	}
	if !found {
		t.Error("ListAll should include this project's refused entry")
	}

	// Cleanup removes only what's past retention.
	if _, err := admin.Exec(ctx, `UPDATE public.platform_sql_log SET created_at = now() - interval '31 days'
		WHERE project_id = $1 AND source = 'migration'`, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := Cleanup(ctx, dev); err != nil {
		t.Fatal(err)
	}
	entries, err = List(ctx, dev, projectID, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("after cleanup: %+v", entries)
	}
}
