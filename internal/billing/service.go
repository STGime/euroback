// Package billing wires the Mollie payments client to Eurobase's
// subscription state. PR 3 of the billing stack — CreateCheckout
// only. Webhook state-machine lives in PR 4; downgrade cron in PR 5.
package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/eurobase/euroback/internal/billing/mollie"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sentinel errors the HTTP handler branches on for status-code
// selection. Keep this list small — every new sentinel is a new
// branch every future handler needs to remember.
var (
	// ErrBillingDisabled is returned when BILLING_ENABLED is not
	// set. Every checkout attempt fails closed until PR 8 flips
	// the flag in prod. Handler returns 503.
	ErrBillingDisabled = errors.New("billing: not enabled")

	// ErrProjectNotFound is returned when the project either
	// doesn't exist or isn't owned by the authenticated user. The
	// same error covers both cases deliberately — leaking whether
	// a UUID exists in the DB is an unnecessary information
	// disclosure. Handler returns 404.
	ErrProjectNotFound = errors.New("billing: project not found")

	// ErrAlreadySubscribed is returned when there's already a
	// live (incomplete / active / past_due) subscription for the
	// project. Handler returns 409. Also produced by the
	// idx_subscriptions_project_live UNIQUE partial index in the
	// race window between the check and the insert — caught below
	// and translated.
	ErrAlreadySubscribed = errors.New("billing: project already has a live subscription")

	// ErrInvalidPlan is returned when planCode isn't one we
	// recognise. Handler returns 400.
	ErrInvalidPlan = errors.New("billing: invalid plan code")

	// ErrPlanNotPriced is returned when a caller requests checkout
	// for a plan whose price_cents on plan_limits is NULL — currently
	// only Team during the closed-beta window. The correct path for
	// these plans is either the admin beta-grant flow (RecordBetaGrant)
	// or waiting for the plan to be priced. Handler returns 400 with
	// a distinct code so the console can render an accurate message.
	ErrPlanNotPriced = errors.New("billing: plan is not priced yet — beta grant required")

	// ErrTeamBetaRequired is returned when a non-beta user tries to
	// subscribe to Team via the checkout path. Mirrors the tenant
	// package's error so callers see a consistent shape regardless
	// of which layer catches it. Handler returns 403.
	ErrTeamBetaRequired = errors.New("billing: team plan requires closed-beta access")

	// ErrPendingCheckoutInFlight is returned when a caller starts a
	// new-project checkout while they already have an unresolved
	// pending row younger than pendingCheckoutCooldown. Prevents
	// a double-click from opening two Mollie payments and
	// charging twice. Handler returns 409. The response body
	// includes the existing pending_project_id and checkout_url so
	// the console can resume the in-flight checkout rather than
	// error visibly. See NewProjectCheckout below.
	ErrPendingCheckoutInFlight = errors.New("billing: another new-project checkout is already in flight for this owner")

	// ErrPendingProjectNotFound is returned when the webhook
	// resolves a pending_project_id from Mollie metadata and no
	// row exists (expired + swept, or the sweeper raced the
	// webhook). Handler returns 410 Gone with a note that the
	// payment will be refunded — the webhook branch schedules the
	// refund before surfacing this error.
	ErrPendingProjectNotFound = errors.New("billing: pending project not found (may have expired)")

	// ErrSlugTaken is returned by NewProjectCheckout when the
	// requested slug already identifies an existing project (any
	// owner — projects.slug is globally UNIQUE). Rejected BEFORE
	// opening a Mollie payment so the user doesn't pay-then-refund
	// on a preventable name clash. Also closes an adopt-path bug
	// (#407 re-review 🟡): without this check, a user reusing a
	// slug they already own would have their new payment adopted
	// onto the pre-existing project. Handler returns 409.
	ErrSlugTaken = errors.New("billing: slug already in use")
)

// pendingCheckoutCooldown is the window during which a second
// new-project checkout by the same owner is rejected with
// ErrPendingCheckoutInFlight. Long enough that a double-click or
// impatient-refresh returns the existing in-flight checkout URL
// rather than opening a second Mollie payment; short enough that
// a user who genuinely abandons the first checkout and comes back
// can start a fresh one without hitting a stale block. 5 min
// matches Mollie's typical hosted-page timeout.
const pendingCheckoutCooldown = 5 * time.Minute

