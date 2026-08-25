package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/billing/mollie"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// HandleMollieWebhook receives Mollie's state-change POSTs. Body is
// application/x-www-form-urlencoded with a single `id` field; the
// canonical state is fetched by GET-ing the resource from Mollie
// (that's Mollie's trust model — the URL is a secret held only by
// us and Mollie, and unsigned POSTs from a third party fail closed
// at the GET step because Mollie doesn't recognise a foreign ID).
//
// Every terminal branch returns 200 — Mollie retries on non-2xx and
// we would rather log-and-continue than end up in a retry storm on
// a transient bug. Unrecoverable errors ARE logged with
// "billing.webhook.failed" so the log-based Grafana alert can fire.
func HandleMollieWebhook(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !svc.Enabled() {
			// Fail closed: Mollie shouldn't be pointing at a
			// disabled env, but if it is, returning 503 makes
			// Mollie retry — which turns "the flag is off" into
			// a webhook-queue backup. Return 200 + log.
			slog.Warn("billing.webhook.disabled_env_hit")
			w.WriteHeader(http.StatusOK)
			return
		}

		if err := r.ParseForm(); err != nil {
			slog.Warn("billing.webhook.parse_failed", "error", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		id := r.PostForm.Get("id")
		if id == "" {
			slog.Warn("billing.webhook.missing_id")
			w.WriteHeader(http.StatusOK)
			return
		}

		if err := svc.ProcessMollieWebhook(r.Context(), id); err != nil {
			// Every terminal error path also bumps a Prometheus
			// counter so the `BillingWebhookFailingSpike` alert
			// in deploy/k8s/cockpit/alerts.yaml can fire — the
			// slog line alone would be silent because we return
			// 200 to Mollie regardless (retry-storm avoidance).
			svc.incFailureMetric(resourceFromID(id))
			slog.Error("billing.webhook.failed",
				"id", id,
				"error", err,
			)
			// Still return 200. See docstring.
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

// resourceFromID classifies a Mollie ID into a metric label so
// dashboards can bisect "payment failing" from "subscription
// failing" without eyeballing raw IDs.
func resourceFromID(id string) string {
	switch {
	case strings.HasPrefix(id, "tr_"):
		return "payment"
	case strings.HasPrefix(id, "sub_"):
		return "subscription"
	default:
		return "unknown"
	}
}

// ProcessMollieWebhook dispatches a webhook by ID prefix, fetches
// the canonical resource state from Mollie, and applies the
// resulting state transition to our DB. Idempotent — the same
// {id} may arrive multiple times (Mollie retries, or two IDs can
// arrive concurrently for the same subscription); every write is
// upsert-shaped or guarded by a status check.
//
// ID prefixes documented at https://docs.mollie.com/reference/:
//
//   tr_   — Payment
//   sub_  — Subscription
//   re_   — Refund (not handled here; refunds are created by our
//                  own action in PR 8, not webhook-driven)
//
// Anything else is a no-op with a warn — resilient to new Mollie
// resource types added after us.
func (s *Service) ProcessMollieWebhook(ctx context.Context, id string) error {
	switch {
	case strings.HasPrefix(id, "tr_"):
		return s.processPaymentWebhook(ctx, id)
	case strings.HasPrefix(id, "sub_"):
		return s.processSubscriptionWebhook(ctx, id)
	default:
		slog.Warn("billing.webhook.unknown_resource_type", "id", id)
		return nil
	}
}

// processPaymentWebhook handles /v2/payments state changes. There
// are three paths that matter:
//
//   1. sequenceType=first + status=paid    → activate: create the
//      Mollie recurring subscription against the mandate captured
//      by the first payment, flip our subscriptions row to
//      'active', mark the invoice paid, flip projects.plan.
//   2. sequenceType=recurring + status=paid → renewal: insert a
//      new invoice row, bump subscriptions.next_charge_at.
//   3. status=failed on any recurring/first attempt → mark the
//      subscription past_due (idempotent: only sets past_due_since
//      on the first transition, subsequent same-signal writes are
//      no-ops).
//
// The other Mollie payment states (open, pending, canceled,
// expired, authorized) are logged and ignored — we react to
// terminal transitions only.
func (s *Service) processPaymentWebhook(ctx context.Context, paymentID string) error {
	payment, err := s.client.GetPayment(ctx, paymentID)
	if err != nil {
		if errors.Is(err, mollie.ErrNotFound) {
			// Foreign ID (someone POSTing our webhook with a
			// random tr_… value). Not an error — the safety net
			// worked exactly as designed.
			slog.Info("billing.webhook.payment_not_found_at_mollie", "id", paymentID)
			return nil
		}
		return fmt.Errorf("fetch payment %s: %w", paymentID, err)
	}

	// Observability: log every webhook entry with the fetched
	// payment status so silent-no-op branches (open / pending /
	// authorized) don't leave a debugging cliff. Added after the
	// 2026-08-16 new-project webhook debugging session — we saw
	// ingress hits with no gateway logs, took 20 min to work out
	// the status was "open". slog.Info because this is a routine
	// entry log, not a failure.
	slog.Info("billing.webhook.received",
		"id", payment.ID,
		"status", payment.Status,
		"sequence_type", payment.SequenceType,
		"has_pending_project_id", payment.Metadata["pending_project_id"] != "",
	)

	switch payment.Status {
	case "paid":
		if payment.SequenceType == mollie.SequenceTypeFirst {
			return s.activateFromFirstPayment(ctx, payment)
		}
		if payment.SequenceType == mollie.SequenceTypeRecurring {
			return s.recordRecurringPayment(ctx, payment)
		}
		// SequenceType=oneoff shouldn't happen for billing —
		// nothing in our flow creates oneoff payments. Log so we
		// spot a future refactor that accidentally introduces them.
		slog.Warn("billing.webhook.unexpected_paid_sequence", "id", payment.ID, "seq", payment.SequenceType)
		return nil

	case "failed", "canceled", "expired":
		return s.markPaymentFailure(ctx, payment)

	case "open", "pending", "authorized":
		// Non-terminal — Mollie will send another webhook when
		// the state resolves. No-op.
		return nil

	default:
		slog.Warn("billing.webhook.unknown_payment_status", "id", payment.ID, "status", payment.Status)
		return nil
	}
}

// activateFromFirstPayment is the critical transition: the user
// completed their first payment on Mollie's page, so a mandate
// now exists on the customer. We create the recurring Mollie
// subscription against that mandate, then flip our internal state
// atomically.
//
// Idempotent: if the subscription row is already 'active' with a
// mollie_subscription_id, we early-return. The CreateSubscription
// call itself is not idempotent (Mollie will happily create two
// subscriptions if we call twice), so the pre-check matters —
// belt-and-suspenders with the invoice paid_at guard below.
func (s *Service) activateFromFirstPayment(ctx context.Context, payment *mollie.Payment) error {
	// Branch on metadata shape. New-project checkouts (issue #406)
	// carry pending_project_id and no subscription_id — the project
	// (and therefore the subscription) doesn't exist yet, so we
	// create both from the pending_projects row. Existing-project
	// upgrades carry the classic subscription_id / project_id pair.
	if pendingID := payment.Metadata["pending_project_id"]; pendingID != "" {
		return s.activateNewProjectFromFirstPayment(ctx, payment, pendingID)
	}

	subscriptionID := payment.Metadata["subscription_id"]
	projectID := payment.Metadata["project_id"]
	planCode := payment.Metadata["plan_code"]
	if subscriptionID == "" || projectID == "" || planCode == "" {
		return fmt.Errorf("first-payment webhook missing metadata: %+v", payment.Metadata)
	}
	if payment.MandateID == "" {
		// Shouldn't happen — Mollie always populates mandateId
		// on a paid first payment. Guard so a hypothetical
		// Mollie regression doesn't silently strand us.
		return fmt.Errorf("first-payment webhook has no mandateId (payment %s)", payment.ID)
	}

	// Check current state before doing external work.
	var currentStatus string
	var existingMollieSub *string
	err := s.pool.QueryRow(ctx,
		`SELECT status, mollie_subscription_id
		   FROM public.subscriptions
		  WHERE id = $1`,
		subscriptionID,
	).Scan(&currentStatus, &existingMollieSub)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("subscription %s not found (payment %s)", subscriptionID, payment.ID)
	}
	if err != nil {
		return fmt.Errorf("load subscription %s: %w", subscriptionID, err)
	}
	if currentStatus == "active" && existingMollieSub != nil && *existingMollieSub != "" {
		// Already activated — likely a webhook retry. Just make
		// sure the invoice is marked paid (idempotent below).
		return s.markInvoicePaid(ctx, payment)
	}

	// Create the Mollie recurring subscription. Amount + interval
	// derive from our subscriptions row (source of truth is our
	// DB — Mollie's payment amount is just the first charge).
	var priceCents int
	err = s.pool.QueryRow(ctx,
		`SELECT price_cents FROM public.subscriptions WHERE id = $1`,
		subscriptionID,
	).Scan(&priceCents)
	if err != nil {
		return fmt.Errorf("read price_cents for %s: %w", subscriptionID, err)
	}

	mollieSub, err := s.client.CreateSubscription(ctx, payment.CustomerID, mollie.SubscriptionCreateRequest{
		Amount:      mollie.AmountFromCents(priceCents, "EUR"),
		Interval:    "1 month",
		Description: payment.Description,
		// Pin the recurring subscription to the mandate captured
		// by THIS first payment. Without this, Mollie picks the
		// most-recent-valid mandate, which is ambiguous once a
		// customer has cycled through cancel-and-resubscribe
		// (multiple mandates on file). See PR 4 review.
		MandateID:  payment.MandateID,
		WebhookURL: fmt.Sprintf("%s/platform/billing/webhook", s.config.WebhookBaseURL),
		Metadata: map[string]string{
			"subscription_id": subscriptionID,
			"project_id":      projectID,
			"plan_code":       planCode,
		},
	}, mollie.WithIdempotencyKey("activate:"+subscriptionID))
	if err != nil {
		return fmt.Errorf("create mollie subscription: %w", err)
	}

	// Flip state atomically. next_charge_at from Mollie's
	// nextPaymentDate string (YYYY-MM-DD) — parse defensively.
	nextChargeAt := parseMollieDate(mollieSub.NextPaymentDate)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`UPDATE public.subscriptions
		    SET mollie_subscription_id = $1,
		        status = 'active',
		        started_at = COALESCE(started_at, now()),
		        next_charge_at = $2
		  WHERE id = $3 AND status = 'incomplete'`,
		mollieSub.ID, nextChargeAt, subscriptionID,
	); err != nil {
		return fmt.Errorf("flip subscription to active: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE public.invoices
		    SET status = 'paid', paid_at = COALESCE(paid_at, now())
		  WHERE mollie_payment_id = $1`,
		payment.ID,
	); err != nil {
		return fmt.Errorf("mark invoice paid: %w", err)
	}

	// Also clear legacy_pro_grace_until (migration 000080).
	// A legacy-Pro user who completes checkout must fall out of
	// the console modal's detection rule (plan='pro' &&
	// legacy_pro_grace_until != NULL); without this clear, the
	// modal would keep nagging them after they've paid.
	if _, err := tx.Exec(ctx,
		`UPDATE public.projects
		    SET plan = $1,
		        legacy_pro_grace_until = NULL
		  WHERE id = $2`,
		planCode, projectID,
	); err != nil {
		return fmt.Errorf("flip project plan: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit activation: %w", err)
	}

	slog.Info("billing.webhook.activated",
		"subscription_id", subscriptionID,
		"project_id", projectID,
		"plan_code", planCode,
		"mollie_subscription_id", mollieSub.ID,
	)

	// Kick off PDF render in the background. Detached goroutine
	// so a slow render doesn't hold the Mollie response; the
	// download endpoint has an on-demand render fallback if
	// this fires before pdf_object_key is populated.
	if invoiceID := s.invoiceIDForPayment(ctx, payment.ID); invoiceID != "" {
		s.enqueueInvoiceRender(invoiceID)
	}
	return nil
}

// activateNewProjectFromFirstPayment is the payment-first sibling
// of activateFromFirstPayment: the user clicked Create Project on
// a paid plan, completed Mollie's checkout, and now we (a) create
// the project, (b) insert the subscription linked to it, (c)
// register the recurring Mollie subscription, (d) mark the invoice
// paid, (e) delete the pending_projects row. See issue #406.
//
// Idempotency: three overlapping guards.
//   - If we already inserted a subscription for this mollie_payment_id,
//     early-return (webhook retry after successful processing).
//   - The pending_projects row is deleted at the tail of the happy
//     path; if it's gone AND no subscription exists, the payment
//     landed after the pending row was swept — we refund via the
//     Mollie API and log for ops.
//   - CreateProjectForBilling itself is not idempotent; on retry
//     after a partial failure we'd get a slug-collision error which
//     surfaces as an ops-visible failure rather than a duplicate
//     project.
func (s *Service) activateNewProjectFromFirstPayment(ctx context.Context, payment *mollie.Payment, pendingID string) error {
	if payment.MandateID == "" {
		return fmt.Errorf("new-project first-payment webhook has no mandateId (payment %s)", payment.ID)
	}

	// Idempotency guard 1: has a subscription already been created
	// for this payment? Webhook retries after a successful
	// activation should be no-ops.
	var existingSubID string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM public.subscriptions WHERE mollie_payment_id = $1 LIMIT 1`,
		payment.ID,
	).Scan(&existingSubID)
	if err == nil {
		// Already processed. Make sure the invoice is marked paid.
		return s.markInvoicePaid(ctx, payment)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check existing subscription for payment %s: %w", payment.ID, err)
	}

	// Load the pending intent. If missing, the payment arrived
	// after the sweeper expired the row — refund and log.
	var (
		ownerID   string
		name      string
		slug      string
		region    string
		planCode  string
		userEmail string
	)
	err = s.pool.QueryRow(ctx,
		`SELECT pp.owner_id::text, pp.name, pp.slug, pp.region, pp.plan, pu.email
		   FROM public.pending_projects pp
		   JOIN public.platform_users pu ON pu.id = pp.owner_id
		  WHERE pp.id = $1`,
		pendingID,
	).Scan(&ownerID, &name, &slug, &region, &planCode, &userEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		slog.Error("billing.webhook.pending_project_missing_will_refund",
			"pending_project_id", pendingID,
			"mollie_payment_id", payment.ID,
		)
		s.refundOrphanedPayment(ctx, payment, "pending_project_expired")
		return ErrPendingProjectNotFound
	}
	if err != nil {
		return fmt.Errorf("load pending_project %s: %w", pendingID, err)
	}

	if s.projectCreator == nil {
		// Dev / test env where the projectCreator wasn't wired.
		// Refund is the safe move — we've taken money but can't
		// create the resource the customer paid for.
		slog.Error("billing.webhook.no_project_creator_will_refund",
			"pending_project_id", pendingID,
			"mollie_payment_id", payment.ID,
		)
		s.refundOrphanedPayment(ctx, payment, "project_creator_unavailable")
		return fmt.Errorf("no project creator configured — payment %s refunded", payment.ID)
	}

	// Resolve price BEFORE creating the project so a price-config
	// glitch doesn't leave us with an orphan project.
	priceCents, err := s.resolvePriceCents(ctx, planCode)
	if err != nil {
		s.refundOrphanedPayment(ctx, payment, "price_resolve_failed")
		return fmt.Errorf("resolve price for %s: %w", planCode, err)
	}

	// Find-or-create the project. This is the crash-retry
	// idempotency fix (#407 review 🟡 #2). CreateProject owns
	// its own tx and commits before the follow-up sub/invoice tx;
	// if we crash between the two, a retry that just called
	// CreateProject again would fail with 23505 on projects.slug
	// (globally UNIQUE per migration 000001) and trigger a refund
	// of a paid, live project. So look up first — if a project
	// with our slug already exists AND is owned by our user, it's
	// our prior attempt: adopt it and continue to the follow-up tx.
	// A different owner having the same slug is a genuine conflict
	// (should be extremely rare since we validate slug availability
	// pre-Mollie, but defensive path exists).
	var projectID string
	var existingOwner string
	err = s.pool.QueryRow(ctx,
		`SELECT id, owner_id::text FROM public.projects WHERE slug = $1`,
		slug,
	).Scan(&projectID, &existingOwner)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Fresh path — no project yet, create one.
		projectID, err = s.projectCreator.CreateProjectForBilling(ctx, ownerID, userEmail, name, slug, region, planCode)
		if err != nil {
			slog.Error("billing.webhook.create_project_failed_will_refund",
				"pending_project_id", pendingID,
				"mollie_payment_id", payment.ID,
				"error", err,
			)
			s.refundOrphanedPayment(ctx, payment, "provisioning_failed")
			return fmt.Errorf("create project: %w", err)
		}
	case err == nil:
		// Project already exists. Adopt ONLY when:
		//   (a) same owner (not adopting someone else's project), AND
		//   (b) no existing subscription (real crashed prior attempt
		//       of ours — an incomplete new-project checkout never
		//       reached the sub/invoice insert). See #407 re-review
		//       🟡: without (b), a user reusing a slug they already
		//       own would have their new payment bound to the OLD
		//       project, potentially adding a paid Pro sub to a Free
		//       project (since adopt skips CreateProject which sets
		//       plan='pro').
		//
		// (a) violation → real slug conflict → refund. Should be
		// impossible after the pre-Mollie slug check in
		// NewProjectCheckout, but defence-in-depth.
		// (b) violation → same-owner slug reuse that slipped past
		// the pre-Mollie check somehow (race with a concurrent
		// project creation, etc.) → refund. Better to hand money
		// back than to silently bill for a project the user
		// already has.
		if existingOwner != ownerID {
			slog.Error("billing.webhook.slug_owned_by_different_user_will_refund",
				"pending_project_id", pendingID,
				"mollie_payment_id", payment.ID,
				"slug", slug,
				"existing_owner", existingOwner,
				"pending_owner", ownerID,
			)
			s.refundOrphanedPayment(ctx, payment, "slug_conflict")
			return fmt.Errorf("slug %q owned by different user", slug)
		}
		var existingSubCount int
		if err := s.pool.QueryRow(ctx,
			`SELECT count(*) FROM public.subscriptions WHERE project_id = $1`,
			projectID,
		).Scan(&existingSubCount); err != nil {
			return fmt.Errorf("check subscriptions on adopt candidate: %w", err)
		}
		if existingSubCount > 0 {
			slog.Error("billing.webhook.adopt_target_has_subscription_will_refund",
				"pending_project_id", pendingID,
				"mollie_payment_id", payment.ID,
				"slug", slug,
				"project_id", projectID,
				"existing_sub_count", existingSubCount,
			)
			s.refundOrphanedPayment(ctx, payment, "adopt_target_not_incomplete")
			return fmt.Errorf("adopt candidate %s already has %d subscription row(s) — not a crashed-attempt shape", projectID, existingSubCount)
		}
		slog.Info("billing.webhook.adopted_existing_project_from_prior_attempt",
			"pending_project_id", pendingID,
			"project_id", projectID,
			"mollie_payment_id", payment.ID,
		)
	default:
		return fmt.Errorf("lookup existing project by slug: %w", err)
	}

	// Create the recurring Mollie subscription. If this fails,
	// we have a project but no recurring charge — the sweeper
	// (or a manual re-invocation) can fix. Log and continue so
	// the project + invoice are recorded regardless.
	mollieSub, msErr := s.client.CreateSubscription(ctx, payment.CustomerID, mollie.SubscriptionCreateRequest{
		Amount:      mollie.AmountFromCents(priceCents, "EUR"),
		Interval:    "1 month",
		Description: payment.Description,
		MandateID:   payment.MandateID,
		WebhookURL:  fmt.Sprintf("%s/platform/billing/webhook", s.config.WebhookBaseURL),
		Metadata: map[string]string{
			"project_id": projectID,
			"plan_code":  planCode,
		},
	}, mollie.WithIdempotencyKey("activate-new-project:"+pendingID))
	var mollieSubID string
	var nextChargeAt *time.Time
	if msErr != nil {
		slog.Error("billing.webhook.create_recurring_subscription_failed",
			"pending_project_id", pendingID,
			"project_id", projectID,
			"mollie_payment_id", payment.ID,
			"error", msErr,
		)
		// Continue — record the first-payment state so the user
		// sees Pro; a follow-up manual sweep re-tries the recurring
		// subscription creation.
	} else {
		mollieSubID = mollieSub.ID
		nextChargeAt = parseMollieDate(mollieSub.NextPaymentDate)
	}

	// Commit the follow-up state in one tx: subscription row,
	// invoice row, delete pending. Project row already committed.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var subscriptionID string
	err = tx.QueryRow(ctx,
		`INSERT INTO public.subscriptions
		    (project_id, mollie_customer_id, mollie_subscription_id, mollie_payment_id,
		     plan, price_cents, currency, billing_interval, status, started_at, next_charge_at)
		 VALUES ($1, $2, $3, $4, $5, $6, 'EUR', '1 month', 'active', now(), $7)
		 RETURNING id`,
		projectID, payment.CustomerID, nullIfEmpty(mollieSubID), payment.ID,
		planCode, priceCents, nextChargeAt,
	).Scan(&subscriptionID)
	if err != nil {
		return fmt.Errorf("insert subscription: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO public.invoices
		    (project_id, subscription_id, mollie_payment_id, amount_cents, currency, status, paid_at)
		 VALUES ($1, $2, $3, $4, 'EUR', 'paid', now())`,
		projectID, subscriptionID, payment.ID, priceCents,
	); err != nil {
		return fmt.Errorf("insert invoice: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM public.pending_projects WHERE id = $1`, pendingID,
	); err != nil {
		return fmt.Errorf("delete pending_project: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit new-project activation: %w", err)
	}

	slog.Info("billing.webhook.new_project_activated",
		"pending_project_id", pendingID,
		"project_id", projectID,
		"subscription_id", subscriptionID,
		"plan_code", planCode,
		"mollie_subscription_id", mollieSubID,
		"mollie_payment_id", payment.ID,
	)

	if invoiceID := s.invoiceIDForPayment(ctx, payment.ID); invoiceID != "" {
		s.enqueueInvoiceRender(invoiceID)
	}
	return nil
}

// refundOrphanedPayment issues a Mollie refund for a payment we
// took but can't fulfill (pending row expired, project creation
// failed, etc.). Best-effort — we log if the refund itself fails,
// since manual intervention is required for such cases anyway.
func (s *Service) refundOrphanedPayment(ctx context.Context, payment *mollie.Payment, reason string) {
	_, err := s.client.CreateRefund(ctx, payment.ID, mollie.RefundCreateRequest{
		Amount:      payment.Amount,
		Description: fmt.Sprintf("Auto-refund: %s (payment %s)", reason, payment.ID),
	}, mollie.WithIdempotencyKey("refund-orphan:"+payment.ID))
	if err != nil {
		slog.Error("billing.webhook.refund_orphan_failed",
			"mollie_payment_id", payment.ID,
			"reason", reason,
			"error", err,
		)
		return
	}
	slog.Warn("billing.webhook.refunded_orphan",
		"mollie_payment_id", payment.ID,
		"reason", reason,
	)
}

// nullIfEmpty returns a nullable pointer suitable for pgx to marshal
// as SQL NULL rather than empty string. Used for optional columns
// like mollie_subscription_id that may not be populated at insert
// time (recurring-subscription creation deferred to retry).
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// recordRecurringPayment handles the happy path of a renewal:
// insert an invoice row for the new charge, bump next_charge_at
// on the subscription. Idempotent — a duplicate webhook for the
// same payment ID does nothing beyond the INSERT ... ON CONFLICT
// / next_charge_at re-computation.
func (s *Service) recordRecurringPayment(ctx context.Context, payment *mollie.Payment) error {
	if payment.SubscriptionID == "" {
		return fmt.Errorf("recurring payment missing subscriptionId (payment %s)", payment.ID)
	}

	// Look up our subscription row via Mollie's ID.
	var subscriptionID, projectID string
	var priceCents int
	err := s.pool.QueryRow(ctx,
		`SELECT id, project_id, price_cents
		   FROM public.subscriptions
		  WHERE mollie_subscription_id = $1`,
		payment.SubscriptionID,
	).Scan(&subscriptionID, &projectID, &priceCents)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("no local subscription for mollie sub %s", payment.SubscriptionID)
	}
	if err != nil {
		return fmt.Errorf("load subscription: %w", err)
	}

	// Fetch canonical next_charge_at from Mollie (their
	// nextPaymentDate is authoritative — the client-side
	// "+1 month" arithmetic would drift on months of unequal
	// length).
	mollieSub, err := s.client.GetSubscription(ctx, payment.CustomerID, payment.SubscriptionID)
	if err != nil {
		// Non-fatal — the invoice insert is the important side
		// effect. Log and continue with next_charge_at unchanged.
		slog.Warn("billing.webhook.next_charge_refresh_failed",
			"subscription_id", subscriptionID, "error", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Insert invoice, ignoring duplicate delivery. subscription_id
	// linked so re-renders don't drift after cancel+resubscribe
	// (PR 6 review).
	tag, err := tx.Exec(ctx,
		`INSERT INTO public.invoices
		    (project_id, subscription_id, mollie_payment_id, amount_cents, currency, status, paid_at)
		 VALUES ($1, $2, $3, $4, 'EUR', 'paid', now())
		 ON CONFLICT (mollie_payment_id) DO NOTHING`,
		projectID, subscriptionID, payment.ID, priceCents,
	)
	if err != nil {
		return fmt.Errorf("insert recurring invoice: %w", err)
	}
	dupe := tag.RowsAffected() == 0

	// Clear past_due if a previous charge had failed and this
	// one succeeded (Mollie's automatic retry recovered).
	if _, err := tx.Exec(ctx,
		`UPDATE public.subscriptions
		    SET status = 'active',
		        past_due_since = NULL,
		        next_charge_at = COALESCE($1, next_charge_at)
		  WHERE id = $2 AND status IN ('active', 'past_due')`,
		func() *time.Time {
			if mollieSub != nil {
				t := parseMollieDate(mollieSub.NextPaymentDate)
				if t != nil {
					return t
				}
			}
			return nil
		}(),
		subscriptionID,
	); err != nil {
		return fmt.Errorf("clear past_due: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit recurring: %w", err)
	}

	if !dupe {
		slog.Info("billing.webhook.recurring_paid",
			"subscription_id", subscriptionID,
			"payment_id", payment.ID,
			"amount_cents", priceCents,
		)
		if invoiceID := s.invoiceIDForPayment(ctx, payment.ID); invoiceID != "" {
			s.enqueueInvoiceRender(invoiceID)
		}
	}
	return nil
}

// invoiceIDForPayment looks up our invoice UUID from Mollie's
// payment ID. Small helper used at both paid-transition sites
// (activate + recurring) to feed the async PDF render.
func (s *Service) invoiceIDForPayment(ctx context.Context, molliePaymentID string) string {
	var id string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM public.invoices WHERE mollie_payment_id = $1`,
		molliePaymentID,
	).Scan(&id)
	if err != nil {
		slog.Warn("billing: invoice lookup by mollie_payment_id failed",
			"mollie_payment_id", molliePaymentID, "error", err)
		return ""
	}
	return id
}

// markPaymentFailure flags the subscription past_due on the first
// failed charge. Mollie will retry twice more on its own schedule;
// each retry fires another webhook. Only the first transition
// sets past_due_since — the query below is a no-op on retries
// because it targets status='active'. PR 5's cron uses the aged
// past_due_since to time the 7-day grace before downgrade.
func (s *Service) markPaymentFailure(ctx context.Context, payment *mollie.Payment) error {
	if payment.SubscriptionID == "" {
		// A failed first payment (before mandate capture) doesn't
		// belong to a Mollie subscription yet — the user just
		// abandoned checkout. Mark the local 'incomplete' row
		// canceled so the retry guard (service.go: status IN
		// ('incomplete','active','past_due')) lets them try again
		// immediately. We keep the row rather than delete so
		// invoices.subscription_id (FK ON DELETE SET NULL) retains
		// the audit link back to what happened.
		slog.Info("billing.webhook.first_payment_failed", "id", payment.ID)
		if _, err := s.pool.Exec(ctx,
			`UPDATE public.invoices
			    SET status = 'failed'
			  WHERE mollie_payment_id = $1`,
			payment.ID,
		); err != nil {
			return fmt.Errorf("mark first invoice failed: %w", err)
		}
		if subID := payment.Metadata["subscription_id"]; subID != "" {
			if _, err := s.pool.Exec(ctx,
				`UPDATE public.subscriptions
				    SET status = 'canceled',
				        canceled_at = now()
				  WHERE id = $1
				    AND status = 'incomplete'`,
				subID,
			); err != nil {
				slog.Warn("billing.webhook.subscription_cancel_on_abandonment_failed",
					"subscription_id", subID,
					"mollie_payment_id", payment.ID,
					"error", err,
				)
			} else {
				slog.Info("billing.webhook.subscription_canceled_on_abandonment",
					"subscription_id", subID,
					"mollie_payment_id", payment.ID,
				)
			}
		}
		// Also clean up any pending_project row this failed payment
		// belonged to (#407 review 🟡 #3). Without this, an
		// abandoned Pro checkout leaves the pending row with a
		// mollie_payment_id set, and the sweeper's stale-resolved
		// branch would log a false "Mollie took a payment we lost"
		// warning every hour until expiry. Abandonment is normal
		// user behaviour, not a webhook delivery failure.
		if pendingID := payment.Metadata["pending_project_id"]; pendingID != "" {
			if _, err := s.pool.Exec(ctx,
				`DELETE FROM public.pending_projects WHERE id = $1`,
				pendingID,
			); err != nil {
				slog.Warn("billing.webhook.pending_cleanup_on_failure_failed",
					"pending_project_id", pendingID,
					"mollie_payment_id", payment.ID,
					"error", err,
				)
			} else {
				slog.Info("billing.webhook.pending_project_cleaned_on_abandonment",
					"pending_project_id", pendingID,
					"mollie_payment_id", payment.ID,
				)
			}
		}
		return nil
	}

	res, err := s.pool.Exec(ctx,
		`UPDATE public.subscriptions
		    SET status = 'past_due',
		        past_due_since = now()
		  WHERE mollie_subscription_id = $1
		    AND status = 'active'`,
		payment.SubscriptionID,
	)
	if err != nil {
		return fmt.Errorf("mark past_due: %w", err)
	}

	if _, err := s.pool.Exec(ctx,
		`UPDATE public.invoices
		    SET status = 'failed'
		  WHERE mollie_payment_id = $1`,
		payment.ID,
	); err != nil {
		return fmt.Errorf("mark invoice failed: %w", err)
	}

	if res.RowsAffected() > 0 {
		slog.Warn("billing.webhook.past_due",
			"mollie_subscription_id", payment.SubscriptionID,
			"payment_id", payment.ID,
		)
	}
	return nil
}

// processSubscriptionWebhook handles /v2/subscriptions state
// changes. Mollie fires this when the subscription is canceled or
// completes its fixed term. Eurobase subscriptions have no fixed
// term (Times is unset), so "completed" is rare — but handle it
// for defensive completeness.
func (s *Service) processSubscriptionWebhook(ctx context.Context, subscriptionID string) error {
	// Look up the customer first so we can hit the correct API
	// path (Mollie's subscription GET is namespaced under a
	// customer). Our subscriptions row carries mollie_customer_id.
	var customerID string
	err := s.pool.QueryRow(ctx,
		`SELECT mollie_customer_id
		   FROM public.subscriptions
		  WHERE mollie_subscription_id = $1`,
		subscriptionID,
	).Scan(&customerID)
	if errors.Is(err, pgx.ErrNoRows) {
		slog.Info("billing.webhook.subscription_not_local", "id", subscriptionID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("load mollie_customer_id for sub %s: %w", subscriptionID, err)
	}

	mollieSub, err := s.client.GetSubscription(ctx, customerID, subscriptionID)
	if err != nil {
		if errors.Is(err, mollie.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("fetch subscription %s: %w", subscriptionID, err)
	}

	switch mollieSub.Status {
	case "canceled":
		if _, err := s.pool.Exec(ctx,
			`UPDATE public.subscriptions
			    SET status = 'canceled',
			        canceled_at = COALESCE(canceled_at, now())
			  WHERE mollie_subscription_id = $1
			    AND status IN ('active', 'past_due')`,
			subscriptionID,
		); err != nil {
			return fmt.Errorf("mark canceled: %w", err)
		}
		slog.Info("billing.webhook.subscription_canceled", "id", subscriptionID)
	case "completed":
		if _, err := s.pool.Exec(ctx,
			`UPDATE public.subscriptions
			    SET status = 'expired',
			        canceled_at = COALESCE(canceled_at, now())
			  WHERE mollie_subscription_id = $1
			    AND status IN ('active', 'past_due')`,
			subscriptionID,
		); err != nil {
			return fmt.Errorf("mark expired: %w", err)
		}
		slog.Info("billing.webhook.subscription_completed", "id", subscriptionID)
	case "active", "pending", "suspended":
		// No-op — active/pending are steady states; suspended
		// is Mollie's warning that recurring is temporarily
		// blocked (usually mandate issue). The payment webhook
		// already covers past_due tracking; we don't need to
		// mirror suspended into our schema.
		return nil
	default:
		slog.Warn("billing.webhook.unknown_subscription_status",
			"id", subscriptionID, "status", mollieSub.Status)
	}
	return nil
}

// markInvoicePaid is the idempotent invoice-flip used by webhook
// retries after activation is already complete. Extracted so both
// the "already active" fast path and future refunds hooks share
// it.
func (s *Service) markInvoicePaid(ctx context.Context, payment *mollie.Payment) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE public.invoices
		    SET status = 'paid',
		        paid_at = COALESCE(paid_at, now())
		  WHERE mollie_payment_id = $1`,
		payment.ID,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return fmt.Errorf("mark invoice paid (%s): %w", pgErr.Code, err)
		}
		return fmt.Errorf("mark invoice paid: %w", err)
	}
	return nil
}

// parseMollieDate parses "YYYY-MM-DD" as returned by Mollie's
// nextPaymentDate + startDate fields. Returns nil on empty/parse
// failure so callers can treat the field as optional.
func parseMollieDate(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		slog.Warn("billing: malformed mollie date", "value", s, "error", err)
		return nil
	}
	return &t
}
