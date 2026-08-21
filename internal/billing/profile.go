package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/jackc/pgx/v5"
)

// ErrProfileNotFound is returned by GetProfile when the platform
// user has no billing_profiles row. Handler translates to 404.
// Deliberately distinct from ErrBillingProfileRequired: the GET
// endpoint uses 404 as the "please fill in the form" signal, but
// the checkout endpoints reject with 409 so the console can
// branch cleanly between "you haven't set one" and "you tried to
// pay without one."
var ErrProfileNotFound = errors.New("billing: profile not found")

// ErrBillingProfileRequired is returned by CreateCheckout and
// NewProjectCheckout when the user has no billing_profiles row.
// Handler returns 409 billing_profile_required so the console
// gate can redirect the user to the profile form.
var ErrBillingProfileRequired = errors.New("billing: profile required before checkout")

// vatNumberRegex matches an EU VAT number shape: 2-letter country
// prefix + 2-12 alphanumerics (uppercase). Does NOT VIES-validate;
// see migration 000106 for the "no VIES until we cross €40k"
// rationale. Kept in sync with the DB CHECK constraint.
var vatNumberRegex = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{2,12}$`)

// countryRegex mirrors the DB CHECK. Two uppercase Latin letters.
// We do NOT enforce ISO 3166 membership here — the list drifts
// (Kosovo, disputed territories) and a wrong 2-letter code hurts
// nobody but the buyer's own accountant. The form on the console
// side is a fixed select, so bad inputs come from API misuse only.
var countryRegex = regexp.MustCompile(`^[A-Z]{2}$`)

// Profile is one row of public.billing_profiles.
type Profile struct {
	ID             string    `json:"id"`
	PlatformUserID string    `json:"platform_user_id"`
	EntityType     string    `json:"entity_type"` // 'individual' | 'business'
	LegalName      string    `json:"legal_name"`
	StreetAddress  string    `json:"street_address"`
	PostalCode     string    `json:"postal_code"`
	City           string    `json:"city"`
	Country        string    `json:"country"` // ISO 3166-1 alpha-2, uppercase
	RegistryCode   string    `json:"registry_code,omitempty"`
	VATNumber      string    `json:"vat_number,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ProfileInput is the write shape for UpsertProfile. Normalise()
// uppercases country/vat_number in place; Validate() rejects any
// row the DB CHECK constraints would reject, plus the code-side
// "business + EE ⇒ registry_code required" rule.
type ProfileInput struct {
	EntityType    string `json:"entity_type"`
	LegalName     string `json:"legal_name"`
	StreetAddress string `json:"street_address"`
	PostalCode    string `json:"postal_code"`
	City          string `json:"city"`
	Country       string `json:"country"`
	RegistryCode  string `json:"registry_code"`
	VATNumber     string `json:"vat_number"`
}

// Normalise trims whitespace on every field, uppercases country
// and vat_number, and collapses "empty registry / vat" to the
// zero value so we can UPSERT with real NULLs on the DB side.
// Called by both the handler and the upsert path so a direct
// service caller can't skip it.
func (in *ProfileInput) Normalise() {
	in.EntityType = strings.ToLower(strings.TrimSpace(in.EntityType))
	in.LegalName = strings.TrimSpace(in.LegalName)
	in.StreetAddress = strings.TrimSpace(in.StreetAddress)
	in.PostalCode = strings.TrimSpace(in.PostalCode)
	in.City = strings.TrimSpace(in.City)
	in.Country = strings.ToUpper(strings.TrimSpace(in.Country))
	in.RegistryCode = strings.TrimSpace(in.RegistryCode)
	in.VATNumber = strings.ToUpper(strings.TrimSpace(in.VATNumber))
}

// ProfileValidationError wraps a field-scoped rejection so the
// handler can echo `{"field": "...", "error": "...", "code":
// "invalid_field"}` and the console can highlight the exact input
// that failed.
type ProfileValidationError struct {
	Field   string
	Message string
}

func (e *ProfileValidationError) Error() string {
	return fmt.Sprintf("billing profile: %s: %s", e.Field, e.Message)
}