// planPriceCents is the source of truth for public plan pricing
// used by the Mollie checkout path. Team is intentionally NOT
// listed here — its price is stored on plan_limits.price_cents (may
// be NULL during the closed beta) and looked up dynamically in
// CreateCheckout. Non-beta Team checkout will read the DB value; if
// still NULL it errors with ErrTeamNotPriced.
//
// Grandfathering was dropped from the launch scope (see
// docs/billing/stacked-pr-plan.md); every user pays the same price.
var planPriceCents = map[string]int{
	"pro": 1900, // €19/mo per project
}

// WebhookMetrics is the surface the webhook handler uses to record
// failure counts. Kept as a tiny interface so tests can inject a
// no-op recorder and the billing package doesn't have to import
// internal/metrics. Nil is safe — every method is a no-op on nil.
type WebhookMetrics interface {
	IncBillingWebhookFailed(resource string)
}

// ProjectCreator is the subset of tenant.TenantService that the
// billing package needs to create a project from a paid first-
// payment webhook (see issue #406, payment-first project creation).
// Matched by tenant.TenantService.CreateProjectForBilling. Optional —
// if nil, NewProjectCheckout still opens the Mollie payment but
// the webhook's activate-new-project branch will refund the
// customer and log an ops-visible warning.
//
// Uses primitives rather than tenant.CreateProjectRequest so
// billing doesn't have to import internal/tenant (billing↔tenant
// wiring goes through interfaces in both directions).
type ProjectCreator interface {
	CreateProjectForBilling(ctx context.Context, ownerID, email, name, slug, region, plan string) (projectID string, err error)
}

// LimitsChecker is the subset of plans.LimitsService that
// NewProjectCheckout uses to enforce the per-owner project cap
// BEFORE opening a Mollie payment. Matched by
// *plans.LimitsService.CheckProjectLimit. Optional but strongly
// recommended — without it, a user can exceed their plan's project
// cap via the paid checkout path (#407 review 🟡 #4).
//
// Return non-nil error → NewProjectCheckout aborts before Mollie.
// The error message surfaces to the user as the 400 body, so it
// should be user-facing.
type LimitsChecker interface {
	CheckProjectLimit(ctx context.Context, ownerID string) error
}

// Service owns the runtime dependencies for the billing HTTP
// surface. Constructed once at server startup and re-used across
// every request — safe because Client, Pool, and the config strings
// are read-only after construction.
type Service struct {
	pool           *pgxpool.Pool
	client         *mollie.Client
	config         Config
	enabled        bool
	metrics        WebhookMetrics
	storage        invoiceStorage
	invoiceMailer  invoiceMailer
	projectCreator ProjectCreator
	limits         LimitsChecker
}

// Config holds the settings CreateCheckout reads on every call.
// Populated from env vars in cmd/gateway/main.go.
type Config struct {
	// ConsoleBaseURL is the console origin used to build the
	// post-checkout redirect URLs. Example:
	// "https://console.eurobase.app".
	ConsoleBaseURL string

	// WebhookBaseURL is the public URL Mollie POSTs to on payment
	// state changes. Typically the gateway's own base — Mollie
	// requires an HTTPS URL reachable from the public internet.
	// Example: "https://api.eurobase.app".
	WebhookBaseURL string

	// Mode is the Mollie environment string surfaced to the
	// console via GET /platform/billing/config so it can render
	// a "test mode — no card is charged" banner. Populated from
	// MOLLIE_ENV in cmd/gateway/main.go; "test" or "live".
	Mode string
}

// NewService constructs the billing service. `enabled=false` makes
// every method return ErrBillingDisabled without hitting the DB or
// Mollie — matches the empty-key fallback shape the Mollie client
// itself uses so dev environments boot without secrets.
func NewService(pool *pgxpool.Pool, client *mollie.Client, cfg Config, enabled bool) *Service {
	return &Service{
		pool:    pool,
		client:  client,
		config:  cfg,
		enabled: enabled,
	}
}

// WithMetrics attaches a metrics recorder. Optional — a nil
// metrics is safe (IncBillingWebhookFailed is a no-op on nil).
// Call after NewService and before serving traffic.
func (s *Service) WithMetrics(m WebhookMetrics) *Service {
	s.metrics = m
	return s
}

// WithProjectCreator attaches the tenant-side project creator so
// the new-project-checkout webhook branch can call CreateProject
// after Mollie confirms first payment. Optional — nil is safe,
// but leaves the new-project flow broken (webhook refunds the
// customer and logs). See issue #406.
func (s *Service) WithProjectCreator(pc ProjectCreator) *Service {
	s.projectCreator = pc
	return s
}

