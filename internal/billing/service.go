package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pro plan price in EUR. Hard-coded for v1 because plan_limits doesn't
// carry a price column (migration 000017). A follow-up migration can
// move this into the DB once the Pro tier ships and a Team tier
// shows up — until then, one constant is fewer moving parts than a
// half-populated config table.
const (
	PlanPro          = "pro"
	PlanFree         = "free"
	ProPriceEUR      = "9.00"
	ProAmountCents   = 900
	ProDescription   = "Eurobase Pro — monthly subscription"
	GracePeriodHours = 72 // 3 days

	// Subscription rows go through these status values. The naming
	// matches Mollie's where they overlap (active, cancelled) and
	// invents the rest to carry our state machine that Mollie has
	// no opinion on (pending_payment, grace, pro_until_period_end).
	StatusPending             = "pending_payment"
	StatusActive              = "active"
	StatusGrace               = "grace"
	StatusProUntilPeriodEnd   = "pro_until_period_end"
	StatusCancelled           = "cancelled"
)

// Service is the billing facade. Construct with NewService; methods
// are safe for concurrent use because each call opens its own
// short-lived DB transaction.
type Service struct {
	pool   *pgxpool.Pool
	mollie *MollieClient
	audit  *audit.Service

	// Public-facing base URL the customer's browser bounces back to
	// after Mollie checkout. Populated from CONSOLE_URL env at boot.
	consoleURL string
	// Public URL Mollie POSTs webhooks to. Populated from
	// PUBLIC_GATEWAY_URL env at boot.
	webhookURL string

	// Optional: materialiser for pending_projects rows. Wired by the
	// gateway after both packages are constructed (avoids a
	// tenant→billing dependency cycle). nil disables the
	// pending-projects branch in ApplyPaymentEvent.
	provisionPending ProjectProvisioner
}

func NewService(pool *pgxpool.Pool, mollie *MollieClient, auditSvc *audit.Service, consoleURL, webhookURL string) *Service {
	return &Service{
		pool:       pool,
		mollie:     mollie,
		audit:      auditSvc,
		consoleURL: strings.TrimRight(consoleURL, "/"),
		webhookURL: strings.TrimRight(webhookURL, "/"),
	}
}

// EnsureCustomer returns the Mollie customer ID for the given
// platform user, creating one in Mollie + writing it back to
// platform_users.mollie_customer_id if it doesn't exist yet. The
// operation is idempotent — two concurrent calls for the same user
// both end up with the same final mollie_customer_id; the second
// caller sees it cached.
func (s *Service) EnsureCustomer(ctx context.Context, platformUserID, email, name string) (string, error) {
	var existing *string
	if err := s.pool.QueryRow(ctx,
		`SELECT mollie_customer_id FROM public.platform_users WHERE id = $1`,
		platformUserID,
	).Scan(&existing); err != nil {
		return "", fmt.Errorf("lookup platform user: %w", err)
	}
	if existing != nil && *existing != "" {
		return *existing, nil
	}

	id, err := s.mollie.CreateCustomer(ctx, email, name)
	if err != nil {
		return "", err
	}

	if _, err := s.pool.Exec(ctx,
		`UPDATE public.platform_users
		 SET mollie_customer_id = $2
		 WHERE id = $1
		   AND (mollie_customer_id IS NULL OR mollie_customer_id = '')`,
		platformUserID, id,
	); err != nil {
		// Don't fail — the customer exists in Mollie, we just couldn't
		// cache the ID. Next call will look it up again.
		slog.Warn("failed to cache mollie_customer_id", "user", platformUserID, "error", err)
	}
	return id, nil
}

// SubscriptionView is what the GET subscription endpoint surfaces.
// Status here is the platform-side status, not Mollie's raw value.
type SubscriptionView struct {
	Plan               string     `json:"plan"`
	Status             string     `json:"status"`
	AmountEUR          string     `json:"amount_eur,omitempty"`
	CurrentPeriodEnd   *time.Time `json:"current_period_end,omitempty"`
	CancelAtPeriodEnd  bool       `json:"cancel_at_period_end"`
	GraceUntil         *time.Time `json:"grace_until,omitempty"`
	MollieSubscription *string    `json:"-"`
}

