# OAuth Social Login — Testing Plan

Eurobase supports four OAuth providers: **Google**, **GitHub**, **LinkedIn**, and **Apple**.

## Prerequisites

1. Gateway running: `source .env.local && go run ./cmd/gateway/`
2. Migration 021 applied
3. Platform user signed in, project with API keys

## Automated Tests

Run the unit test suite (no credentials needed):

```bash
go test ./internal/oauth/ -v
```

Tests cover:
- All 4 providers registered (google, github, linkedin, apple)
- AuthURL returns correct provider domain
- AuthURL includes correct parameters (client_id, state, scopes)
- Apple rejects requests without team_id/key_id
- Unknown provider returns error

---

## Test 1: Endpoints without OAuth configured

These should return clear errors (no crash):

```bash
# Google redirect — not configured
curl -s "http://localhost:8080/v1/auth/oauth/google?redirect_url=http://localhost:3000" \
  -H "apikey: $PUBLIC_KEY" | jq .
# Expected: 400 {"error": "oauth provider \"google\" is not enabled"}

# LinkedIn redirect — not configured
curl -s "http://localhost:8080/v1/auth/oauth/linkedin?redirect_url=http://localhost:3000" \
  -H "apikey: $PUBLIC_KEY" | jq .
# Expected: 400 {"error": "oauth provider \"linkedin\" is not enabled"}

# Apple redirect — not configured
curl -s "http://localhost:8080/v1/auth/oauth/apple?redirect_url=http://localhost:3000" \
  -H "apikey: $PUBLIC_KEY" | jq .
# Expected: 400 {"error": "oauth provider \"apple\" is not enabled"}

# Unknown provider
curl -s "http://localhost:8080/v1/auth/oauth/facebook?redirect_url=http://localhost:3000" \
  -H "apikey: $PUBLIC_KEY" | jq .
# Expected: 400 {"error": "unknown oauth provider: facebook"}

# Missing redirect_url
curl -s "http://localhost:8080/v1/auth/oauth/google" \
  -H "apikey: $PUBLIC_KEY" | jq .
# Expected: 400 {"error": "redirect_url is required"}

# No API key
curl -s "http://localhost:8080/v1/auth/oauth/google?redirect_url=http://localhost:3000" | jq .
# Expected: 401
```

## Test 2: Save OAuth config

```bash
# Enable Google OAuth with test credentials
curl -s -X PATCH "http://localhost:8080/v1/tenants/$PROJECT_ID" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "auth_config": {
      "providers": {"email_password": {"enabled": true}},
      "oauth_providers": {
        "google": {"enabled": true, "client_id": "test-id", "client_secret": "test-secret"}
      },
      "password_min_length": 8,
      "session_duration": "168h",
      "redirect_urls": ["http://localhost:3000"]
    }
  }' | jq .auth_config.oauth_providers
# Expected: google enabled with test-id, secret_set: true

# Enable LinkedIn OAuth
curl -s -X PATCH "http://localhost:8080/v1/tenants/$PROJECT_ID" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "auth_config": {
      "providers": {"email_password": {"enabled": true}},
      "oauth_providers": {
        "linkedin": {"enabled": true, "client_id": "test-li-id", "client_secret": "test-li-secret"}
      },
      "password_min_length": 8,
      "session_duration": "168h",
      "redirect_urls": ["http://localhost:3000"]
    }
  }' | jq .auth_config.oauth_providers
# Expected: linkedin enabled with test-li-id, secret_set: true

# Enable Apple OAuth with team_id and key_id
curl -s -X PATCH "http://localhost:8080/v1/tenants/$PROJECT_ID" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "auth_config": {
      "providers": {"email_password": {"enabled": true}},
      "oauth_providers": {
        "apple": {"enabled": true, "client_id": "com.example.app", "team_id": "ABCDE12345", "key_id": "ABC123DEFG", "client_secret": "-----BEGIN PRIVATE KEY-----\ntest\n-----END PRIVATE KEY-----"}
      },
      "password_min_length": 8,
      "session_duration": "168h",
      "redirect_urls": ["http://localhost:3000"]
    }
  }' | jq .auth_config.oauth_providers
# Expected: apple enabled, team_id and key_id persisted, secret_set: true
```

## Test 3: OAuth redirect (with config)

```bash
# Google — should redirect to accounts.google.com (302)
curl -s -o /dev/null -w "%{http_code} %{redirect_url}" \
  "http://localhost:8080/v1/auth/oauth/google?redirect_url=http://localhost:3000" \
  -H "apikey: $PUBLIC_KEY"
# Expected: 302 with redirect to accounts.google.com

# LinkedIn — should redirect to linkedin.com (302)
curl -s -o /dev/null -w "%{http_code} %{redirect_url}" \
  "http://localhost:8080/v1/auth/oauth/linkedin?redirect_url=http://localhost:3000" \
  -H "apikey: $PUBLIC_KEY"
# Expected: 302 with redirect to www.linkedin.com

# Apple — should redirect to appleid.apple.com (302)
curl -s -o /dev/null -w "%{http_code} %{redirect_url}" \
  "http://localhost:8080/v1/auth/oauth/apple?redirect_url=http://localhost:3000" \
  -H "apikey: $PUBLIC_KEY"
# Expected: 302 with redirect to appleid.apple.com
```