// WithLimits attaches the project-limit checker so NewProjectCheckout
// can enforce the per-owner project cap BEFORE opening a Mollie
// payment. Optional but strongly recommended — see #407 review 🟡 #4.
func (s *Service) WithLimits(l LimitsChecker) *Service {
	s.limits = l
	return s
}

// incFailureMetric bumps the webhook-failure counter if metrics is
// wired. Called from the webhook handler on every terminal error
// branch (which returns 200 to Mollie — the counter is our only
// signal that something broke).
func (s *Service) incFailureMetric(resource string) {
	if s.metrics != nil {
		s.metrics.IncBillingWebhookFailed(resource)
	}
}

// Enabled reports whether billing is turned on for this process.
// Handlers use this for the fail-closed 503 branch before doing any
// work. Exported so the router can register or hide routes at
// startup depending on the flag.
func (s *Service) Enabled() bool { return s.enabled }

// Mode reports the Mollie environment ("test" or "live"). Used by
// the console via GET /platform/billing/config to decide whether to
// render the "no card is charged" banner. Empty string when Config
// wasn't populated — the console treats that as unknown and hides
// the banner (fail-safe: never claim test mode when we don't know).
func (s *Service) Mode() string { return s.config.Mode }

// CheckoutResult carries the outbound values the handler needs to
// serialise back to the console. CheckoutURL is what the console
// redirects the user's browser to; SubscriptionID is our internal
// UUID so the console can start polling for status.
type CheckoutResult struct {
	SubscriptionID string
	CheckoutURL    string
}

