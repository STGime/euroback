package enduser

import (
	"errors"
	"time"
)

// ErrEmailNotConfirmed is returned by sign-in, refresh and passkey sign-in
// when the project requires email confirmation and the user hasn't
// confirmed yet (#806). Sign-in only returns it after the correct
// password; handlers answer 403 {"error":"email_not_confirmed"}.
var ErrEmailNotConfirmed = errors.New("email_not_confirmed")

// ErrInvalidRefreshToken is returned by RefreshToken when the token is
// unknown, revoked or expired — the only refresh failure answered with 401.
// The SDK signs the user out on 400/401/403 and keeps the session (and
// retries) on anything else (#814), so the gateway's own failures
// (database down, …) must not be reported as 401.
var ErrInvalidRefreshToken = errors.New("invalid or expired refresh token")

// ErrAccountSuspended is returned by RefreshToken when the user is banned.
// Answered 403 so the SDK ends the session (not a retryable 5xx).
var ErrAccountSuspended = errors.New("account_suspended")

// maxPasswordHashCost is the highest bcrypt cost a stored end-user hash
// may have — the platform hashes at 12, Supabase at 10. Imports refuse
// higher ones, and sign-in won't evaluate them.
const maxPasswordHashCost = 12

// emailConfirmationPending is the one rule every session-issuing path
// applies (#806): with confirmation required, a user who has a password
// and hasn't confirmed their email gets no session. Users without a
// password (OAuth, magic link, phone, passkey-only) are confirmed by how
// they sign in, or have no email to confirm.
func emailConfirmationPending(requireConfirmation, hasPassword bool, confirmedAt *time.Time) bool {
	return requireConfirmation && hasPassword && confirmedAt == nil
}

// SignUpRequest is the JSON body for end-user sign-up.
type SignUpRequest struct {
	Email    string                 `json:"email"`
	Password string                 `json:"password"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`

	// EmailRedirectTo overrides auth_config.email_verification_url
	// for this signup only (#258, part of #257). Must be a member of
	// auth_config.redirect_urls or the signup fails 400. Empty →
	// resolver falls back to auth_config.email_verification_url and
	// eventually 400 if that's also empty and email confirmation is
	// required.
	EmailRedirectTo string `json:"email_redirect_to,omitempty"`
}

// SignInRequest is the JSON body for end-user sign-in.
type SignInRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// RefreshRequest is the JSON body for token refresh.
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// AuthResponse is returned after successful sign-up, sign-in, or refresh.
// A sign-up that must confirm its email first carries only User and
// ConfirmationRequired — no tokens (#806).
type AuthResponse struct {
	AccessToken          string `json:"access_token,omitempty"`
	TokenType            string `json:"token_type,omitempty"`
	ExpiresIn            int    `json:"expires_in,omitempty"`
	RefreshToken         string `json:"refresh_token,omitempty"`
	User                 User   `json:"user"`
	ConfirmationRequired bool   `json:"confirmation_required,omitempty"`
}

// User represents an end-user in a tenant schema.
type User struct {
	ID          string                 `json:"id"`
	Email       *string                `json:"email,omitempty"`
	Phone       *string                `json:"phone,omitempty"`
	DisplayName *string                `json:"display_name,omitempty"`
	AvatarURL   *string                `json:"avatar_url,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
	// EmailConfirmedAt is null until the user confirms their email
	// (always set for OAuth, magic-link and admin-created users).
	EmailConfirmedAt *time.Time `json:"email_confirmed_at"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// EmailString returns the email as a plain string, or empty if nil.
func (u *User) EmailString() string {
	if u.Email != nil {
		return *u.Email
	}
	return ""
}

// SendPhoneOTPRequest is the JSON body for phone OTP initiation.
type SendPhoneOTPRequest struct {
	Phone string `json:"phone"`
}

// VerifyPhoneOTPRequest is the JSON body for phone OTP verification.
type VerifyPhoneOTPRequest struct {
	Phone string `json:"phone"`
	Code  string `json:"code"`
}
