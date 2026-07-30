// Package mollie is a thin REST client for the Mollie Payments API.
//
// It exposes only the endpoints Eurobase billing needs: Customers
// (create + get), Subscriptions (create + get + cancel), Payments
// (get) and Refunds (create). Everything else Mollie offers is out of
// scope for now — the design goal is a small, auditable surface so the
// billing state machine (PR 4) has a predictable dependency.
//
// The API is documented at https://docs.mollie.com/reference. Two
// authentication modes exist: a test key (starts with test_) and a
// live key (starts with live_). Which one the client uses is
// controlled by NewClient's cfg.Env field; the token itself is opaque
// to this package.
//
// Mollie's REST responses are JSON with a _links envelope for HATEOAS
// traversal. We only decode the fields billing needs; unknown fields
// are silently ignored so a Mollie API-side addition doesn't break
// deserialisation.
package mollie

import (
	"log/slog"
	"time"
)

// Amount is Mollie's standard money envelope: currency + decimal
// string. Kept as a string (not float) to avoid IEEE-754 rounding on
// invoice totals. Callers convert to/from integer cents via
// AmountFromCents / (Amount).Cents.
type Amount struct {
	Currency string `json:"currency"`
	Value    string `json:"value"`
}

// SequenceType controls Mollie's recurring-payment model. `first` is a
// one-off charge that also captures a mandate for future recurring;
// `recurring` reuses that mandate to charge without customer
// interaction; `oneoff` is a plain non-recurring charge. Eurobase uses
// first at checkout and recurring for renewals.
type SequenceType string

const (
	SequenceTypeFirst     SequenceType = "first"
	SequenceTypeRecurring SequenceType = "recurring"
	SequenceTypeOneoff    SequenceType = "oneoff"
)