// CreateCheckout starts the payment flow for a project. Steps:
//
//  1. Verify the user owns the project.
//  2. Refuse if a live subscription already exists (409).
//  3. Lazily create/lookup the Mollie customer on platform_users.
//  4. Insert an 'incomplete' row in subscriptions (transactional).
//  5. Create a first Mollie payment tied to that customer and
//     project; write the resulting mollie_payment_id back onto the
//     invoice + subscription rows.
//  6. Return the Mollie checkout URL for the console to redirect to.
//
// The unique partial index idx_subscriptions_project_live catches
// the race where two concurrent checkouts insert an 'incomplete'
// row for the same project; the SQLSTATE 23505 error surfaces as
// ErrAlreadySubscribed.
func (s *Service) CreateCheckout(ctx context.Context, userID, projectID, planCode string) (*CheckoutResult, error) {
	if !s.enabled {
		return nil, ErrBillingDisabled
	}

	price, err := s.resolvePriceCents(ctx, planCode)
	if err != nil {
		return nil, err
	}

	// 1. Ownership check. Same query shape as tenant/context.go
	// uses so error semantics match the rest of the platform.
	var projectName string
	err = s.pool.QueryRow(ctx,
		`SELECT name
		   FROM public.projects
		  WHERE id = $1 AND owner_id = $2::uuid AND status = 'active'`,
		projectID, userID,
	).Scan(&projectName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProjectNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("billing: ownership check: %w", err)
	}

	// 2. Pre-check for an existing live subscription. The unique
	// partial index enforces this at insert time; the pre-check
	// just gives a cleaner error path for the common (non-race)
	// case.
	var existing string
	err = s.pool.QueryRow(ctx,
		`SELECT id FROM public.subscriptions
		  WHERE project_id = $1 AND status IN ('incomplete', 'active', 'past_due')
		  LIMIT 1`,
		projectID,
	).Scan(&existing)
	if err == nil {
		return nil, ErrAlreadySubscribed
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("billing: existing-sub check: %w", err)
	}

	// 3. Get or create the Mollie customer. Stored on
	// platform_users.mollie_customer_id so a user with many
	// projects reuses the same Mollie customer.
	var userEmail string
	var mollieCustomerID *string
	err = s.pool.QueryRow(ctx,
		`SELECT email, mollie_customer_id FROM public.platform_users WHERE id = $1::uuid`,
		userID,
	).Scan(&userEmail, &mollieCustomerID)
	if err != nil {
		return nil, fmt.Errorf("billing: load platform_user: %w", err)
	}

	customerID := ""
	if mollieCustomerID != nil {
		customerID = *mollieCustomerID
	}
	if customerID == "" {
		cust, cerr := s.client.CreateCustomer(ctx, mollie.CustomerCreateRequest{
			Email: userEmail,
			Metadata: map[string]string{
				"platform_user_id": userID,
			},
		}, mollie.WithIdempotencyKey("customer:"+userID))
		if cerr != nil {
			return nil, fmt.Errorf("billing: create mollie customer: %w", cerr)
		}
		customerID = cust.ID
		if _, uerr := s.pool.Exec(ctx,
			`UPDATE public.platform_users SET mollie_customer_id = $1 WHERE id = $2::uuid`,
			customerID, userID,
		); uerr != nil {
			// Non-fatal: the WithIdempotencyKey("customer:"+userID)
			// above means Mollie returns the SAME customer on retry
			// (no leak). We just fail to remember the ID locally, so
			// the next checkout re-derives via the same idempotency
			// key and gets the same cst_ back. Log loudly so ops
			// sees the drift and can investigate the DB-write path.
			slog.Warn("billing: created Mollie customer but failed to persist ID",
				"user_id", userID, "mollie_customer_id", customerID, "error", uerr)
		}
	}

	// 4. Insert the incomplete subscription row. Kept OUT of a
	// long-running tx so the Mollie HTTP call below doesn't hold
	// row locks + unique-index writes for up to 15 s under a
	// slow-Mollie failure mode (which would block every concurrent
	// checkout on the same project). Any SQLSTATE 23505 from the
	// unique partial index surfaces as ErrAlreadySubscribed.
	var subscriptionID string
	err = s.pool.QueryRow(ctx,
		`INSERT INTO public.subscriptions
		    (project_id, mollie_customer_id, plan, price_cents, currency,
		     billing_interval, status)
		 VALUES ($1, $2, $3, $4, 'EUR', '1 month', 'incomplete')
		 RETURNING id`,
		projectID, customerID, planCode, price,
	).Scan(&subscriptionID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// unique_violation — the UNIQUE partial index on
			// (project_id) WHERE status IN live-set caught a
			// concurrent checkout.
			return nil, ErrAlreadySubscribed
		}
		return nil, fmt.Errorf("billing: insert subscription: %w", err)
	}

	// 5. Create the first Mollie payment. Metadata carries the
	// project + subscription IDs so PR 4's webhook can resolve
	// back without a lookup. The idempotency key derives from the
	// subscription UUID (freshly minted above) so a River retry
	// after a network flake reuses the same Mollie payment.
	//
	// On failure we EXPLICITLY roll back the subscription row so
	// the unique partial index doesn't block a retry with a fresh
	// UUID. Without this rollback the row would linger in
	// 'incomplete' state until PR 5's cron sweeps it — that's
	// operationally fine but produces a confusing "already
	// subscribed" error for the user on their next attempt.
	payment, err := s.client.CreatePayment(ctx, mollie.PaymentCreateRequest{
		Amount:       mollie.AmountFromCents(price, "EUR"),
		Description:  fmt.Sprintf("Eurobase %s — %s", planCode, projectName),
		RedirectURL:  fmt.Sprintf("%s/projects/%s/billing?status=success", s.config.ConsoleBaseURL, projectID),
		WebhookURL:   fmt.Sprintf("%s/platform/billing/webhook", s.config.WebhookBaseURL),
		CustomerID:   customerID,
		SequenceType: mollie.SequenceTypeFirst,
		Metadata: map[string]string{
			"subscription_id": subscriptionID,
			"project_id":      projectID,
			"plan_code":       planCode,
		},
	}, mollie.WithIdempotencyKey("checkout:"+subscriptionID))
	if err != nil {
		s.rollbackSubscription(ctx, subscriptionID)
		return nil, fmt.Errorf("billing: create mollie payment: %w", err)
	}
	if payment.Links.Checkout == nil || payment.Links.Checkout.Href == "" {
		// Extremely unexpected — first payments always return a
		// checkout URL. Surface loudly rather than 500 silently.
		s.rollbackSubscription(ctx, subscriptionID)
		return nil, fmt.Errorf("billing: mollie returned no checkout URL (payment %s)", payment.ID)
	}

	// 6. Persist the payment ID for the webhook idempotency
	// guard. We also drop an invoices row in 'pending' so the
	// console can immediately show "Awaiting first payment" on
	// the /billing screen. Two writes in one tx now that the
	// external call is safely behind us.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("billing: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Link invoice to subscription so re-renders of historical
	// invoices don't drift after cancel+resubscribe (PR 6 review).
	_, err = tx.Exec(ctx,
		`INSERT INTO public.invoices
		    (project_id, subscription_id, mollie_payment_id, amount_cents, currency, status)
		 VALUES ($1, $2, $3, $4, 'EUR', 'pending')`,
		projectID, subscriptionID, payment.ID, price,
	)
	if err != nil {
		return nil, fmt.Errorf("billing: insert invoice: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("billing: commit: %w", err)
	}

	slog.Info("billing: checkout created",
		"user_id", userID,
		"project_id", projectID,
		"subscription_id", subscriptionID,
		"mollie_payment_id", payment.ID,
		"plan_code", planCode,
		"price_cents", price,
	)

	return &CheckoutResult{
		SubscriptionID: subscriptionID,
		CheckoutURL:    payment.Links.Checkout.Href,
	}, nil
}

