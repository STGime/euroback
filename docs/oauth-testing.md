# OAuth Social Login — Testing Plan

## Prerequisites

1. Gateway running: `source .env.local && go run ./cmd/gateway/`
2. Migration 021 applied
3. Platform user signed in, project with API keys

## Test 1: Endpoints without OAuth configured

These should return clear errors (no crash):

```bash
# Google redirect — not configured
curl -s "http://localhost:8080/v1/auth/oauth/google?redirect_url=http://localhost:3000" \
  -H "apikey: $PUBLIC_KEY" | jq .
# Expected: 400 {"error": "oauth provider \"google\" is not enabled"}

# GitHub redirect — not configured
curl -s "http://localhost:8080/v1/auth/oauth/github?redirect_url=http://localhost:3000" \
  -H "apikey: $PUBLIC_KEY" | jq .
# Expected: 400 {"error": "oauth provider \"github\" is not enabled"}

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
# Expected: google enabled with test-id
```

## Test 3: Google redirect (with config)

```bash
# Should redirect to Google consent screen (302)
curl -s -o /dev/null -w "%{http_code} %{redirect_url}" \
  "http://localhost:8080/v1/auth/oauth/google?redirect_url=http://localhost:3000" \
  -H "apikey: $PUBLIC_KEY"
# Expected: 302 with redirect to accounts.google.com
```

## Test 4: Callback with invalid code

```bash
curl -s "http://localhost:8080/v1/auth/oauth/google/callback?code=invalid&state=invalid" \
  -H "apikey: $PUBLIC_KEY" | jq .
# Expected: 400 (invalid state or failed exchange)
```

## Test 5: Full end-to-end (requires real credentials)

### Google Setup
1. Go to https://console.cloud.google.com/apis/credentials
2. Create OAuth 2.0 Client ID (Web application)
3. Add authorized redirect URI: `http://localhost:8080/v1/auth/oauth/google/callback`
4. Save Client ID and Client Secret
5. Configure in Eurobase console Auth settings or via API

### GitHub Setup
1. Go to https://github.com/settings/developers
2. Create New OAuth App
3. Set callback URL: `http://localhost:8080/v1/auth/oauth/github/callback`
4. Save Client ID and Client Secret
5. Configure in Eurobase console Auth settings

### Test flow
1. Open browser: `http://localhost:8080/v1/auth/oauth/google?redirect_url=http://localhost:3000/callback&apikey=YOUR_PUBLIC_KEY`
   (Note: apikey can also be passed as query param if the middleware supports it)
2. Should redirect to Google consent screen
3. Approve access
4. Should redirect back to `http://localhost:3000/callback#access_token=...&refresh_token=...`
5. Extract the access_token from the URL fragment
6. Verify: `curl -s http://localhost:8080/v1/auth/user -H "apikey: $PUBLIC_KEY" -H "Authorization: Bearer $ACCESS_TOKEN"`

### Verify user was created
```bash
curl -s "http://localhost:8080/platform/projects/$PROJECT_ID/users" \
  -H "Authorization: Bearer $TOKEN" | jq '.users[] | {email, provider}'
# Should show: {"email": "your@gmail.com", "provider": "google"}
```

## Test 6: Console UI

1. Navigate to Auth settings page
2. Toggle Google on → Client ID and Secret fields appear with setup instructions
3. Toggle GitHub on → same
4. Sovereignty callout appears when any OAuth is enabled
5. Save → reload → values persist
6. Toggle off → save → fields hidden

## Test 7: Account linking

If a user already signed up with email/password and then signs in with Google
using the same email, the accounts should be linked (same user ID, provider
updated).

## Test 8: SDK

```typescript
import { createClient } from '@eurobase/sdk'

const eb = createClient({ url: 'http://localhost:8080', apiKey: 'eb_pk_...' })

// Redirect to Google
eb.auth.signInWithOAuth('google', { redirectTo: 'http://localhost:3000/callback' })

// On callback page:
const { data, error } = await eb.auth.handleOAuthCallback()
console.log(data) // { access_token: '...', user: { email: '...' } }
```