// Customer maps to Mollie's /v2/customers resource. Metadata is a
// free-form JSON object; Eurobase stashes {platform_user_id, email}
// there so a Mollie-dashboard operator can trace a customer back to a
// row in billing_customers without cross-referencing.
type Customer struct {
	ID        string            `json:"id"`
	Mode      string            `json:"mode"`
	Name      string            `json:"name,omitempty"`
	Email     string            `json:"email,omitempty"`
	Locale    string            `json:"locale,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
}

// CustomerCreateRequest is the POST /v2/customers body.
type CustomerCreateRequest struct {
	Name     string            `json:"name,omitempty"`
	Email    string            `json:"email,omitempty"`
	Locale   string            `json:"locale,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Subscription maps to Mollie's /v2/customers/:id/subscriptions
// resource. NextPaymentDate is the ISO-8601 date Mollie will attempt
// the next recurring charge; the billing state machine (PR 4) mirrors
// it into subscriptions.next_charge_at for cron-based reasoning.
type Subscription struct {
	ID              string            `json:"id"`
	Mode            string            `json:"mode"`
	CustomerID      string            `json:"customerId"`
	Status          string            `json:"status"`
	Amount          Amount            `json:"amount"`
	Times           *int              `json:"times,omitempty"`
	TimesRemaining  *int              `json:"timesRemaining,omitempty"`
	Interval        string            `json:"interval"`
	StartDate       string            `json:"startDate,omitempty"`
	NextPaymentDate string            `json:"nextPaymentDate,omitempty"`
	Description     string            `json:"description"`
	Method          string            `json:"method,omitempty"`
	WebhookURL      string            `json:"webhookUrl,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	CreatedAt       time.Time         `json:"createdAt"`
	CanceledAt      *time.Time        `json:"canceledAt,omitempty"`
}

// SubscriptionCreateRequest is the POST body for creating a
// subscription on an existing customer. Interval is a Mollie-native
// string ("1 month", "3 months"); Eurobase always uses "1 month" for
// the Pro tier.
type SubscriptionCreateRequest struct {
	Amount      Amount            `json:"amount"`
	Interval    string            `json:"interval"`
	Description string            `json:"description"`
	StartDate   string            `json:"startDate,omitempty"`
	WebhookURL  string            `json:"webhookUrl,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Method      string            `json:"method,omitempty"`
	Times       *int              `json:"times,omitempty"`
}

// Payment is the /v2/payments resource. Eurobase reads this in the
// webhook handler (PR 4) to derive canonical state from Mollie
// (Mollie's webhook body only contains {id}; the actual state is
// fetched by GET-ing the payment).
type Payment struct {
	ID              string            `json:"id"`
	Mode            string            `json:"mode"`
	Status          string            `json:"status"`
	Amount          Amount            `json:"amount"`
	AmountRefunded  *Amount           `json:"amountRefunded,omitempty"`
	Description     string            `json:"description"`
	Method          string            `json:"method,omitempty"`
	CustomerID      string            `json:"customerId,omitempty"`
	SubscriptionID  string            `json:"subscriptionId,omitempty"`
	MandateID       string            `json:"mandateId,omitempty"`
	SequenceType    SequenceType      `json:"sequenceType"`
	CreatedAt       time.Time         `json:"createdAt"`
	PaidAt          *time.Time        `json:"paidAt,omitempty"`
	FailedAt        *time.Time        `json:"failedAt,omitempty"`
	CanceledAt      *time.Time        `json:"canceledAt,omitempty"`
	ExpiredAt       *time.Time        `json:"expiredAt,omitempty"`
	Locale          string            `json:"locale,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	WebhookURL      string            `json:"webhookUrl,omitempty"`
	SettlementAmount *Amount          `json:"settlementAmount,omitempty"`
	Details          map[string]any   `json:"details,omitempty"`
	Links            PaymentLinks     `json:"_links"`
}

// PaymentLinks carries Mollie's HATEOAS follow-up URLs. Eurobase only
// consumes Checkout — the URL the user must visit to complete a first
// payment (SEPA mandate signature or card 3DS). Pointer type so
// omitempty actually elides the field for recurring payments where
// Mollie omits the checkout link entirely.
type PaymentLinks struct {
	Checkout *LinkObject `json:"checkout,omitempty"`
}

// LinkObject is Mollie's canonical link envelope.
type LinkObject struct {
	Href string `json:"href"`
	Type string `json:"type,omitempty"`
}

// RefundCreateRequest is the POST body for refunding a payment.
// Description is user-visible on the Mollie dashboard; keep it short.
type RefundCreateRequest struct {
	Amount      Amount            `json:"amount"`
	Description string            `json:"description,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// Refund is the /v2/payments/:id/refunds resource.
type Refund struct {
	ID          string            `json:"id"`
	Amount      Amount            `json:"amount"`
	Status      string            `json:"status"`
	Description string            `json:"description,omitempty"`
	PaymentID   string            `json:"paymentId"`
	CreatedAt   time.Time         `json:"createdAt"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// AmountFromCents converts an integer eurocents value into Mollie's
// {currency, value} envelope. Value is always a fixed two-decimal
// string ("19.00"), which is what Mollie's validator expects for EUR.
func AmountFromCents(cents int, currency string) Amount {
	if currency == "" {
		currency = "EUR"
	}
	whole := cents / 100
	frac := cents % 100
	if frac < 0 {
		// Guard against a caller passing negative cents; the string
		// formatter below would emit "-1.-9" without this.
		whole, frac = 0, 0
	}
	return Amount{
		Currency: currency,
		Value:    formatCents(whole, frac),
	}
}

// Cents parses Mollie's decimal-string value back to integer
// eurocents. Returns 0 on parse failure so that a malformed webhook
// body doesn't panic the state machine; the failure is slog-warned
// with the offending value so operators can trace the bad input.
// Callers that care about precision should re-fetch from Mollie's
// API rather than trust the webhook echo.
//
// Supported shapes: "0.00", "19.00", "19.5" (single decimal, promoted
// to 1950), "0" (whole only). Rejected: negative signs, more than
// two decimals, letters, empty string. Mollie returns exactly two
// decimals for EUR in every observed response so anything else is
// either operator-injected test data or a Mollie API-side change
// worth an ops alert.
func (a Amount) Cents() int {
	if a.Value == "" {
		return 0 // Empty value is a common "not applicable" from optional Amount fields (e.g. amountRefunded) — no warn.
	}
	whole, frac := 0, 0
	sawDot := false
	fracDigits := 0
	for i := 0; i < len(a.Value); i++ {
		ch := a.Value[i]
		switch {
		case ch == '.':
			if sawDot {
				slog.Warn("mollie: Amount.Cents parse failure, multiple dots", "value", a.Value)
				return 0
			}
			sawDot = true
		case ch >= '0' && ch <= '9':
			if sawDot {
				if fracDigits >= 2 {
					// More than two decimal places — Mollie
					// doesn't emit these for EUR. Rather than
					// silently truncate we warn and refuse.
					slog.Warn("mollie: Amount.Cents parse failure, more than 2 decimals", "value", a.Value)
					return 0
				}
				frac = frac*10 + int(ch-'0')
				fracDigits++
			} else {
				whole = whole*10 + int(ch-'0')
			}
		default:
			slog.Warn("mollie: Amount.Cents parse failure, unexpected char", "value", a.Value, "char", string(ch))
			return 0
		}
	}
	// Promote "19.5" (1 decimal) to 1950. Two-decimal input is the
	// common case and needs no adjustment.
	if sawDot && fracDigits == 1 {
		frac *= 10
	}
	return whole*100 + frac
}

// formatCents renders the (whole, frac) pair as "W.FF" with a leading
// zero on frac. Kept package-private; AmountFromCents is the only
// caller.
func formatCents(whole, frac int) string {
	buf := make([]byte, 0, 8)
	if whole == 0 {
		buf = append(buf, '0')
	} else {
		// Reverse-append digits.
		tmp := make([]byte, 0, 8)
		for whole > 0 {
			tmp = append(tmp, byte('0'+whole%10))
			whole /= 10
		}
		for i := len(tmp) - 1; i >= 0; i-- {
			buf = append(buf, tmp[i])
		}
	}
	buf = append(buf, '.')
	buf = append(buf, byte('0'+frac/10))
	buf = append(buf, byte('0'+frac%10))
	return string(buf)
}