// NewProjectCheckoutRequest is the input for NewProjectCheckout.
// The name/slug/region trio is what would normally go straight to
// TenantService.CreateProject on the Free path; here it's persisted
// on public.pending_projects and applied to a fresh CreateProject
// call from the webhook once Mollie confirms first payment.
type NewProjectCheckoutRequest struct {
	Name   string
	Slug   string
	Region string
	Plan   string // must be "pro" — Free and Team don't use this path
}

// NewProjectCheckoutResult carries the outbound values the handler
// needs to serialise back to the console. On a duplicate-in-flight
// (ErrPendingCheckoutInFlight) the handler unwraps a sentinel and
// re-fetches these fields from the existing row rather than
// erroring — nicer UX than a 409.
type NewProjectCheckoutResult struct {
	PendingProjectID string
	CheckoutURL      string
}

// NewProjectCheckout starts a Mollie checkout for a paid-plan
// project that DOES NOT YET EXIST. The project row is created by
// the webhook (billing/webhook.go) once Mollie confirms first
// payment. See issue #406 for the "payment-first project creation"
// design; migration 000102 for the pending_projects table.
//
// Flow mirrors CreateCheckout except:
//   - no ownership check (the project doesn't exist yet)
//   - inserts into pending_projects instead of subscriptions
//   - Mollie metadata carries pending_project_id (not subscription_id
//     + project_id)
//   - no invoice row created here — that happens in the webhook so
//     the invoice's project_id column can point at the real project
//   - concurrent-click guard: rejects (with the in-flight
//     checkout's URL) when the same owner has an unresolved pending
//     row younger than pendingCheckoutCooldown
//
// Plan must be "pro". Free doesn't need billing; Team is closed-
// beta and goes through the beta-grant flow in TenantService.CreateProject.
func (s *Service) NewProjectCheckout(ctx context.Context, userID string, req NewProjectCheckoutRequest) (*NewProjectCheckoutResult, error) {
	if !s.enabled {
		return nil, ErrBillingDisabled
	}
	if req.Plan != "pro" {
		return nil, fmt.Errorf("%w: NewProjectCheckout only accepts 'pro' (got %q)", ErrInvalidPlan, req.Plan)
	}
	if req.Name == "" || req.Slug == "" || req.Region == "" {
		return nil, fmt.Errorf("%w: name, slug, and region are required", ErrInvalidPlan)
	}

	// 0a. Enforce project quota BEFORE opening a Mollie payment
	// (#407 review 🟡 #4). Otherwise a user over their cap can
	// still pay, and the webhook's CreateProject call has no
	// LimitsService plumbing so nothing catches it there —
	// they'd get a refund AFTER paying. Fail-loud upfront.
	if s.limits != nil {
		if err := s.limits.CheckProjectLimit(ctx, userID); err != nil {
			return nil, fmt.Errorf("%w: %s", ErrInvalidPlan, err.Error())
		}
	}

	// 0b. Reject slug clashes BEFORE opening a Mollie payment
	// (#407 re-review 🟡). Otherwise a user reusing a slug they
	// already own would have their new payment adopted onto the
	// pre-existing project by the webhook's find-or-create branch.
	// projects.slug is globally UNIQUE, so any hit here (regardless
	// of owner) blocks the checkout — user picks a different name.
	var slugTaken bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM public.projects WHERE slug = $1)`,
		req.Slug,
	).Scan(&slugTaken)
	if err != nil {
		return nil, fmt.Errorf("billing: slug availability check: %w", err)
	}
	if slugTaken {
		return nil, ErrSlugTaken
	}

	price, err := s.resolvePriceCents(ctx, req.Plan)
	if err != nil {
		return nil, err
	}

	// 1. Acquire per-owner advisory lock in a short tx that
	// covers the guard-check + INSERT (#407 review 🔴). Without
	// this, two racing requests both see "no in-flight row" and
	// both INSERT (the mollie_payment_id is set later, so the
	// original guard's IS NOT NULL filter can't see either).
	// The partial unique index on (owner_id) WHERE
	// mollie_payment_id IS NULL is the DB-level backstop; the
	// advisory lock avoids surfacing 23505 to the user in the
	// common case.
	lockTx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("billing: begin lock tx: %w", err)
	}
	defer func() { _ = lockTx.Rollback(ctx) }()

	if _, err := lockTx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1::text))`,
		userID,
	); err != nil {
		return nil, fmt.Errorf("billing: acquire per-owner lock: %w", err)
	}

	// Reclaim stale unresolved rows for THIS owner before the
	// guard check (#407 re-review 🟢). If a prior request
	// crashed after the lock-tx committed but before the Mollie
	// call set mollie_payment_id, the row lingers as NULL-payment
	// until expires_at (24h default), self-locking the user out
	// of new-project checkouts. A NULL-payment row older than
	// the cooldown window means Mollie was never successfully
	// called (a successful call sets payment_id in seconds), so
	// deleting it is safe — no external state references it.
	if _, err := lockTx.Exec(ctx,
		`DELETE FROM public.pending_projects
		  WHERE owner_id = $1::uuid
		    AND mollie_payment_id IS NULL
		    AND created_at < now() - $2::interval`,
		userID, fmt.Sprintf("%d seconds", int(pendingCheckoutCooldown.Seconds())),
	); err != nil {
		return nil, fmt.Errorf("billing: reclaim stale unresolved rows: %w", err)
	}

	// Guard: check for BOTH shapes of in-flight row inside the
	// lock. Resolved-and-recent → return existing URL (idempotent
	// re-click). Unresolved-any (mollie_payment_id NULL) → another
	// request just INSERTed but hasn't set the payment ID yet →
	// treat as in-flight.
	var existingID string
	var existingPaymentID *string
	err = lockTx.QueryRow(ctx,
		`SELECT id, mollie_payment_id FROM public.pending_projects
		  WHERE owner_id = $1::uuid
		    AND (
		        (mollie_payment_id IS NOT NULL AND created_at > now() - $2::interval)
		     OR (mollie_payment_id IS NULL AND expires_at > now())
		    )
		  ORDER BY created_at DESC LIMIT 1`,
		userID, fmt.Sprintf("%d seconds", int(pendingCheckoutCooldown.Seconds())),
	).Scan(&existingID, &existingPaymentID)
	if err == nil {
		if existingPaymentID != nil {
			// Resolved recent: re-fetch and return existing URL.
			// Drop the lock tx before the external call.
			_ = lockTx.Rollback(ctx)
			existingPayment, perr := s.client.GetPayment(ctx, *existingPaymentID)
			if perr == nil && existingPayment.Links.Checkout != nil && existingPayment.Links.Checkout.Href != "" {
				return &NewProjectCheckoutResult{
					PendingProjectID: existingID,
					CheckoutURL:      existingPayment.Links.Checkout.Href,
				}, nil
			}
			slog.Warn("billing: found in-flight pending row but couldn't re-fetch Mollie checkout URL — surfacing as in-flight",
				"user_id", userID, "existing_pending_id", existingID, "error", perr)
			return nil, ErrPendingCheckoutInFlight
		}
		// Unresolved row exists — a concurrent request is mid-
		// checkout. The user should wait a few seconds and retry.
		return nil, ErrPendingCheckoutInFlight
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("billing: pending-project in-flight check: %w", err)
	}

	// 2. INSERT under the same lock. The partial unique index
	// backstops a lost-lock scenario (pod restart mid-flight);
	// on 23505 we surface ErrPendingCheckoutInFlight.
	var pendingID string
	err = lockTx.QueryRow(ctx,
		`INSERT INTO public.pending_projects
		    (owner_id, name, slug, region, plan)
		 VALUES ($1::uuid, $2, $3, $4, $5)
		 RETURNING id`,
		userID, req.Name, req.Slug, req.Region, req.Plan,
	).Scan(&pendingID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrPendingCheckoutInFlight
		}
		return nil, fmt.Errorf("billing: insert pending_project: %w", err)
	}

	// Commit + release lock BEFORE the Mollie call. Holding the
	// per-owner lock across a Mollie network round-trip would
	// serialize every same-owner request for the full duration
	// of the outbound HTTP call — not what we want, and the
	// partial unique index + guard combination already prevents
	// duplicates.
	if err := lockTx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("billing: commit lock tx: %w", err)
	}

	// 3. Get or create the Mollie customer. Identical to
	// CreateCheckout — stored on platform_users.mollie_customer_id
	// so subsequent checkouts (new-project or upgrade-existing)
	// reuse the same Mollie customer.
	var userEmail string
	var mollieCustomerID *string
	err = s.pool.QueryRow(ctx,
		`SELECT email, mollie_customer_id FROM public.platform_users WHERE id = $1::uuid`,
		userID,
	).Scan(&userEmail, &mollieCustomerID)
	if err != nil {
		s.deletePendingProject(ctx, pendingID)
		return nil, fmt.Errorf("billing: load platform_user: %w", err)
	}
	customerID := ""
	if mollieCustomerID != nil {
		customerID = *mollieCustomerID
	}
	if customerID == "" {
		cust, cerr := s.client.CreateCustomer(ctx, mollie.CustomerCreateRequest{
			Email: userEmail,
			Metadata: map[string]string{
				"platform_user_id": userID,
			},
		}, mollie.WithIdempotencyKey("customer:"+userID))
		if cerr != nil {
			s.deletePendingProject(ctx, pendingID)
			return nil, fmt.Errorf("billing: create mollie customer: %w", cerr)
		}
		customerID = cust.ID
		if _, uerr := s.pool.Exec(ctx,
			`UPDATE public.platform_users SET mollie_customer_id = $1 WHERE id = $2::uuid`,
			customerID, userID,
		); uerr != nil {
			slog.Warn("billing: created Mollie customer but failed to persist ID",
				"user_id", userID, "mollie_customer_id", customerID, "error", uerr)
		}
	}

	// 4. Create the first Mollie payment. Metadata carries
	// pending_project_id so the webhook can resolve back to this
	// row and trigger CreateProject. RedirectURL lands on the
	// projects list with a status marker; the console polls for
	// the newly-created project to appear.
	payment, err := s.client.CreatePayment(ctx, mollie.PaymentCreateRequest{
		Amount:       mollie.AmountFromCents(price, "EUR"),
		Description:  fmt.Sprintf("Eurobase %s — %s (new project)", req.Plan, req.Name),
		RedirectURL:  fmt.Sprintf("%s/projects?status=success&pending=%s", s.config.ConsoleBaseURL, pendingID),
		WebhookURL:   fmt.Sprintf("%s/platform/billing/webhook", s.config.WebhookBaseURL),
		CustomerID:   customerID,
		SequenceType: mollie.SequenceTypeFirst,
		Metadata: map[string]string{
			"pending_project_id": pendingID,
			"plan_code":          req.Plan,
			"owner_id":           userID,
		},
	}, mollie.WithIdempotencyKey("new-project:"+pendingID))
	if err != nil {
		s.deletePendingProject(ctx, pendingID)
		return nil, fmt.Errorf("billing: create mollie payment: %w", err)
	}
	if payment.Links.Checkout == nil || payment.Links.Checkout.Href == "" {
		s.deletePendingProject(ctx, pendingID)
		return nil, fmt.Errorf("billing: mollie returned no checkout URL (payment %s)", payment.ID)
	}

	// 5. Persist the payment ID onto the pending row for webhook
	// idempotency. Same pattern as CreateCheckout: the mollie
	// call is behind us, so the write is safe.
	if _, err := s.pool.Exec(ctx,
		`UPDATE public.pending_projects SET mollie_payment_id = $1 WHERE id = $2`,
		payment.ID, pendingID,
	); err != nil {
		// Non-fatal: the sweeper will retry via the WithIdempotencyKey
		// idempotency guarantee. Log so ops sees the drift.
		slog.Warn("billing: created Mollie payment but failed to persist payment ID on pending_project",
			"pending_project_id", pendingID, "mollie_payment_id", payment.ID, "error", err)
	}

	slog.Info("billing: new-project checkout created",
		"user_id", userID,
		"pending_project_id", pendingID,
		"mollie_payment_id", payment.ID,
		"plan_code", req.Plan,
		"price_cents", price,
	)

	return &NewProjectCheckoutResult{
		PendingProjectID: pendingID,
		CheckoutURL:      payment.Links.Checkout.Href,
	}, nil
}