// GetSubscription returns the platform-side view of the project's
// current subscription state. Free projects with no subscription row
// still get a valid response (just plan=free, no period info).
func (s *Service) GetSubscription(ctx context.Context, projectID string) (*SubscriptionView, error) {
	var v SubscriptionView
	var mollieID *string
	err := s.pool.QueryRow(ctx,
		`SELECT s.plan, s.status, s.current_period_end, s.cancel_at_period_end,
		        s.grace_until, s.mollie_subscription_id
		 FROM public.subscriptions s
		 WHERE s.project_id = $1
		 ORDER BY s.created_at DESC
		 LIMIT 1`,
		projectID,
	).Scan(&v.Plan, &v.Status, &v.CurrentPeriodEnd, &v.CancelAtPeriodEnd, &v.GraceUntil, &mollieID)
	if err == pgx.ErrNoRows {
		// No subscription row → genuinely on free. The projects.plan
		// column is the authoritative tier flag; subscription is the
		// audit trail of how that tier was set.
		return &SubscriptionView{Plan: PlanFree, Status: PlanFree}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load subscription: %w", err)
	}
	if v.Plan == PlanPro {
		v.AmountEUR = ProPriceEUR
	}
	v.MollieSubscription = mollieID
	return &v, nil
}

// StartUpgradeForPending creates a Mollie first payment that, once
// paid, materialises a brand-new project from a pending_projects row.
// Closes #70 — Pro-at-signup used to silently downgrade to free
// because the project was created before the user had paid for it.
//
// Returns the Mollie payment ID + checkout URL. The caller is
// responsible for storing the payment ID on the pending_projects row
// (we don't take a DB tx here — the handler does it).
func (s *Service) StartUpgradeForPending(ctx context.Context, pendingProjectID, ownerID, ownerEmail, ownerName, plan string) (paymentID, checkoutURL string, err error) {
	if plan != PlanPro {
		return "", "", fmt.Errorf("unsupported plan: %q", plan)
	}
	customerID, err := s.EnsureCustomer(ctx, ownerID, ownerEmail, ownerName)
	if err != nil {
		return "", "", fmt.Errorf("ensure customer: %w", err)
	}
	payment, err := s.mollie.CreateFirstPayment(ctx, FirstPaymentRequest{
		AmountEUR:   ProPriceEUR,
		CustomerID:  customerID,
		RedirectURL: fmt.Sprintf("%s/projects?status=pending_provision", s.consoleURL),
		WebhookURL:  fmt.Sprintf("%s/webhooks/mollie", s.webhookURL),
		Description: ProDescription,
		Metadata: map[string]string{
			"pending_project_id": pendingProjectID,
			"plan":               plan,
		},
	})
	if err != nil {
		return "", "", err
	}
	return payment.ID, payment.Links.Checkout.Href, nil
}