// Validate checks the input against the DB CHECK constraints plus
// the code-only "EE business ⇒ registry_code required" rule.
// Assumes Normalise has already run — caller's responsibility.
//
// Uses utf8.RuneCountInString instead of len() so the bounds
// match Postgres length() (which counts characters, not bytes).
// Without this, "OÜ" (2 chars, 3 bytes) at the 2-char lower
// bound would pass Go's byte-count but violate the DB CHECK and
// surface as an opaque 500 instead of a clean 400 invalid_field.
func (in *ProfileInput) Validate() error {
	if in.EntityType != "individual" && in.EntityType != "business" {
		return &ProfileValidationError{Field: "entity_type", Message: "must be 'individual' or 'business'"}
	}
	if n := utf8.RuneCountInString(in.LegalName); n < 2 || n > 200 {
		return &ProfileValidationError{Field: "legal_name", Message: "must be 2–200 characters"}
	}
	if n := utf8.RuneCountInString(in.StreetAddress); n < 2 || n > 200 {
		return &ProfileValidationError{Field: "street_address", Message: "must be 2–200 characters"}
	}
	if n := utf8.RuneCountInString(in.PostalCode); n < 1 || n > 20 {
		return &ProfileValidationError{Field: "postal_code", Message: "must be 1–20 characters"}
	}
	if n := utf8.RuneCountInString(in.City); n < 1 || n > 100 {
		return &ProfileValidationError{Field: "city", Message: "must be 1–100 characters"}
	}
	if !countryRegex.MatchString(in.Country) {
		return &ProfileValidationError{Field: "country", Message: "must be an ISO 3166-1 alpha-2 country code (e.g. EE, DE, FR)"}
	}
	if in.RegistryCode != "" {
		if n := utf8.RuneCountInString(in.RegistryCode); n < 2 || n > 40 {
			return &ProfileValidationError{Field: "registry_code", Message: "must be 2–40 characters"}
		}
	}
	if in.VATNumber != "" {
		if !vatNumberRegex.MatchString(in.VATNumber) {
			return &ProfileValidationError{Field: "vat_number", Message: "must be a valid EU VAT number (e.g. EE123456789)"}
		}
	}
	// EE business customers file with the Estonian Business Register;
	// the registry code (registrikood) is standard on invoices there.
	// Not enforced for other countries — the norm varies.
	if in.EntityType == "business" && in.Country == "EE" && in.RegistryCode == "" {
		return &ProfileValidationError{Field: "registry_code", Message: "required for Estonian businesses (registrikood)"}
	}
	return nil
}

// GetProfile loads the billing profile for a platform user.
// Returns ErrProfileNotFound if none exists.
func (s *Service) GetProfile(ctx context.Context, userID string) (*Profile, error) {
	p := &Profile{}
	var (
		registryCode *string
		vatNumber    *string
	)
	// PII path — MUST use the developer pool. Migration 000106
	// REVOKEs public.billing_profiles from eurobase_gateway, so
	// running this on the gateway pool would 42501 in prod.
	err := s.pii().QueryRow(ctx,
		`SELECT id::text, platform_user_id::text, entity_type, legal_name,
		        street_address, postal_code, city, country,
		        registry_code, vat_number, created_at, updated_at
		   FROM public.billing_profiles
		  WHERE platform_user_id = $1::uuid`,
		userID,
	).Scan(&p.ID, &p.PlatformUserID, &p.EntityType, &p.LegalName,
		&p.StreetAddress, &p.PostalCode, &p.City, &p.Country,
		&registryCode, &vatNumber, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProfileNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("billing: load profile: %w", err)
	}
	if registryCode != nil {
		p.RegistryCode = *registryCode
	}
	if vatNumber != nil {
		p.VATNumber = *vatNumber
	}
	return p, nil
}