// deletePendingProject removes a pending_projects row after the
// Mollie call failed (so the sweeper doesn't have to track this
// non-durable state). Logs but does not surface the error — the
// caller is already returning a wrapped error to the user.
func (s *Service) deletePendingProject(ctx context.Context, pendingID string) {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM public.pending_projects WHERE id = $1`, pendingID,
	); err != nil {
		slog.Warn("billing: rollback of pending_project failed — will be swept by cron",
			"pending_project_id", pendingID, "error", err)
	}
}

// rollbackSubscription deletes an 'incomplete' subscription row
// after a downstream failure (Mollie call errored, or Mollie
// returned no checkout URL). Uses status='incomplete' as a guard
// so a webhook that races us can't get its 'active' flip deleted.
// Logs but does not surface the error — the caller is already
// returning a wrapped error to the user, and losing the rollback
// row is preferable to hiding the original failure behind a
// double-fault log line.
func (s *Service) rollbackSubscription(ctx context.Context, subscriptionID string) {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM public.subscriptions
		  WHERE id = $1 AND status = 'incomplete'`,
		subscriptionID,
	); err != nil {
		slog.Warn("billing: rollback of incomplete subscription failed — will be swept by PR 5 cron",
			"subscription_id", subscriptionID, "error", err)
	}
}

