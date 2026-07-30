package mollie

import (
	"errors"
	"fmt"
)

// Sentinel errors the billing state machine (PR 4) branches on.
// APIError wraps everything else so callers can inspect the Mollie
// response body for debugging without every branch needing its own
// typed error.
var (
	// ErrNotFound is returned by GET endpoints when Mollie responds
	// 404 — typically because a customer/subscription/payment ID
	// referenced in our DB has been deleted from Mollie (rare, but
	// possible in test mode when the dashboard is used to clean up).
	ErrNotFound = errors.New("mollie: resource not found")

	// ErrUnauthorized indicates the API key is missing, revoked, or
	// mismatched to the requested resource's mode (test key against
	// a live-mode resource). The billing service treats this as
	// non-retryable and pages ops.
	ErrUnauthorized = errors.New("mollie: unauthorized")

	// ErrRateLimited is a 429 response. Callers should back off; the
	// client itself does not retry automatically — retries live in
	// the River worker layer where the retry policy can be composed
	// with the surrounding business logic (e.g. don't retry a
	// duplicate charge attempt).
	ErrRateLimited = errors.New("mollie: rate limited")

	// ErrInvalidRequest is a 4xx that isn't one of the above —
	// typically a validation failure on our request body. Fatal at
	// the caller level; a retry with the same body will just fail
	// again.
	ErrInvalidRequest = errors.New("mollie: invalid request")

	// ErrServer is a 5xx from Mollie. Callers should retry with
	// backoff.
	ErrServer = errors.New("mollie: server error")
)

// APIError is the wire-level error envelope Mollie returns on 4xx/5xx
// responses. The Status field is the HTTP status code; Title / Detail
// are Mollie-authored user-facing text. The client attaches Body for
// slog audit trails; callers should not depend on its format.
type APIError struct {
	Status int    `json:"status"`
	Title  string `json:"title,omitempty"`
	Detail string `json:"detail,omitempty"`
	Body   string `json:"-"`
}

func (e *APIError) Error() string {
	if e.Title != "" && e.Detail != "" {
		return fmt.Sprintf("mollie: %d %s — %s", e.Status, e.Title, e.Detail)
	}
	if e.Title != "" {
		return fmt.Sprintf("mollie: %d %s", e.Status, e.Title)
	}
	return fmt.Sprintf("mollie: %d %s", e.Status, e.Body)
}

// classify maps an HTTP status to one of the sentinels above so
// callers can errors.Is() rather than switching on status codes. The
// caller passes an already-decoded *APIError (Title/Detail populated
// from Mollie's JSON envelope when parseable, empty otherwise); this
// function only decides which sentinel to wrap it with, based on
// status. The concrete *APIError is always accessible via errors.As.
func classify(api *APIError) error {
	switch {
	case api.Status == 404:
		return fmt.Errorf("%w: %w", ErrNotFound, api)
	case api.Status == 401 || api.Status == 403:
		return fmt.Errorf("%w: %w", ErrUnauthorized, api)
	case api.Status == 429:
		return fmt.Errorf("%w: %w", ErrRateLimited, api)
	case api.Status >= 400 && api.Status < 500:
		return fmt.Errorf("%w: %w", ErrInvalidRequest, api)
	case api.Status >= 500:
		return fmt.Errorf("%w: %w", ErrServer, api)
	}
	return api
}
