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
//	tr_   — Payment
//	sub_  — Subscription
//	re_   — Refund (not handled here; refunds are created by our
//	               own action in PR 8, not webhook-driven)
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
//  1. sequenceType=first + status=paid    → activate: create the
//     Mollie recurring subscription against the mandate captured
//     by the first payment, flip our subscriptions row to
//     'active', mark the invoice paid, flip projects.plan.
//  2. sequenceType=recurring + status=paid → renewal: insert a
//     new invoice row, bump subscriptions.next_charge_at.
//  3. status=failed on any recurring/first attempt → mark the
//     subscription past_due (idempotent: only sets past_due_since
//     on the first transition, subsequent same-signal writes are
//     no-ops).
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

	// A zero-amount payment that only adds a new card / PayPal account
	// for an existing subscription (payment_method.go). Never a checkout:
	// a failed or abandoned one changes nothing.
	if payment.Metadata["purpose"] == purposePaymentMethodUpdate {
		if payment.Status == "paid" {
			return s.applyPaymentMethodUpdate(ctx, payment)
		}
		return nil
	}

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
	var (
		currentStatus     string
		existingMollieSub *string
		startsAt          *time.Time
		priceCents        int
		subCustomerID     *string
		ownerCustomerID   *string
	)
	err := s.pool.QueryRow(ctx,
		`SELECT s.status, s.mollie_subscription_id, s.starts_at, s.price_cents,
		        s.mollie_customer_id, u.mollie_customer_id
		   FROM public.subscriptions s
		   JOIN public.projects p ON p.id = s.project_id
		   JOIN public.platform_users u ON u.id = p.owner_id
		  WHERE s.id = $1 AND s.project_id = $2`,
		subscriptionID, projectID,
	).Scan(&currentStatus, &existingMollieSub, &startsAt, &priceCents, &subCustomerID, &ownerCustomerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("subscription %s not found (payment %s)", subscriptionID, payment.ID)
	}
	if err != nil {
		return fmt.Errorf("load subscription %s: %w", subscriptionID, err)
	}
	if existingMollieSub != nil && *existingMollieSub != "" {
		// Already processed — a webhook retry, or Mollie re-calling the
		// webhook for a refund / chargeback on this payment. Never
		// treat it as a new or orphaned payment.
		if currentStatus == "active" || currentStatus == "scheduled" {
			return s.markInvoicePaid(ctx, payment)
		}
		return nil
	}
	// A checkout that was closed meanwhile (the project changed owner,
	// the owner started another one) or a payer who no longer owns the
	// project: never start a subscription on their mandate.
	// The payer must be the customer the checkout was started for, and
	// that customer must be the project's current owner's. A customer no
	// platform user carries (its ID was never persisted there) is
	// accepted on the checkout row's match alone.
	var payerUserID string
	if perr := s.pool.QueryRow(ctx,
		`SELECT id FROM public.platform_users WHERE mollie_customer_id = $1 ORDER BY created_at LIMIT 1`,
		payment.CustomerID,
	).Scan(&payerUserID); perr != nil && !errors.Is(perr, pgx.ErrNoRows) {
		return fmt.Errorf("resolve payer: %w", perr)
	}
	var ownerID string
	if oerr := s.pool.QueryRow(ctx,
		`SELECT owner_id FROM public.projects WHERE id = $1`, projectID,
	).Scan(&ownerID); oerr != nil {
		return fmt.Errorf("load project owner: %w", oerr)
	}
	scheduled := currentStatus == "scheduled"
	payerIsOwner := subCustomerID != nil && *subCustomerID == payment.CustomerID &&
		(payerUserID == ownerID || (payerUserID == "" && ownerCustomerID == nil))
	if (currentStatus != "incomplete" && !scheduled) || !payerIsOwner {
		slog.Warn("billing.webhook.first_payment_for_closed_checkout",
			"subscription_id", subscriptionID, "project_id", projectID,
			"status", currentStatus, "mollie_payment_id", payment.ID)
		if _, uerr := s.pool.Exec(ctx,
			`UPDATE public.subscriptions
			    SET status = 'canceled', canceled_at = COALESCE(canceled_at, now())
			  WHERE id = $1 AND status IN ('incomplete', 'scheduled')
			    AND mollie_subscription_id IS NULL`,
			subscriptionID,
		); uerr != nil {
			return fmt.Errorf("close checkout %s: %w", subscriptionID, uerr)
		}
		if payment.Amount.Value != "" && payment.Amount.Value != "0.00" {
			s.refundOrphanedPayment(ctx, payment, "checkout no longer valid")
			if _, uerr := s.pool.Exec(ctx,
				`UPDATE public.invoices SET status = 'refunded' WHERE mollie_payment_id = $1`,
				payment.ID,
			); uerr != nil {
				slog.Warn("billing.webhook.orphan_invoice_update_failed", "mollie_payment_id", payment.ID, "error", uerr)
			}
		}
		return nil
	}

	// Create the Mollie recurring subscription. Amount + interval
	// derive from our subscriptions row (source of truth is our
	// DB — Mollie's payment amount is just the first charge).
	// A scheduled subscription's first charge is on starts_at (the
	// end of the period the previous subscription paid for); the
	// zero-amount first payment only set up the mandate.
	startDate := mollie.DefaultRecurringStartDate(time.Now())
	if scheduled {
		// A scheduled subscription only starts while the one it follows
		// is the project's only live subscription and is cancelled (or
		// already gone). If another live subscription started meanwhile
		// (the owner subscribed normally after a Free gap), starting this
		// one would charge twice: close it instead.
		var conflicting int
		if cerr := s.pool.QueryRow(ctx,
			`SELECT count(*) FROM public.subscriptions
			  WHERE project_id = $1
			    AND status IN ('incomplete', 'active', 'past_due')
			    AND canceled_at IS NULL`,
			projectID,
		).Scan(&conflicting); cerr != nil {
			return fmt.Errorf("check live subscriptions: %w", cerr)
		}
		if conflicting > 0 {
			slog.Warn("billing.webhook.scheduled_superseded",
				"subscription_id", subscriptionID, "project_id", projectID)
			if _, uerr := s.pool.Exec(ctx,
				`UPDATE public.subscriptions
				    SET status = 'canceled', canceled_at = now()
				  WHERE id = $1 AND status = 'scheduled' AND mollie_subscription_id IS NULL`,
				subscriptionID,
			); uerr != nil {
				return fmt.Errorf("close superseded scheduled subscription: %w", uerr)
			}
			return nil
		}
		if startsAt != nil {
			startDate = startsAt.UTC().Format("2006-01-02")
			if today := mollie.TodayInAmsterdam(time.Now()); startDate < today {
				startDate = today
			}
		}
	}
	mollieSub, err := s.client.CreateSubscription(ctx, payment.CustomerID, mollie.SubscriptionCreateRequest{
		Amount:      mollie.AmountFromCents(priceCents, "EUR"),
		Interval:    "1 month",
		StartDate:   startDate,
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

	if scheduled {
		// Stays 'scheduled' until starts_at: the current subscription
		// keeps the project on its plan until then.
		tag, err := s.pool.Exec(ctx,
			`UPDATE public.subscriptions
			    SET mollie_subscription_id = $1,
			        next_charge_at = $2
			  WHERE id = $3 AND status = 'scheduled' AND mollie_subscription_id IS NULL`,
			mollieSub.ID, firstChargeDate(mollieSub), subscriptionID,
		)
		if err != nil {
			return fmt.Errorf("confirm scheduled subscription: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// Closed while we were creating it (e.g. the project changed
			// owner): don't leave a Mollie subscription nothing tracks.
			s.abandonActivation(ctx, payment, mollieSub.ID, subscriptionID)
			return nil
		}
		slog.Info("billing.webhook.scheduled",
			"subscription_id", subscriptionID,
			"project_id", projectID,
			"mollie_subscription_id", mollieSub.ID,
			"start_date", startDate,
		)
		// Late confirmation (the period already ended): take over now.
		if _, perr := promoteScheduled(ctx, s.pool, s.client, projectID, false); perr != nil {
			slog.Warn("billing.webhook.promote_failed", "project_id", projectID, "error", perr)
		}
		return nil
	}

	// Flip state atomically. next_charge_at from Mollie's
	// nextPaymentDate (YYYY-MM-DD), or the startDate we sent when
	// Mollie leaves it out — parse defensively.
	nextChargeAt := firstChargeDate(mollieSub)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	flip, err := tx.Exec(ctx,
		`UPDATE public.subscriptions
		    SET mollie_subscription_id = $1,
		        status = 'active',
		        started_at = COALESCE(started_at, now()),
		        next_charge_at = $2
		  WHERE id = $3 AND status = 'incomplete'`,
		mollieSub.ID, nextChargeAt, subscriptionID,
	)
	if err != nil {
		return fmt.Errorf("flip subscription to active: %w", err)
	}
	if flip.RowsAffected() == 0 {
		// The checkout was closed while we were creating the Mollie
		// subscription (e.g. the project changed owner): no invoice, no
		// plan change, and nothing left charging.
		_ = tx.Rollback(ctx)
		s.abandonActivation(ctx, payment, mollieSub.ID, subscriptionID)
		return nil
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
		ownerID        string
		name           string
		slug           string
		region         string
		planCode       string
		userEmail      string
		orgID          *string
		orgIDExplicit  bool
		requestedOrgID *string
	)
	err = s.pool.QueryRow(ctx,
		`SELECT pp.owner_id::text, pp.name, pp.slug, pp.region, pp.plan, pu.email, pp.org_id::text, pp.org_id_explicit, pp.requested_org_id::text
		   FROM public.pending_projects pp
		   JOIN public.platform_users pu ON pu.id = pp.owner_id
		  WHERE pp.id = $1`,
		pendingID,
	).Scan(&ownerID, &name, &slug, &region, &planCode, &userEmail, &orgID, &orgIDExplicit, &requestedOrgID)
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
		// Fresh path — no project yet, create one. Race-log for #610
		// option A: only fires when the caller explicitly picked a
		// specific org AND the FK cascade nulled pp.org_id (target
		// org deleted mid-payment). Detected via requested_org_id
		// (never nulled by cascade) not org_id_explicit alone, since
		// the latter is also true for ordinary Personal picks.
		// Round-1 review on #617 caught that the old signal would
		// warn on every Personal Pro checkout and had no distinct
		// signal for the actual race.
		if requestedOrgID != nil && orgID == nil {
			slog.Warn("billing.webhook.explicit_org_gone_falling_back_to_personal",
				"pending_project_id", pendingID,
				"owner_id", ownerID,
				"slug", slug,
				"requested_org_id", *requestedOrgID,
			)
		}
		projectID, err = s.projectCreator.CreateProjectForBilling(ctx, ownerID, userEmail, name, slug, region, planCode, orgID, orgIDExplicit)
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
	//
	// **startDate is critical.** See mollie.DefaultRecurringStartDate
	// for the full explanation — TL;DR: without startDate Mollie
	// fires a second €25 charge within 24h of the initial
	// mandate-creating payment, double-billing month 1.
	mollieSub, msErr := s.client.CreateSubscription(ctx, payment.CustomerID, mollie.SubscriptionCreateRequest{
		Amount:      mollie.AmountFromCents(priceCents, "EUR"),
		Interval:    "1 month",
		StartDate:   mollie.DefaultRecurringStartDate(time.Now()),
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
		nextChargeAt = firstChargeDate(mollieSub)
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
		    (project_id, subscription_id, mollie_payment_id, amount_cents, currency, status, paid_at, billed_to_user_id)
		 VALUES ($1, $2, $3, $4, 'EUR', 'paid', now(),
		         (SELECT owner_id FROM public.projects WHERE id = $1))`,
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

// abandonActivation undoes a Mollie subscription created for a checkout
// that was closed meanwhile: cancels it and refunds a non-zero first
// payment. Best-effort; failures are logged loudly for ops.
func (s *Service) abandonActivation(ctx context.Context, payment *mollie.Payment, mollieSubID, subscriptionID string) {
	slog.Error("billing.webhook.activation_for_closed_checkout — cancelling",
		"subscription_id", subscriptionID, "mollie_subscription_id", mollieSubID, "mollie_payment_id", payment.ID)
	if _, err := s.client.CancelSubscription(ctx, payment.CustomerID, mollieSubID); err != nil && !errors.Is(err, mollie.ErrNotFound) {
		slog.Error("billing.webhook.abandon_cancel_failed",
			"subscription_id", subscriptionID, "mollie_subscription_id", mollieSubID, "error", err)
	}
	if payment.Amount.Value != "" && payment.Amount.Value != "0.00" {
		_ = s.refundOrphanedPayment(ctx, payment, "checkout closed during activation")
		if _, err := s.pool.Exec(ctx,
			`UPDATE public.invoices SET status = 'refunded' WHERE mollie_payment_id = $1`, payment.ID,
		); err != nil {
			slog.Warn("billing.webhook.abandon_invoice_update_failed", "mollie_payment_id", payment.ID, "error", err)
		}
	}
}

// alreadyRefunded reports whether Mollie says the payment has been
// refunded in full.
func alreadyRefunded(p *mollie.Payment) bool {
	if p.AmountRefunded == nil {
		return false
	}
	total := p.Amount.Cents()
	return total > 0 && p.AmountRefunded.Cents() >= total
}

// refundOrphanedPayment issues a Mollie refund for a payment we
// took but can't fulfill (pending row expired, project creation
// failed, etc.). Best-effort — we log if the refund itself fails,
// since manual intervention is required for such cases anyway.
func (s *Service) refundOrphanedPayment(ctx context.Context, payment *mollie.Payment, reason string) error {
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
		return err
	}
	slog.Warn("billing.webhook.refunded_orphan",
		"mollie_payment_id", payment.ID,
		"reason", reason,
	)
	return nil
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
	var subscriptionID, projectID, status string
	var priceCents int
	var prevNextCharge, canceledAt *time.Time
	load := func() error {
		return s.pool.QueryRow(ctx,
			`SELECT id, project_id, status, price_cents, next_charge_at, canceled_at
			   FROM public.subscriptions
			  WHERE mollie_subscription_id = $1`,
			payment.SubscriptionID,
		).Scan(&subscriptionID, &projectID, &status, &priceCents, &prevNextCharge, &canceledAt)
	}
	err := load()
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("no local subscription for mollie sub %s", payment.SubscriptionID)
	}
	if err != nil {
		return fmt.Errorf("load subscription: %w", err)
	}

	// First charge of a scheduled subscription (handover.go) before the
	// hourly sweep promoted it: promote now.
	if status == "scheduled" {
		if _, perr := promoteScheduled(ctx, s.pool, s.client, projectID, true); perr != nil {
			return fmt.Errorf("promote scheduled subscription: %w", perr)
		}
		if err := load(); err != nil {
			return fmt.Errorf("reload subscription: %w", err)
		}
	}

	// Already refunded (Mollie calls the webhook again for a refund or
	// chargeback on a payment): nothing more to do here.
	if alreadyRefunded(payment) {
		return nil
	}

	// A charge that lands on a subscription that has ended: a late
	// retry of a failed charge (SEPA can settle days later) after the
	// project was downgraded, or any charge from a subscription the
	// project no longer uses. Refund it, unless we recorded it as paid
	// before (then this is a redelivery for a charge that was fine when
	// it came in). A 'failed' invoice doesn't count: a retry that
	// succeeds late may carry the failed payment's id.
	if status == "expired" || status == "canceled" {
		var recorded bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM public.invoices
			                 WHERE mollie_payment_id = $1 AND status IN ('paid', 'refunded'))`,
			payment.ID,
		).Scan(&recorded); err != nil {
			return fmt.Errorf("check invoice for %s: %w", payment.ID, err)
		}
		if !recorded {
			slog.Error("billing.webhook.charge_on_closed_subscription — refunding",
				"subscription_id", subscriptionID, "status", status, "mollie_payment_id", payment.ID)
			if err := s.refundOrphanedPayment(ctx, payment, "charged after the subscription had ended"); err != nil {
				return fmt.Errorf("refund charge on closed subscription %s: %w", payment.ID, err)
			}
			return nil
		}
	}

	// A charge Mollie created after we cancelled the subscription means
	// the cancel didn't reach Mollie (e.g. during an ownership transfer):
	// refund it rather than bill a period nobody asked for, and cancel
	// again.
	if canceledAt != nil && payment.CreatedAt.After(*canceledAt) {
		slog.Error("billing.webhook.charge_after_cancel — refunding",
			"subscription_id", subscriptionID, "mollie_payment_id", payment.ID)
		refundErr := s.refundOrphanedPayment(ctx, payment, "charged after the subscription was cancelled")
		if _, cerr := s.client.CancelSubscription(ctx, payment.CustomerID, payment.SubscriptionID); cerr != nil && !errors.Is(cerr, mollie.ErrNotFound) {
			slog.Error("billing.webhook.cancel_retry_failed",
				"subscription_id", subscriptionID, "error", cerr)
		}
		if refundErr != nil {
			// Fails the webhook so the failure metric fires and Mollie
			// re-delivers; the refund's idempotency key makes a retry safe.
			return fmt.Errorf("refund charge after cancel %s: %w", payment.ID, refundErr)
		}
		return nil
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
		    (project_id, subscription_id, mollie_payment_id, amount_cents, currency, status, paid_at, billed_to_user_id)
		 VALUES ($1, $2, $3, $4, 'EUR', 'paid', now(),
		         -- The payer: whoever the subscription's Mollie customer
		         -- belongs to, which after a transfer isn't necessarily
		         -- the project's current owner.
		         COALESCE(
		           (SELECT u.id FROM public.platform_users u
		              JOIN public.subscriptions s ON s.mollie_customer_id = u.mollie_customer_id
		             WHERE s.id = $2
		             ORDER BY u.created_at LIMIT 1),
		           (SELECT owner_id FROM public.projects WHERE id = $1)))
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
		renewalNextCharge(mollieSub, prevNextCharge, time.Now(), subscriptionID),
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
// failed charge. Mollie retries a failed recurring charge up to 5
// times, once a day, and cancels the subscription after the last one
// (or at once for some failure reasons); each retry fires another
// webhook. Only the first transition sets past_due_since and mails
// the owner — the query below is a no-op on retries because it
// targets status='active'. The downgrade sweep uses past_due_since
// to time the 7-day grace before the project drops to Free.
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
				    AND (status = 'incomplete'
				         OR (status = 'scheduled' AND mollie_subscription_id IS NULL))`,
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

	// A failed first charge of a scheduled subscription: it takes over
	// first, so the failure is tracked on the subscription now in force.
	var schedProject string
	if qerr := s.pool.QueryRow(ctx,
		`SELECT project_id FROM public.subscriptions
		  WHERE mollie_subscription_id = $1 AND status = 'scheduled'`,
		payment.SubscriptionID,
	).Scan(&schedProject); qerr == nil {
		if _, perr := promoteScheduled(ctx, s.pool, s.client, schedProject, true); perr != nil {
			return fmt.Errorf("promote scheduled subscription: %w", perr)
		}
	} else if !errors.Is(qerr, pgx.ErrNoRows) {
		return fmt.Errorf("load scheduled subscription: %w", qerr)
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
		sendPaymentProblemMail(ctx, s.pool, s.invoiceMailer, payment.SubscriptionID, paymentProblemChargeFailed)
	}
	return nil
}

// processSubscriptionWebhook handles a sub_ id posted to the webhook.
// Mollie doesn't send webhooks for subscription status changes (only
// for their payments), so in practice this runs only if someone posts
// a subscription id by hand; the downgrade sweep's reconcile step
// (reconcileStoppedSubscriptions) is what notices a subscription Mollie
// stopped. Both apply the same rule: a live subscription we didn't
// cancel goes past_due, so the project gets the usual grace and then
// drops to Free, instead of staying on Pro unpaid.
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

	if !mollieSubscriptionStopped(mollieSub.Status) {
		// active / pending: steady states, nothing to mirror.
		return nil
	}
	// A scheduled subscription stopped before it started can't take
	// over: close it.
	if _, err := s.pool.Exec(ctx,
		`UPDATE public.subscriptions
		    SET status = 'canceled', canceled_at = COALESCE(canceled_at, now())
		  WHERE mollie_subscription_id = $1 AND status = 'scheduled'`,
		subscriptionID,
	); err != nil {
		return fmt.Errorf("close scheduled subscription: %w", err)
	}
	changed, err := markStoppedAtMollie(ctx, s.pool, subscriptionID)
	if err != nil {
		return fmt.Errorf("mark stopped subscription past_due: %w", err)
	}
	if changed {
		slog.Warn("billing.webhook.subscription_stopped_at_mollie",
			"id", subscriptionID, "status", mollieSub.Status)
		sendPaymentProblemMail(ctx, s.pool, s.invoiceMailer, subscriptionID, paymentProblemStoppedAtMollie)
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

// firstChargeDate is the next charge of a subscription just created:
// Mollie's nextPaymentDate, or its startDate when Mollie leaves
// nextPaymentDate out (seen in production). Cancel at end of period
// and the downgrade sweeper need this date.
func firstChargeDate(sub *mollie.Subscription) *time.Time {
	if sub == nil {
		return nil
	}
	if t := parseMollieDate(sub.NextPaymentDate); t != nil {
		return t
	}
	slog.Warn("billing.mollie.next_payment_date_missing",
		"mollie_subscription_id", sub.ID, "mollie_status", sub.Status,
		"start_date", sub.StartDate, "fallback", "start_date")
	return parseMollieDate(sub.StartDate)
}

// renewalNextCharge is next_charge_at after a monthly charge was paid:
// Mollie's nextPaymentDate, else the previous date plus one month when
// that date has been reached (keeping the charged date would make an
// end-of-period cancel downgrade a month early), else the previous date.
// nil leaves the column unchanged.
func renewalNextCharge(sub *mollie.Subscription, prev *time.Time, now time.Time, subscriptionID string) *time.Time {
	if sub != nil {
		if t := parseMollieDate(sub.NextPaymentDate); t != nil {
			return t
		}
	}
	if prev == nil || prev.After(now) {
		return prev
	}
	next := addMonthClamped(*prev)
	slog.Warn("billing.mollie.next_payment_date_missing",
		"subscription_id", subscriptionID, "fallback", "previous_plus_one_month",
		"next_charge_at", next.Format("2006-01-02"))
	return &next
}

// addMonthClamped adds one calendar month, clamped to the last day of
// that month (Jan 31 → Feb 28), like mollie.DefaultRecurringStartDate.
func addMonthClamped(t time.Time) time.Time {
	first := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	lastOfNext := first.AddDate(0, 2, -1).Day()
	day := t.Day()
	if day > lastOfNext {
		day = lastOfNext
	}
	return time.Date(t.Year(), t.Month()+1, day, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
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
