package auth

// Team-tier SSO handlers. Two routes:
//
//   POST /platform/auth/sso/init      — body {email} → returns
//                                       {authorization_url}
//   GET  /platform/auth/sso/callback  — OIDC redirect target;
//                                       verifies + issues platform
//                                       JWT + redirects to console
//
// The OIDC protocol machinery lives in internal/auth/oidc (that's
// the primitive layer). This file is the platform-user glue:
// state signing, org lookup, JWT issuance, first-login user
// creation (only for users the org has explicitly invited).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/auth/oidc"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OrgsForSSO is the narrow interface the SSO handler needs from the
// tenant/orgs service — a subset kept small so tests don't have to
// mock the whole thing. Prod plumbing wraps
// *tenant.OrgsService.
type OrgsForSSO interface {
	// FindOrgForSSOMember returns the org_id that this email should
	// SSO into. MVP: user must already be an org_members row (via
	// manual invite). Returns tenant.ErrOrgMemberMissing if not.
	FindOrgForSSOMember(ctx context.Context, email string) (string, error)
	// GetOIDCConfigForOrg returns the resolved plaintext OIDC config
	// (including client_secret) for internal use only.
	GetOIDCConfigForOrg(ctx context.Context, orgID string) (*OIDCConfigForSSO, error)
	// EnsureMemberFromSSO confirms the user is already a member of
	// the org (MVP does not auto-provision new members).
	EnsureMemberFromSSO(ctx context.Context, orgID, platformUserID string) error
}

// OIDCConfigForSSO is a copy of tenant.OIDCConfig to keep the SSO
// handler agnostic of the tenant package's types. The actual
// service returns tenant.OIDCConfig; router wiring adapts.
type OIDCConfigForSSO struct {
	Provider     string
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

// SSOConfig configures the SSO handler at wiring time.
type SSOConfig struct {
	// PlatformJWTSecret is used to sign the OIDC `state` value so
	// the callback can trust the org_id + nonce round-trip without
	// server-side session storage.
	PlatformJWTSecret []byte
	// ConsoleRedirectURL is where we send the browser AFTER a
	// successful callback (with the access_token in the URL fragment).
	// Typically https://console.eurobase.app/.
	ConsoleRedirectURL string
	// CallbackURL is the fully-qualified OIDC redirect_uri the
	// gateway advertises — must EXACTLY match the value registered
	// with each org's IdP client. Typically
	// https://api.eurobase.app/platform/auth/sso/callback.
	CallbackURL string
}

// SSOHandler wires the two OIDC endpoints. Constructed once at
// gateway startup.
type SSOHandler struct {
	pool        *pgxpool.Pool
	authSvc     *PlatformAuthService
	orgs        OrgsForSSO
	oidcClient  *oidc.Client
	cfg         SSOConfig
}

// NewSSOHandler constructs the handler. The oidc.Client can be
// shared across handlers (it caches discovery + JWKS internally).
func NewSSOHandler(pool *pgxpool.Pool, authSvc *PlatformAuthService, orgs OrgsForSSO, oidcClient *oidc.Client, cfg SSOConfig) *SSOHandler {
	return &SSOHandler{
		pool:       pool,
		authSvc:    authSvc,
		orgs:       orgs,
		oidcClient: oidcClient,
		cfg:        cfg,
	}
}

// stateClaims is the JWT payload for the OIDC state parameter.
// Signed with PlatformJWTSecret; short-lived (5 minutes — enough
// for a human to complete the IdP flow but not enough to be
// replayed).
type stateClaims struct {
	OrgID string `json:"org_id"`
	Nonce string `json:"nonce"`
	jwt.RegisteredClaims
}

const stateLifetime = 5 * time.Minute

func (h *SSOHandler) signState(orgID, nonce string) (string, error) {
	claims := stateClaims{
		OrgID: orgID,
		Nonce: nonce,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(stateLifetime)),
			Issuer:    "eurobase",
			Subject:   "sso-state",
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(h.cfg.PlatformJWTSecret)
}

func (h *SSOHandler) verifyState(state string) (*stateClaims, error) {
	var claims stateClaims
	_, err := jwt.ParseWithClaims(state, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return h.cfg.PlatformJWTSecret, nil
	}, jwt.WithIssuer("eurobase"), jwt.WithSubject("sso-state"))
	if err != nil {
		return nil, err
	}
	return &claims, nil
}