// UpsertProfile writes the billing profile. Normalise + Validate
// run inside so a direct service caller can't skip them. Uses
// INSERT ... ON CONFLICT ON THE UNIQUE (platform_user_id) so
// concurrent PUTs from a double-click land on one row.
func (s *Service) UpsertProfile(ctx context.Context, userID string, in ProfileInput) (*Profile, error) {
	in.Normalise()
	if err := in.Validate(); err != nil {
		return nil, err
	}

	var (
		registryCode any
		vatNumber    any
	)
	if in.RegistryCode == "" {
		registryCode = nil
	} else {
		registryCode = in.RegistryCode
	}
	if in.VATNumber == "" {
		vatNumber = nil
	} else {
		vatNumber = in.VATNumber
	}

	p := &Profile{}
	var (
		outRegistry *string
		outVAT      *string
	)
	// PII path — see GetProfile comment for the pool rationale.
	err := s.pii().QueryRow(ctx,
		`INSERT INTO public.billing_profiles
		    (platform_user_id, entity_type, legal_name, street_address,
		     postal_code, city, country, registry_code, vat_number)
		 VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9)
		 ON CONFLICT (platform_user_id) DO UPDATE
		    SET entity_type    = EXCLUDED.entity_type,
		        legal_name     = EXCLUDED.legal_name,
		        street_address = EXCLUDED.street_address,
		        postal_code    = EXCLUDED.postal_code,
		        city           = EXCLUDED.city,
		        country        = EXCLUDED.country,
		        registry_code  = EXCLUDED.registry_code,
		        vat_number     = EXCLUDED.vat_number
		 RETURNING id::text, platform_user_id::text, entity_type, legal_name,
		           street_address, postal_code, city, country,
		           registry_code, vat_number, created_at, updated_at`,
		userID, in.EntityType, in.LegalName, in.StreetAddress,
		in.PostalCode, in.City, in.Country, registryCode, vatNumber,
	).Scan(&p.ID, &p.PlatformUserID, &p.EntityType, &p.LegalName,
		&p.StreetAddress, &p.PostalCode, &p.City, &p.Country,
		&outRegistry, &outVAT, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("billing: upsert profile: %w", err)
	}
	if outRegistry != nil {
		p.RegistryCode = *outRegistry
	}
	if outVAT != nil {
		p.VATNumber = *outVAT
	}
	return p, nil
}

// requireProfile is the pre-checkout guard. Wrapped so CreateCheckout
// and NewProjectCheckout share the same error shape.
func (s *Service) requireProfile(ctx context.Context, userID string) error {
	_, err := s.GetProfile(ctx, userID)
	if errors.Is(err, ErrProfileNotFound) {
		return ErrBillingProfileRequired
	}
	return err
}

// ── HTTP handlers ──────────────────────────────────────────────

// HandleGetBillingProfile is GET /platform/billing/profile.
// 200 + profile JSON on success; 404 profile_not_found when the
// row is absent (the console distinguishes "never set" from
// "network error" via the code). Unlike the checkout handlers,
// this endpoint works even when BILLING_ENABLED is false so the
// user can pre-fill the form before the launch flip.
func HandleGetBillingProfile(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok || claims == nil {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		p, err := svc.GetProfile(r.Context(), claims.Subject)
		if err != nil {
			if errors.Is(err, ErrProfileNotFound) {
				writeJSONError(w, http.StatusNotFound, "profile_not_found", "no billing profile on file")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to load billing profile")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p)
	}
}

// HandleUpsertBillingProfile is PUT /platform/billing/profile.
// 200 + stored profile on success; 400 invalid_field with the
// exact field name on validation error. Also works when billing
// is disabled — same pre-fill rationale as the GET handler.
func HandleUpsertBillingProfile(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok || claims == nil {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		var in ProfileInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_body", "request body must be JSON")
			return
		}
		p, err := svc.UpsertProfile(r.Context(), claims.Subject, in)
		if err != nil {
			var vErr *ProfileValidationError
			if errors.As(err, &vErr) {
				writeJSONFieldError(w, http.StatusBadRequest, "invalid_field", vErr.Message, vErr.Field)
				return
			}
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to save billing profile")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p)
	}
}

// writeJSONFieldError extends writeJSONError with a `field` key
// so a validation error can name the input the console should
// highlight. Envelope: {"error": <human>, "code": <machine>,
// "field": <json field name>}.
func writeJSONFieldError(w http.ResponseWriter, status int, code, message, field string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": message,
		"code":  code,
		"field": field,
	})
}