// StartUpgrade creates a Mollie first payment for the project's
// owner and returns the checkout URL. It also writes a
// pending_payment subscription row so the webhook handler can find
// the project by Mollie payment ID later.
//
// Today only Pro is supported; the plan argument is kept to make
// future Team / Enterprise tiers a one-line addition.
func (s *Service) StartUpgrade(ctx context.Context, projectID, ownerID, ownerEmail, ownerName, plan string) (string, error) {
	if plan != PlanPro {
		return "", fmt.Errorf("unsupported plan: %q", plan)
	}

	customerID, err := s.EnsureCustomer(ctx, ownerID, ownerEmail, ownerName)
	if err != nil {
		return "", fmt.Errorf("ensure customer: %w", err)
	}

	payment, err := s.mollie.CreateFirstPayment(ctx, FirstPaymentRequest{
		AmountEUR:   ProPriceEUR,
		CustomerID:  customerID,
		RedirectURL: fmt.Sprintf("%s/p/%s/billing?status=return", s.consoleURL, projectID),
		WebhookURL:  fmt.Sprintf("%s/webhooks/mollie", s.webhookURL),
		Description: ProDescription,
		Metadata: map[string]string{
			"project_id": projectID,
			"plan":       plan,
		},
	})
	if err != nil {
		return "", err
	}

	// Persist the pending-payment row keyed by the Mollie payment ID so
	// the webhook handler can find the project to upgrade.
	_, err = s.pool.Exec(ctx,
		`INSERT INTO public.subscriptions (project_id, mollie_subscription_id, plan, status, current_period_start)
		 VALUES ($1, NULL, $2, $3, now())
		 ON CONFLICT DO NOTHING`,
		projectID, plan, StatusPending,
	)
	if err != nil {
		slog.Warn("failed to insert pending subscription row", "project", projectID, "error", err)
	}

	if s.audit != nil {
		s.audit.Log(ctx, projectID, ownerID, ownerEmail,
			audit.ActionSubscriptionCreated,
			audit.WithMetadata(map[string]any{
				"plan":           plan,
				"mollie_payment": payment.ID,
				"status":         StatusPending,
			}))
	}

	return payment.Links.Checkout.Href, nil
}

// Cancel flips cancel_at_period_end=true on the active subscription
// and asks Mollie to stop further charges. The customer keeps Pro
// features until current_period_end; the dunning sweeper does the
// actual downgrade.
func (s *Service) Cancel(ctx context.Context, projectID, actorID, actorEmail string) error {
	var mollieCustomerID, mollieSubID *string
	err := s.pool.QueryRow(ctx,
		`SELECT pu.mollie_customer_id, s.mollie_subscription_id
		 FROM public.subscriptions s
		 JOIN public.projects p   ON p.id = s.project_id
		 JOIN public.platform_users pu ON pu.id = p.owner_id
		 WHERE s.project_id = $1 AND s.status IN ($2, $3)
		 ORDER BY s.created_at DESC LIMIT 1`,
		projectID, StatusActive, StatusGrace,
	).Scan(&mollieCustomerID, &mollieSubID)
	if err == pgx.ErrNoRows {
		return fmt.Errorf("no active subscription")
	}
	if err != nil {
		return fmt.Errorf("lookup subscription: %w", err)
	}

	if mollieCustomerID != nil && *mollieCustomerID != "" &&
		mollieSubID != nil && *mollieSubID != "" {
		if err := s.mollie.CancelSubscription(ctx, *mollieCustomerID, *mollieSubID); err != nil {
			return fmt.Errorf("mollie cancel: %w", err)
		}
	}

	_, err = s.pool.Exec(ctx,
		`UPDATE public.subscriptions
		 SET cancel_at_period_end = true, status = $2
		 WHERE project_id = $1 AND status IN ($3, $4)`,
		projectID, StatusProUntilPeriodEnd, StatusActive, StatusGrace,
	)
	if err != nil {
		return fmt.Errorf("flag cancel_at_period_end: %w", err)
	}

	if s.audit != nil {
		s.audit.Log(ctx, projectID, actorID, actorEmail,
			audit.ActionSubscriptionCancelled,
			audit.WithMetadata(map[string]any{
				"effective_until_period_end": true,
			}))
	}
	return nil
}

// ProjectProvisioner is what the billing service calls back into when
// it needs to materialise a pending_projects row into a real
// public.projects row on first-payment-paid (#70). The tenant
// package supplies this so internal/billing doesn't need to depend
// on internal/tenant.
type ProjectProvisioner func(ctx context.Context, pendingProjectID string) (projectID string, err error)

// SetProjectProvisioner wires in the callback used by the
// pending-project materialisation path. Safe to leave unset for
// tests that don't exercise the pending-projects branch.
func (s *Service) SetProjectProvisioner(fn ProjectProvisioner) { s.provisionPending = fn }

