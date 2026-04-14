package enduser

import "time"

// SignUpRequest is the JSON body for end-user sign-up.
type SignUpRequest struct {
	Email    string                 `json:"email"`
	Password string                `json:"password"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
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
type AuthResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	User         User   `json:"user"`
}

// User represents an end-user in a tenant schema.
type User struct {
	ID          string                 `json:"id"`
	Email       *string                `json:"email,omitempty"`
	Phone       *string                `json:"phone,omitempty"`
	DisplayName *string                `json:"display_name,omitempty"`
	AvatarURL   *string                `json:"avatar_url,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt   time.Time              `json:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
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
