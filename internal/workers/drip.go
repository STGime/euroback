package workers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/eurobase/euroback/internal/email"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// SendDripEmailWorker fires one step of the onboarding drip series
// (Phase C of the public-beta launch plan).
//
// Runs in the worker pod (cmd/worker), triggered by River jobs
// scheduled at signupTime + step*OnboardingIntervalDays. On every
// invocation:
//
//  1. Load the user's email + display name + (optionally) first
//     project name from the DB.
//  2. Check mailing_preferences for the user — if they've opted out
//     of 'onboarding' or 'all', write a 'skipped_opt_out' row to
//     drip_email_sends and return nil (no retry, this is terminal).
//  3. Check drip_email_sends for an existing terminal row for
//     (user, step). If one exists, return nil (idempotent — River
//     shouldn't re-fire but defence in depth against a manual
//     re-enqueue or a lost ack).
//  4. Render the template via email.RenderOnboardingStep.
//  5. Send via emailService.SendRaw (goes through TEM; no
//     per-project routing — this mail is from the platform to the
//     tenant, not from the tenant to their end-users).
//  6. Write a 'sent' row to drip_email_sends. On send failure,
//     write a 'failed' row + return the error so River retries.
//
// The worker never touches the tenant schema — this is
// platform-to-tenant mail.
type SendDripEmailWorker struct {
	river.WorkerDefaults[jobs.SendDripEmailArgs]
	DBPool     *pgxpool.Pool
	Emails     *email.EmailService
	Signer     *email.UnsubscribeSigner
	BaseURL    string // absolute URL to the platform gateway, e.g. https://api.eurobase.app
	ConsoleURL string // absolute URL to the console, for the footer link
	DocsURL    string // absolute URL to the in-console docs
}

// Work executes one drip step. See the type doc for the flow.
func (w *SendDripEmailWorker) Work(ctx context.Context, job *river.Job[jobs.SendDripEmailArgs]) error {
	args := job.Args
	logger := slog.With("user_id", args.UserID, "step", args.Step)

	// ── Load user ──
	var (
		userEmail   string
		displayName *string
		projectName *string
	)
	err := w.DBPool.QueryRow(ctx,
		`SELECT u.email, u.display_name,
		        (SELECT p.name
		           FROM projects p
		           JOIN project_members pm ON pm.project_id = p.id
		          WHERE pm.user_id = u.id
		          ORDER BY p.created_at ASC
		          LIMIT 1) AS first_project
		 FROM platform_users u
		 WHERE u.id = $1`,
		args.UserID,
	).Scan(&userEmail, &displayName, &projectName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// User deleted between enqueue + fire — nothing to do.
			// Log at debug because it's an expected outcome for
			// deleted-account users still holding scheduled jobs.
			logger.Debug("drip skip — user gone")
			return nil
		}
		return fmt.Errorf("load user: %w", err)
	}

	// ── Opt-out check ──
	var optedOut bool
	err = w.DBPool.QueryRow(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM mailing_preferences
		    WHERE user_id = $1
		      AND category IN ('onboarding', 'all')
		      AND opted_out_at IS NOT NULL
		 )`,
		args.UserID,
	).Scan(&optedOut)
	if err != nil {
		return fmt.Errorf("check opt-out: %w", err)
	}
	if optedOut {
		logger.Info("drip skip — user opted out")
		return w.recordSend(ctx, args.UserID, args.Step, "skipped_opt_out", "")
	}

	// ── Idempotency guard ──
	// If a terminal row already exists for (user, step), don't
	// re-send. This defends against manual re-enqueue AND against
	// a River worker crash between step 5 (send) and step 6
	// (record) — the re-run would double-send otherwise. Loser of
	// a race between two concurrent workers gets the UNIQUE
	// (user_id, step, status) violation and returns nil.
	var already bool
	err = w.DBPool.QueryRow(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM drip_email_sends
		    WHERE user_id = $1
		      AND step = $2
		      AND status IN ('sent', 'skipped_opt_out')
		 )`,
		args.UserID, args.Step,
	).Scan(&already)
	if err != nil {
		return fmt.Errorf("check idempotency: %w", err)
	}
	if already {
		logger.Info("drip skip — already handled for (user, step)")
		return nil
	}

	// ── Render + send ──
	data := email.OnboardingData{
		UserEmail:      userEmail,
		DisplayName:    stringOrEmpty(displayName),
		ProjectName:    stringOrEmpty(projectName),
		UnsubscribeURL: email.BuildUnsubscribeURL(w.Signer, w.BaseURL, args.UserID, "onboarding"),
		DocsURL:        w.DocsURL,
		ConsoleURL:     w.ConsoleURL,
	}
	subject, body, err := email.RenderOnboardingStep(args.Step, data)
	if err != nil {
		// Template error — record as failed but DON'T retry (a bad
		// template will fail identically on the next attempt).
		// Returning nil confirms the job to River.
		logger.Error("drip render failed", "error", err)
		_ = w.recordSend(ctx, args.UserID, args.Step, "failed", err.Error())
		return nil
	}
	if err := w.Emails.SendRaw(ctx, userEmail, subject, body); err != nil {
		// Transient failure — let River retry. Also record the
		// failure so a support-facing SQL query can see the last
		// error message without grepping worker logs. Duplicate-key
		// on the UNIQUE (user_id, step, status='failed') is fine —
		// the ON CONFLICT below turns it into an UPDATE.
		logger.Warn("drip send failed", "error", err)
		if recErr := w.recordSend(ctx, args.UserID, args.Step, "failed", err.Error()); recErr != nil {
			logger.Debug("also failed to record", "error", recErr)
		}
		return err // triggers River retry
	}

	// ── Record success ──
	if err := w.recordSend(ctx, args.UserID, args.Step, "sent", ""); err != nil {
		// Send succeeded, record failed. The user got the email.
		// If we return the error, River retries, we double-send.
		// Log loudly and return nil.
		logger.Error("drip sent but record failed — will NOT retry to avoid double-send", "error", err)
		return nil
	}
	logger.Info("drip sent")
	return nil
}

// recordSend inserts a row in drip_email_sends. Uses ON CONFLICT so
// re-invocations (e.g. a 'failed' → 'failed' → 'sent' progression)
// update the row rather than colliding with the UNIQUE constraint.
func (w *SendDripEmailWorker) recordSend(ctx context.Context, userID string, step int, status, errMsg string) error {
	var errParam interface{}
	if errMsg != "" {
		errParam = errMsg
	}
	_, err := w.DBPool.Exec(ctx,
		`INSERT INTO drip_email_sends (user_id, step, status, error)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (user_id, step, status) DO UPDATE
		    SET error = EXCLUDED.error, sent_at = now()`,
		userID, step, status, errParam,
	)
	if err != nil {
		return fmt.Errorf("record drip send: %w", err)
	}
	return nil
}

// stringOrEmpty dereferences a *string, defaulting to "".
func stringOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