// ApplyPaymentEvent is the workhorse of the webhook handler. Given a
// payment ID (which Mollie hands us in the webhook body), it pulls
// the authoritative state from Mollie and reconciles the
// subscription + project plan + audit log. Safe to call multiple
// times for the same payment ID — idempotency comes from
// ON CONFLICT DO NOTHING on invoices and from the status-machine
// transitions being no-ops when the state already matches.
func (s *Service) ApplyPaymentEvent(ctx context.Context, paymentID string) error {
	payment, err := s.mollie.GetPayment(ctx, paymentID)
	if err != nil {
		return fmt.Errorf("fetch payment: %w", err)
	}
	projectID := payment.Metadata["project_id"]
	// Pending-project flow (#70): no project exists yet, materialise
	// from pending_projects when the payment first lands as paid.
	if projectID == "" {
		if pendingID := payment.Metadata["pending_project_id"]; pendingID != "" && payment.Status == "paid" && s.provisionPending != nil {
			created, perr := s.provisionPending(ctx, pendingID)
			if perr != nil {
				return fmt.Errorf("provision pending project: %w", perr)
			}
			projectID = created
			// Delete the pending row now that the real project exists.
			_, _ = s.pool.Exec(ctx, `DELETE FROM public.pending_projects WHERE id = $1`, pendingID)
		} else if pendingID := payment.Metadata["pending_project_id"]; pendingID != "" && (payment.Status == "failed" || payment.Status == "expired" || payment.Status == "canceled") {
			// Customer abandoned checkout or the payment failed before
			// we could provision. Drop the reservation so the slug is
			// freed up for them to try again (or pick a different name).
			_, _ = s.pool.Exec(ctx, `DELETE FROM public.pending_projects WHERE id = $1`, pendingID)
			return nil
		} else {
			return fmt.Errorf("payment %s has no project_id metadata", paymentID)
		}
	}

	// Record the invoice unconditionally — failed / refunded
	// payments still get a row so the operator can see the attempt.
	paidAt := time.Time{}
	if payment.PaidAt != nil {
		paidAt = *payment.PaidAt
	}
	amountCents := euroStringToCents(payment.Amount.Value)
	_, _ = s.pool.Exec(ctx,
		`INSERT INTO public.invoices
		   (project_id, mollie_payment_id, amount_cents, currency, status, paid_at)
		 VALUES ($1, $2, $3, $4, $5, NULLIF($6, '0001-01-01 00:00:00+00'::timestamptz))
		 ON CONFLICT (mollie_payment_id) DO UPDATE
		   SET status = EXCLUDED.status,
		       paid_at = EXCLUDED.paid_at`,
		projectID, payment.ID, amountCents, payment.Amount.Currency,
		payment.Status, paidAt,
	)

	switch payment.Status {
	case "paid":
		return s.handlePaymentPaid(ctx, projectID, payment)
	case "failed", "expired", "canceled":
		return s.handlePaymentFailed(ctx, projectID, payment)
	default:
		// "open" / "pending" — nothing to do; we'll see another
		// webhook when the state moves.
		return nil
	}
}