## Test 4: Callback with invalid code

```bash
curl -s "http://localhost:8080/v1/auth/oauth/google/callback?code=invalid&state=invalid" \
  -H "apikey: $PUBLIC_KEY" | jq .
# Expected: 400 (invalid state or failed exchange)
```

## Test 5: Apple POST callback accepted

```bash
# Apple uses form_post — verify POST is accepted (not 405)
curl -s -X POST "http://localhost:8080/v1/auth/oauth/apple/callback" \
  -H "apikey: $PUBLIC_KEY" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "code=invalid&state=test:http://localhost:3000" | jq .
# Expected: error response (not 405 Method Not Allowed)
```

---

## Full End-to-End Testing (requires real credentials)

### Google Setup
1. Go to https://console.cloud.google.com/apis/credentials
2. Create OAuth 2.0 Client ID (Web application)
3. Add authorized redirect URI: `{API_URL}/v1/auth/oauth/google/callback`
4. Copy Client ID and Client Secret
5. Configure in Eurobase console Auth settings

### GitHub Setup
1. Go to https://github.com/settings/developers
2. Create New OAuth App
3. Set callback URL: `{API_URL}/v1/auth/oauth/github/callback`
4. Copy Client ID and Client Secret
5. Configure in Eurobase console Auth settings

### LinkedIn Setup
1. Go to https://www.linkedin.com/developers/apps
2. Create a new app
3. Request the **Sign In with LinkedIn using OpenID Connect** product
4. Under Auth → OAuth 2.0 settings, add redirect URL: `{API_URL}/v1/auth/oauth/linkedin/callback`
5. Copy Client ID and Client Secret
6. Configure in Eurobase console Auth settings

### Apple Setup
1. Go to https://developer.apple.com/account/resources
2. Register a Services ID (Identifiers → Services IDs)
   - Enable Sign In with Apple
   - Add return URL: `{API_URL}/v1/auth/oauth/apple/callback`
3. Create a Key for Sign In with Apple — download the .p8 file
4. Note the Key ID and your Team ID
5. In Eurobase console Auth settings, enable Apple and fill in:
   - Service ID (Client ID), Team ID, Key ID, and paste the private key

### Test flow (for each provider)
1. Open browser: `{API_URL}/v1/auth/oauth/{provider}?redirect_url=http://localhost:3000`
   (Add `&apikey=YOUR_PUBLIC_KEY` if not using a custom domain)
2. Should redirect to provider's consent screen
3. Approve access
4. Should redirect back to `http://localhost:3000#access_token=...&refresh_token=...`
5. Extract the access_token from the URL fragment
6. Verify: `curl -s {API_URL}/v1/auth/user -H "apikey: $PUBLIC_KEY" -H "Authorization: Bearer $ACCESS_TOKEN"`

### Verify user was created
```bash
curl -s "{API_URL}/platform/projects/$PROJECT_ID/users" \
  -H "Authorization: Bearer $TOKEN" | jq '.users[] | {email, provider}'
```

---

## Test 6: Console UI

1. Navigate to Auth settings page
2. Toggle Google on → Client ID and Secret fields appear with setup instructions
3. Toggle GitHub on → same
4. Toggle LinkedIn on → same
5. Toggle Apple on → shows Service ID, Team ID, Key ID, and Private Key fields with setup instructions and Apple-specific warnings
6. Sovereignty callout appears when any OAuth is enabled
7. Save → reload → values persist
8. Toggle off → save → fields hidden

## Test 7: Account linking

If a user already signed up with email/password and then signs in with an OAuth provider using the same email, the accounts should be linked (same user ID, provider updated).

## Test 8: Apple-specific behavior

- **Name only on first auth:** Apple sends the user's name only the first time. Verify that on subsequent logins the name field is preserved (not overwritten with empty).
- **Private relay email:** If the user hides their email, Apple returns a private relay address like `abc123@privaterelay.appleid.com`. Verify this is accepted.
- **Re-testing:** Revoke access at https://appleid.apple.com → Security → Apps using Apple ID to trigger the first-auth flow again.

## Test 9: SDK

```typescript
import { createClient } from '@eurobase/sdk'

const eb = createClient({ url: 'http://localhost:8080', apiKey: 'eb_pk_...' })

// Redirect to provider
eb.auth.signInWithOAuth('google', { redirectTo: 'http://localhost:3000/callback' })
eb.auth.signInWithOAuth('linkedin', { redirectTo: 'http://localhost:3000/callback' })
eb.auth.signInWithOAuth('apple', { redirectTo: 'http://localhost:3000/callback' })

// On callback page:
const { data, error } = await eb.auth.handleOAuthCallback()
console.log(data) // { access_token: '...', user: { email: '...' } }
```
