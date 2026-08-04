package dbprovider

import "errors"

// Sentinel errors the workers + gateway branch on. Mirror the shape
// of internal/billing/mollie/errors.go — one sentinel per HTTP
// class, plus a couple of domain-specific ones for the interface.
var (
	// ErrNotFound is returned when the provider 404s a resource we
	// asked about — instance / snapshot deleted on the dashboard,
	// stale ID in our DB, etc. Idempotent operations (Delete) treat
	// this as success.
	ErrNotFound = errors.New("dbprovider: resource not found")

	// ErrUnauthorized is a 401/403 from the provider. Typically
	// means SCW_ACCESS_KEY / SCW_SECRET_KEY is missing or revoked.
	// Non-retryable — pages ops.
	ErrUnauthorized = errors.New("dbprovider: unauthorized")

	// ErrRateLimited is a 429. Callers should back off; the client
	// itself does not retry automatically — retries live in the
	// River worker layer where the retry policy can be composed
	// with the surrounding business logic.
	ErrRateLimited = errors.New("dbprovider: rate limited")

	// ErrInvalidRequest is a 4xx that isn't 401/403/404/429 —
	// typically a validation failure on our request body. Fatal at
	// the caller level; a retry with the same body will just fail
	// again.
	ErrInvalidRequest = errors.New("dbprovider: invalid request")

	// ErrProviderUnavailable is a 5xx or network-level failure from
	// the provider. Callers should retry with backoff.
	ErrProviderUnavailable = errors.New("dbprovider: provider unavailable")

	// ErrInvalidRestoreSource fires when a caller passes a
	// RestoreSource that specifies both or neither of SnapshotID
	// and PITRTarget. Interface contract violation — never seen if
	// the console UI is behaving.
	ErrInvalidRestoreSource = errors.New("dbprovider: restore source must specify exactly one of SnapshotID or PITRTarget")

	// ErrProviderNotRegistered is returned by Registry.Get when the
	// name is unknown. Callers should surface as a 500 (config
	// error), not a 4xx — the name comes from
	// project_databases.provider which our own code sets.
	ErrProviderNotRegistered = errors.New("dbprovider: provider not registered")
)