// dbPricedPlans is the allow-list of plan codes whose price comes
// from plan_limits.price_cents rather than the planPriceCents map.
// Keeping this explicit means an unknown/typoed plan code fails
// fast without a pool round-trip (see TestHandler_InvalidPlanReturns400
// which passes a nil pool to check the branch).
var dbPricedPlans = map[string]struct{}{
	"team":       {},
	"legal_team": {}, // M2b — reserved so the same code path works when the tier ships
}

// resolvePriceCents returns the price for a plan code, looking up
// plan_limits.price_cents when the plan isn't in the hardcoded
// planPriceCents map AND is on the dbPricedPlans allow-list. Team
// lives in the DB (nullable — NULL during the closed beta), Pro
// lives in the map (kept there so a DB misconfiguration can't
// accidentally move Pro's price).
//
// Returns ErrPlanNotPriced when the DB row has NULL price_cents —
// this is the closed-beta signal that the caller should use the
// RecordBetaGrant path instead of Mollie.
func (s *Service) resolvePriceCents(ctx context.Context, planCode string) (int, error) {
	if price, ok := planPriceCents[planCode]; ok {
		return price, nil
	}
	if _, ok := dbPricedPlans[planCode]; !ok {
		return 0, fmt.Errorf("%w: %q", ErrInvalidPlan, planCode)
	}
	var priceCents *int
	err := s.pool.QueryRow(ctx,
		`SELECT price_cents FROM public.plan_limits WHERE plan = $1`,
		planCode,
	).Scan(&priceCents)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("%w: %q", ErrInvalidPlan, planCode)
	}
	if err != nil {
		return 0, fmt.Errorf("billing: resolve plan price: %w", err)
	}
	if priceCents == nil {
		return 0, ErrPlanNotPriced
	}
	return *priceCents, nil
}