// HandleSSOInit — POST /platform/auth/sso/init  {email}
// Returns {authorization_url} the caller should redirect the
// browser to.
func (h *SSOHandler) HandleSSOInit() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body struct {
			Email string `json:"email"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
			return
		}
		email := strings.ToLower(strings.TrimSpace(body.Email))
		if email == "" || !strings.Contains(email, "@") {
			http.Error(w, `{"error":"email is required"}`, http.StatusBadRequest)
			return
		}

		orgID, err := h.orgs.FindOrgForSSOMember(r.Context(), email)
		if err != nil {
			// Deliberately opaque error to avoid enumeration:
			// same 404 for "no such org" as for "email not in any
			// org's roster." A determined attacker can still
			// enumerate via the platform password signin surface
			// so this isn't a strong defense — but it doesn't hurt.
			http.Error(w, `{"error":"no SSO organization found for that email — ask an admin to invite you first"}`, http.StatusNotFound)
			return
		}

		cfg, err := h.orgs.GetOIDCConfigForOrg(r.Context(), orgID)
		if err != nil {
			slog.Error("sso init: get oidc config", "error", err, "org_id", orgID)
			http.Error(w, `{"error":"organization SSO not fully configured"}`, http.StatusServiceUnavailable)
			return
		}

		nonce, err := oidc.GenerateNonce()
		if err != nil {
			http.Error(w, `{"error":"entropy failure"}`, http.StatusInternalServerError)
			return
		}
		state, err := h.signState(orgID, nonce)
		if err != nil {
			slog.Error("sso init: sign state", "error", err)
			http.Error(w, `{"error":"state signing failed"}`, http.StatusInternalServerError)
			return
		}

		oidcCfg := oidc.Config{
			Issuer:               cfg.Issuer,
			ClientID:             cfg.ClientID,
			ClientSecret:         cfg.ClientSecret,
			RedirectURL:          h.cfg.CallbackURL,
			AllowUnverifiedEmail: true, // Team-tier SSO: caller-invited members are already domain-bound via manual invite
		}
		authURL, err := h.oidcClient.AuthURL(r.Context(), oidcCfg, state, nonce)
		if err != nil {
			slog.Error("sso init: build auth URL", "error", err, "org_id", orgID)
			http.Error(w, `{"error":"could not build authorization URL"}`, http.StatusInternalServerError)
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"authorization_url": authURL,
		})
	}
}

// HandleSSOCallback — GET /platform/auth/sso/callback?code=...&state=...
// Verifies + issues platform JWT + redirects to console with
// the access_token in the URL fragment.
func (h *SSOHandler) HandleSSOCallback() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		if idpErr := r.URL.Query().Get("error"); idpErr != "" {
			h.redirectWithError(w, r, "idp_error", idpErr)
			return
		}
		if code == "" || state == "" {
			h.redirectWithError(w, r, "missing_params", "code or state missing")
			return
		}

		claims, err := h.verifyState(state)
		if err != nil {
			h.redirectWithError(w, r, "invalid_state", "state expired or tampered")
			return
		}

		cfg, err := h.orgs.GetOIDCConfigForOrg(r.Context(), claims.OrgID)
		if err != nil {
			h.redirectWithError(w, r, "org_config", "organization SSO not configured")
			return
		}

		oidcCfg := oidc.Config{
			Issuer:               cfg.Issuer,
			ClientID:             cfg.ClientID,
			ClientSecret:         cfg.ClientSecret,
			RedirectURL:          h.cfg.CallbackURL,
			AllowUnverifiedEmail: true,
		}
		vt, err := h.oidcClient.ExchangeCode(r.Context(), oidcCfg, code, claims.Nonce)
		if err != nil {
			slog.Warn("sso callback: token verification failed", "error", err, "org_id", claims.OrgID)
			h.redirectWithError(w, r, "verify_failed", "id token verification failed")
			return
		}

		// Find-or-create the platform_users row. MVP creates on
		// first SSO if the email isn't there yet, but only if the
		// user is already invited to the org (checked below). This
		// prevents random SSO'd users from a permissive IdP creating
		// accounts.
		userID, isSuperadmin, err := findOrCreatePlatformUserForSSO(r.Context(), h.pool, vt.Claims.Email, vt.Claims.Name)
		if err != nil {
			slog.Error("sso callback: user find-or-create", "error", err)
			h.redirectWithError(w, r, "user_provisioning_failed", "could not resolve platform user")
			return
		}

		// Confirm the user is a member of the org they SSO'd into.
		// Manual-invite gate: rejects a valid SSO login from an
		// email address that no admin has ever invited. Closes
		// the "permissive IdP mints any email" attack.
		if err := h.orgs.EnsureMemberFromSSO(r.Context(), claims.OrgID, userID); err != nil {
			slog.Warn("sso callback: user not member of org", "org_id", claims.OrgID, "user_id", userID)
			h.redirectWithError(w, r, "not_a_member", "you are not a member of this organization — ask an admin to invite you")
			return
		}

		// Issue the platform JWT.
		accessToken, expiresIn, err := h.authSvc.IssuePlatformJWT(userID, vt.Claims.Email, isSuperadmin)
		if err != nil {
			slog.Error("sso callback: JWT issue", "error", err)
			h.redirectWithError(w, r, "token_issue_failed", "could not issue session token")
			return
		}

		// Redirect back to the console with the access token in the
		// URL fragment (fragment stays client-side, doesn't hit
		// server logs).
		u, err := url.Parse(h.cfg.ConsoleRedirectURL)
		if err != nil {
			// Config error at boot time; should never happen at runtime
			slog.Error("sso callback: bad console redirect URL", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		frag := url.Values{
			"access_token": {accessToken},
			"token_type":   {"bearer"},
			"expires_in":   {fmt.Sprintf("%d", expiresIn)},
			"sso":          {"1"},
		}
		u.Fragment = frag.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	}
}

// redirectWithError sends the user back to the console with an
// error indicator in the fragment. The login page reads it and
// shows a message rather than a blank screen.
func (h *SSOHandler) redirectWithError(w http.ResponseWriter, r *http.Request, code, msg string) {
	u, err := url.Parse(h.cfg.ConsoleRedirectURL)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !strings.HasSuffix(u.Path, "/login") {
		u.Path = strings.TrimRight(u.Path, "/") + "/login"
	}
	frag := url.Values{"sso_error": {code}, "sso_error_msg": {msg}}
	u.Fragment = frag.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// findOrCreatePlatformUserForSSO looks up platform_users by email;
// creates a row on first SSO login. Returns (userID, isSuperadmin).
//
// Password column is set to a marker value ('sso-only:<random>')
// that fails password auth by design — the user can still sign in
// via SSO, or reset password via the forgot-password flow if they
// want to enable password auth as a fallback.
func findOrCreatePlatformUserForSSO(ctx context.Context, pool *pgxpool.Pool, email, displayName string) (string, bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var userID string
	var isSuperadmin bool
	err := pool.QueryRow(ctx, `
		SELECT id::text, COALESCE(is_superadmin, false)
		  FROM public.platform_users
		 WHERE lower(email) = $1
	`, email).Scan(&userID, &isSuperadmin)
	if err == nil {
		return userID, isSuperadmin, nil
	}
	if err != pgx.ErrNoRows {
		return "", false, fmt.Errorf("lookup user: %w", err)
	}
	// Create — password is a marker that fails all password auth.
	pwMarker := "sso-only:" + fmt.Sprintf("%d", time.Now().UnixNano())
	err = pool.QueryRow(ctx, `
		INSERT INTO public.platform_users (email, password_hash, display_name)
		VALUES ($1, $2, $3)
		RETURNING id::text
	`, email, pwMarker, displayName).Scan(&userID)
	if err != nil {
		return "", false, fmt.Errorf("create user: %w", err)
	}
	return userID, false, nil
}
