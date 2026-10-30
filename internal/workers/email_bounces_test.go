package workers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/email"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type fakeFailedLister []email.FailedEmail

func (f fakeFailedLister) ListFailedEmails(context.Context, time.Time) ([]email.FailedEmail, error) {
	return f, nil
}

// TestEmailBounceSyncAndDripSkips runs the bounce sync and the onboarding
// worker's new skips as the gateway role (the worker's pool), against a
// database with migrations applied:
//
//	EVENTS_TEST_ADMIN_URL=postgres://postgres@localhost:5432/eurobase \
//	EVENTS_TEST_GATEWAY_URL=postgres://eurobase_gateway:localdev@localhost:5432/eurobase \
//	go test ./internal/workers/ -run TestEmailBounceSyncAndDripSkips
func TestEmailBounceSyncAndDripSkips(t *testing.T) {
	adminURL, gwURL := os.Getenv("EVENTS_TEST_ADMIN_URL"), os.Getenv("EVENTS_TEST_GATEWAY_URL")
	if adminURL == "" || gwURL == "" {
		t.Skip("EVENTS_TEST_ADMIN_URL / EVENTS_TEST_GATEWAY_URL not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	gw, err := pgxpool.New(ctx, gwURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gw.Close)

	sfx := fmt.Sprintf("%d", os.Getpid())
	bounced := "bounce-" + sfx + "@bounce-check.eu"
	unconfirmed := "unverified-" + sfx + "@bounce-check.eu"
	invited := "invited-" + sfx + "@bounce-check.eu"
	stranger := "end-user-" + sfx + "@bounce-check.eu"
	late := "late-" + sfx + "@bounce-check.eu"
	var bouncedID, unconfirmedID, lateID string
	if err := admin.QueryRow(ctx, `INSERT INTO platform_users (email, email_confirmed_at) VALUES ($1, now()) RETURNING id`, bounced).Scan(&bouncedID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO platform_users (email) VALUES ($1) RETURNING id`, unconfirmed).Scan(&unconfirmedID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO platform_users (email, email_confirmed_at) VALUES ($1, now()) RETURNING id`, late).Scan(&lateID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO platform_allowlist (email) VALUES ($1)`, invited); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = admin.Exec(c, `DELETE FROM email_suppressions WHERE email LIKE $1`, "%-"+sfx+"@bounce-check.eu")
		_, _ = admin.Exec(c, `DELETE FROM platform_users WHERE id = ANY($1)`, []string{bouncedID, unconfirmedID, lateID})
		_, _ = admin.Exec(c, `DELETE FROM platform_allowlist WHERE email = $1`, invited)
	})

	suppressed := func(addr string) bool {
		var ok bool
		if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM email_suppressions WHERE email = $1)`, addr).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}

	// Sync: platform account and allowlist entry recorded; an address
	// that is neither (e.g. a project's end user) and a reserved one not.
	lister := fakeFailedLister{
		{Recipient: bounced, Reason: "dropped: user unknown", At: time.Now()},
		{Recipient: invited, Reason: "dropped", At: time.Now()},
		{Recipient: stranger, Reason: "dropped"},
		{Recipient: "probe-" + sfx + "@example.com", Reason: "dropped"},
	}
	if n := syncEmailBounces(ctx, gw, lister, time.Now().Add(-time.Hour)); n != 2 {
		t.Fatalf("recorded %d, want 2", n)
	}
	if !suppressed(bounced) || !suppressed(invited) || suppressed(stranger) {
		t.Fatalf("suppressions: bounced=%v invited=%v stranger=%v", suppressed(bounced), suppressed(invited), suppressed(stranger))
	}

	// Onboarding worker (runs as the gateway role, like the worker pool).
	w := &SendDripEmailWorker{DBPool: gw}
	job := func(userID string, created time.Time) *river.Job[jobs.SendDripEmailArgs] {
		return &river.Job[jobs.SendDripEmailArgs]{
			JobRow: &rivertype.JobRow{CreatedAt: created},
			Args:   jobs.SendDripEmailArgs{UserID: userID, Step: 0},
		}
	}
	status := func(userID string) string {
		var s string
		_ = admin.QueryRow(ctx, `SELECT status FROM drip_email_sends WHERE user_id = $1 AND step = 0 ORDER BY sent_at DESC LIMIT 1`, userID).Scan(&s)
		return s
	}
	if err := w.Work(ctx, job(bouncedID, time.Now())); err != nil {
		t.Fatalf("bounced address: %v", err)
	}
	if got := status(bouncedID); got != "skipped_bounced" {
		t.Fatalf("bounced address status = %q, want skipped_bounced", got)
	}
	var snooze *river.JobSnoozeError
	if err := w.Work(ctx, job(unconfirmedID, time.Now())); !errors.As(err, &snooze) {
		t.Fatalf("unverified, recent signup: err = %v, want a snooze", err)
	}
	if got := status(unconfirmedID); got != "" {
		t.Fatalf("a snooze must not record anything, got %q", got)
	}
	if err := w.Work(ctx, job(unconfirmedID, time.Now().Add(-8*24*time.Hour))); err != nil {
		t.Fatalf("unverified, old signup: %v", err)
	}
	if got := status(unconfirmedID); got != "skipped_unconfirmed" {
		t.Fatalf("unverified status = %q, want skipped_unconfirmed", got)
	}

	// Verified, but step 0 runs after step 1 was already due (verification
	// came late): skipped, so the user doesn't get several at once.
	if err := w.Work(ctx, job(lateID, time.Now().Add(-5*24*time.Hour))); err != nil {
		t.Fatalf("late step: %v", err)
	}
	if got := status(lateID); got != "skipped_unconfirmed" {
		t.Fatalf("late step status = %q, want skipped_unconfirmed", got)
	}

	// A suppression not refreshed for 90 days expires.
	if _, err := admin.Exec(ctx, `UPDATE email_suppressions SET last_seen_at = now() - interval '91 days' WHERE email = $1`, bounced); err != nil {
		t.Fatal(err)
	}
	syncEmailBounces(ctx, gw, fakeFailedLister{}, time.Now())
	if suppressed(bounced) {
		t.Fatal("an expired suppression must be removed")
	}
	if _, err := admin.Exec(ctx, `INSERT INTO email_suppressions (email, reason) VALUES ($1, 'dropped')`, bounced); err != nil {
		t.Fatal(err)
	}

	// A row whose address no longer belongs to any account or invitation
	// is removed on the next pass.
	if _, err := admin.Exec(ctx, `DELETE FROM platform_allowlist WHERE email = $1`, invited); err != nil {
		t.Fatal(err)
	}
	syncEmailBounces(ctx, gw, fakeFailedLister{}, time.Now())
	if suppressed(invited) || !suppressed(bounced) {
		t.Fatalf("after cleanup: invited=%v bounced=%v", suppressed(invited), suppressed(bounced))
	}
}
