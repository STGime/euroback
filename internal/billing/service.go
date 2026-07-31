// Package billing wires the Mollie payments client to Eurobase's
// subscription state. PR 3 of the billing stack — CreateCheckout
// only. Webhook state-machine lives in PR 4; downgrade cron in PR 5.
package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

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
)

// planPriceCents is the source of truth for public plan pricing.
// Grandfathering was dropped from the launch scope (see
// docs/billing/stacked-pr-plan.md); every user pays the same
// price. Team is here for schema completeness but the handler
// refuses it until Team billing ships.
var planPriceCents = map[string]int{
	"pro":  1900,  // €19/mo per project
	"team": 14900, // €149/mo per project (not shipped yet)
}

// WebhookMetrics is the surface the webhook handler uses to record
// failure counts. Kept as a tiny interface so tests can inject a
// no-op recorder and the billing package doesn't have to import
// internal/metrics. Nil is safe — every method is a no-op on nil.
type WebhookMetrics interface {
	IncBillingWebhookFailed(resource string)
}

// Service owns the runtime dependencies for the billing HTTP
// surface. Constructed once at server startup and re-used across
// every request — safe because Client, Pool, and the config strings
// are read-only after construction.
type Service struct {
	pool    *pgxpool.Pool
	client  *mollie.Client
	config  Config
	enabled bool
	metrics WebhookMetrics
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

	price, ok := planPriceCents[planCode]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrInvalidPlan, planCode)
	}
	// Guard: Team billing isn't shipped yet. Fail closed rather
	// than accept a plan we don't invoice for.
	if planCode == "team" {
		return nil, fmt.Errorf("%w: team billing not yet available", ErrInvalidPlan)
	}

	// 1. Ownership check. Same query shape as tenant/context.go
	// uses so error semantics match the rest of the platform.
	var projectName string
	err := s.pool.QueryRow(ctx,
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

	_, err = tx.Exec(ctx,
		`INSERT INTO public.invoices
		    (project_id, mollie_payment_id, amount_cents, currency, status)
		 VALUES ($1, $2, $3, 'EUR', 'pending')`,
		projectID, payment.ID, price,
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