// RecordBetaGrant writes a subscriptions row with status='beta_grant'
// for a project the caller was awarded via the Team-tier closed beta
// admin flow. Same schema shape as a paid subscription so downstream
// UI (list, cancel, downgrade) doesn't need to branch. Zero price;
// no Mollie customer or subscription IDs; no invoice.
//
// Called from tenant.CreateProject when plan=team AND the user has
// team_beta_access. Idempotent — if a live subscription already
// exists for the project the caller gets ErrAlreadySubscribed.
//
// Kept separate from CreateCheckout so the fail-closed
// `BILLING_ENABLED=false` gate on that path never blocks the closed
// beta — beta grants are recorded regardless of the Mollie flag. If
// billing IS enabled, RecordBetaGrant still won't touch Mollie.
func (s *Service) RecordBetaGrant(ctx context.Context, projectID, planCode string) (string, error) {
	if planCode == "" {
		return "", fmt.Errorf("%w: empty plan code", ErrInvalidPlan)
	}
	// The unique partial index on public.subscriptions catches races
	// with a concurrent checkout attempt for the same project — the
	// SQLSTATE 23505 surfaces as ErrAlreadySubscribed.
	var subscriptionID string
	err := s.pool.QueryRow(ctx,
		`INSERT INTO public.subscriptions
		    (project_id, plan, price_cents, currency,
		     billing_interval, status, started_at)
		 VALUES ($1, $2, 0, 'EUR', '1 month', 'beta_grant', now())
		 RETURNING id`,
		projectID, planCode,
	).Scan(&subscriptionID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return "", ErrAlreadySubscribed
		}
		return "", fmt.Errorf("billing: insert beta grant subscription: %w", err)
	}
	slog.Info("billing: beta_grant subscription recorded",
		"project_id", projectID,
		"subscription_id", subscriptionID,
		"plan_code", planCode,
	)
	return subscriptionID, nil
}