// handlePaymentPaid promotes the project to Pro. Covers two cases:
// (a) first payment captured a mandate → create the recurring
//     subscription in Mollie + flip status to active.
// (b) subsequent monthly charge → already in active; just re-up the
//     period_end and leave grace_until null (cleared from any prior
//     dunning).
func (s *Service) handlePaymentPaid(ctx context.Context, projectID string, payment *MolliePayment) error {
	mollieSubID := payment.SubscriptionID
	if mollieSubID == "" {
		// First payment — we need to actually create the recurring
		// subscription now. The mandate was captured by sequenceType=first.
		sub, err := s.mollie.CreateSubscription(ctx, payment.CustomerID, ProPriceEUR, ProDescription,
			fmt.Sprintf("%s/webhooks/mollie", s.webhookURL))
		if err != nil {
			slog.Error("create mollie subscription failed after first payment paid",
				"project", projectID, "payment", payment.ID, "error", err)
			return err
		}
		mollieSubID = sub.ID
	}

	periodEnd := time.Now().AddDate(0, 1, 0)
	_, err := s.pool.Exec(ctx,
		`UPDATE public.subscriptions
		 SET status = $2, mollie_subscription_id = $3,
		     current_period_start = now(),
		     current_period_end = $4,
		     grace_until = NULL
		 WHERE project_id = $1 AND status IN ($5, $6, $7)`,
		projectID, StatusActive, mollieSubID, periodEnd,
		StatusPending, StatusGrace, StatusActive,
	)
	if err != nil {
		return fmt.Errorf("update subscription to active: %w", err)
	}

	_, err = s.pool.Exec(ctx,
		`UPDATE public.projects SET plan = $2 WHERE id = $1 AND plan <> $2`,
		projectID, PlanPro,
	)
	if err != nil {
		return fmt.Errorf("flip project plan to pro: %w", err)
	}

	if s.audit != nil {
		md, _ := json.Marshal(map[string]any{
			"mollie_payment":      payment.ID,
			"mollie_subscription": mollieSubID,
			"amount_eur":          payment.Amount.Value,
		})
		s.audit.Log(ctx, projectID, "", "", audit.ActionPaymentSucceeded,
			audit.WithTarget("payment", payment.ID),
			audit.WithMetadata(map[string]any{"raw": string(md)}))
		s.audit.Log(ctx, projectID, "", "", audit.ActionPlanChanged,
			audit.WithMetadata(map[string]any{"from": PlanFree, "to": PlanPro}))
	}
	return nil
}

// handlePaymentFailed starts (or extends) grace. Mollie keeps
// retrying the charge for up to 21 days; we give the customer 3 days
// of grace, then downgrade. The dunning sweeper performs the actual
// downgrade — this function only sets grace_until.
func (s *Service) handlePaymentFailed(ctx context.Context, projectID string, payment *MolliePayment) error {
	graceUntil := time.Now().Add(GracePeriodHours * time.Hour)
	_, err := s.pool.Exec(ctx,
		`UPDATE public.subscriptions
		 SET status = $2, grace_until = $3
		 WHERE project_id = $1 AND status IN ($4, $5)`,
		projectID, StatusGrace, graceUntil, StatusActive, StatusPending,
	)
	if err != nil {
		return fmt.Errorf("flip subscription to grace: %w", err)
	}

	if s.audit != nil {
		s.audit.Log(ctx, projectID, "", "", audit.ActionPaymentFailed,
			audit.WithTarget("payment", payment.ID),
			audit.WithMetadata(map[string]any{
				"grace_until":     graceUntil.Format(time.RFC3339),
				"mollie_payment":  payment.ID,
				"payment_status":  payment.Status,
			}))
	}
	return nil
}

// ── Helpers ────────────────────────────────────────────────────────────────

// euroStringToCents converts Mollie's decimal-string amount ("9.00") to
// integer cents (900). Mollie never returns more than 2 decimals for
// EUR. On a malformed value we return 0 + log; downstream callers
// treat zero amounts as "we recorded the attempt but the auditor will
// need to reconcile against Mollie".
func euroStringToCents(v string) int {
	v = strings.TrimSpace(v)
	parts := strings.SplitN(v, ".", 2)
	if len(parts) == 0 || parts[0] == "" {
		return 0
	}
	whole := 0
	for _, r := range parts[0] {
		if r < '0' || r > '9' {
			slog.Warn("malformed mollie amount", "value", v)
			return 0
		}
		whole = whole*10 + int(r-'0')
	}
	cents := 0
	if len(parts) == 2 {
		fr := parts[1]
		if len(fr) > 2 {
			fr = fr[:2]
		}
		for _, r := range fr {
			if r < '0' || r > '9' {
				slog.Warn("malformed mollie amount", "value", v)
				return 0
			}
			cents = cents*10 + int(r-'0')
		}
		if len(fr) == 1 {
			cents *= 10
		}
	}
	return whole*100 + cents
}
