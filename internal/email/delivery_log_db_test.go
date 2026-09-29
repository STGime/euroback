package email

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The email log under the production roles (000135): the gateway (SDK
// auth requests) may only insert; the developer pool lists and cleans up.
//
// Needs a database migrated by scripts/db/apply-migrations.sh:
//
//	EMAIL_LOG_TEST_ADMIN_URL=postgres://postgres@localhost:5432/eurobase \
//	EMAIL_LOG_TEST_GATEWAY_URL=postgres://eurobase_gateway:localdev@localhost:5432/eurobase \
//	EMAIL_LOG_TEST_DEVELOPER_URL=postgres://eurobase_developer:localdev@localhost:5432/eurobase \
//	go test ./internal/email/ -run EmailLog
func TestEmailLog_ProductionRoles(t *testing.T) {
	adminURL, gwURL, devURL := os.Getenv("EMAIL_LOG_TEST_ADMIN_URL"), os.Getenv("EMAIL_LOG_TEST_GATEWAY_URL"), os.Getenv("EMAIL_LOG_TEST_DEVELOPER_URL")
	if adminURL == "" || gwURL == "" || devURL == "" {
		t.Skip("EMAIL_LOG_TEST_{ADMIN,GATEWAY,DEVELOPER}_URL not set")
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
	if _, err := admin.Exec(ctx, `INSERT INTO platform_users (id, email) VALUES ($1, $2)`, projectID, "emaillog-"+suffix+"@test.local"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		VALUES ($1, $1, 'emaillog', $2, $3, $4, 'fr-par', 'free', 'active')`,
		projectID, "emaillog-"+suffix, "tenant_emaillog_"+suffix, "b-emaillog-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = admin.Exec(context.Background(), `DELETE FROM platform_users WHERE id = $1`, projectID)
	})

	// The gateway writes (Record swallows errors, so check the row).
	NewDeliveryLog(gw).Record(ctx, projectID, DeliveryLogEntry{
		Flow: FlowMagicLink, Recipient: "petra@perweg.de", Outcome: OutcomeSkipped, Reason: ReasonNoUser,
	})
	NewDeliveryLog(gw).Record(ctx, projectID, DeliveryLogEntry{
		Flow: FlowPasswordReset, Recipient: "a@b.io", Outcome: OutcomeSent, Via: ViaPlatform, MessageID: "tem-1",
	})

	// …but can't read, change or delete.
	for _, q := range []string{
		`SELECT count(*) FROM public.project_email_log`,
		`UPDATE public.project_email_log SET detail = 'x'`,
		`DELETE FROM public.project_email_log`,
	} {
		if _, err := gw.Exec(ctx, q); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("gateway %q: want permission denied, got %v", q, err)
		}
	}

	// The developer pool lists, newest first, masked.
	entries, err := ListDeliveryLog(ctx, dev, projectID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(entries), entries)
	}
	if e := entries[1]; e.Recipient != "p****@perweg.de" || e.Reason != ReasonNoUser || e.Outcome != OutcomeSkipped {
		t.Errorf("skipped entry: %+v", e)
	}
	if e := entries[0]; e.Via != ViaPlatform || e.MessageID != "tem-1" {
		t.Errorf("sent entry: %+v", e)
	}

	// Cleanup removes only what's past retention.
	if _, err := admin.Exec(ctx, `UPDATE public.project_email_log SET created_at = now() - interval '15 days'
		WHERE project_id = $1 AND flow = 'magic_link'`, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := CleanupDeliveryLog(ctx, dev); err != nil {
		t.Fatal(err)
	}
	entries, err = ListDeliveryLog(ctx, dev, projectID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Flow != FlowPasswordReset {
		t.Errorf("after cleanup: %+v", entries)
	}
}
