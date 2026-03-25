# Manual Testing — Auth Config (Onboarding Step 2)

## Prerequisites

```bash
# 1. Start infrastructure
make setup

# 2. Apply migrations
source .env.local && migrate -path migrations -database "$DATABASE_URL" up

# 3. Start gateway in dev mode
DEV_MODE=true source .env.local && go run ./cmd/gateway/

# 4. Start console
cd console && npm run dev
```

---

## A. Backend API (curl)

All curl examples assume `DEV_MODE=true` (any Bearer token works).

### A1. PATCH — save valid auth_config

```bash
# First, get a project_id from the list
curl -s http://localhost:8080/v1/tenants \
  -H 'Authorization: Bearer dev' | jq '.[0].id'

# Set it for later
export PID=<project_id_here>

# Save auth_config
curl -s -X PATCH "http://localhost:8080/v1/tenants/$PID" \
  -H 'Authorization: Bearer dev' \
  -H 'Content-Type: application/json' \
  -d '{
    "auth_config": {
      "providers": { "email_password": { "enabled": true } },
      "password_min_length": 10,
      "require_email_confirmation": false,
      "session_duration": "24h",
      "redirect_urls": ["http://localhost:3000"]
    }
  }' | jq .
```

**Expected:** 200 with full project JSON including `auth_config` with the values you sent.

### A2. PATCH — rejected: password_min_length < 8

```bash
curl -s -X PATCH "http://localhost:8080/v1/tenants/$PID" \
  -H 'Authorization: Bearer dev' \
  -H 'Content-Type: application/json' \
  -d '{
    "auth_config": {
      "providers": { "email_password": { "enabled": true } },
      "password_min_length": 3,
      "require_email_confirmation": false,
      "session_duration": "168h",
      "redirect_urls": ["http://localhost:3000"]
    }
  }' | jq .
```

**Expected:** 400 `{"error":"password_min_length must be between 8 and 128"}`

### A3. PATCH — rejected: invalid session_duration

```bash
curl -s -X PATCH "http://localhost:8080/v1/tenants/$PID" \
  -H 'Authorization: Bearer dev' \
  -H 'Content-Type: application/json' \
  -d '{
    "auth_config": {
      "providers": { "email_password": { "enabled": true } },
      "password_min_length": 8,
      "require_email_confirmation": false,
      "session_duration": "48h",
      "redirect_urls": ["http://localhost:3000"]
    }
  }' | jq .
```

**Expected:** 400 `{"error":"session_duration must be one of: 1h, 24h, 168h, 720h"}`

### A4. PATCH — rejected: no providers enabled

```bash
curl -s -X PATCH "http://localhost:8080/v1/tenants/$PID" \
  -H 'Authorization: Bearer dev' \
  -H 'Content-Type: application/json' \
  -d '{
    "auth_config": {
      "providers": { "email_password": { "enabled": false } },
      "password_min_length": 8,
      "require_email_confirmation": false,
      "session_duration": "168h",
      "redirect_urls": ["http://localhost:3000"]
    }
  }' | jq .
```

**Expected:** 400 `{"error":"at least one auth provider must be enabled"}`

### A5. PATCH — rejected: invalid redirect URL

```bash
curl -s -X PATCH "http://localhost:8080/v1/tenants/$PID" \
  -H 'Authorization: Bearer dev' \
  -H 'Content-Type: application/json' \
  -d '{
    "auth_config": {
      "providers": { "email_password": { "enabled": true } },
      "password_min_length": 8,
      "require_email_confirmation": false,
      "session_duration": "168h",
      "redirect_urls": ["not-a-url"]
    }
  }' | jq .
```

**Expected:** 400 `{"error":"invalid redirect URL: not-a-url"}`

### A6. PATCH — rejected: no auth header

```bash
curl -s -X PATCH "http://localhost:8080/v1/tenants/$PID" \
  -H 'Content-Type: application/json' \
  -d '{"auth_config": {"providers": {"email_password": {"enabled": true}}, "password_min_length": 8, "require_email_confirmation": false, "session_duration": "168h", "redirect_urls": ["http://localhost:3000"]}}' | jq .
```

**Expected:** 401 `{"error":"missing authorization header"}`

### A7. Verify auth_config flows through to GET list

```bash
curl -s http://localhost:8080/v1/tenants \
  -H 'Authorization: Bearer dev' | jq '.[0].auth_config'
```

**Expected:** The `auth_config` object you saved in A1.

---

## B. Auth Config Enforcement on End-User Auth

These tests verify the SDK auth routes respect the config.

### B1. Set password_min_length to 12, session to 1h

```bash
curl -s -X PATCH "http://localhost:8080/v1/tenants/$PID" \
  -H 'Authorization: Bearer dev' \
  -H 'Content-Type: application/json' \
  -d '{
    "auth_config": {
      "providers": { "email_password": { "enabled": true } },
      "password_min_length": 12,
      "require_email_confirmation": false,
      "session_duration": "1h",
      "redirect_urls": ["http://localhost:3000"]
    }
  }' | jq .auth_config
```

### B2. End-user signup — password too short (should fail)

```bash
# Get the public key
export APIKEY=$(curl -s "http://localhost:8080/platform/projects/$PID/api-keys" \
  -H 'Authorization: Bearer dev' | jq -r '.[0].key_prefix')
# NOTE: In dev mode you'll need the full public key. Check your project creation output.

curl -s -X POST http://localhost:8080/v1/auth/signup \
  -H "apikey: $APIKEY" \
  -H 'Content-Type: application/json' \
  -d '{"email": "test-short@example.eu", "password": "short123"}' | jq .
```

**Expected:** 400 `{"error":"password must be at least 12 characters"}`

### B3. End-user signup — password long enough (should succeed)

```bash
curl -s -X POST http://localhost:8080/v1/auth/signup \
  -H "apikey: $APIKEY" \
  -H 'Content-Type: application/json' \
  -d '{"email": "test-long@example.eu", "password": "longpassword12"}' | jq .
```

**Expected:** 201 with `expires_in: 3600` (1 hour = 3600 seconds).

### B4. End-user signin — session duration check

```bash
curl -s -X POST http://localhost:8080/v1/auth/signin \
  -H "apikey: $APIKEY" \
  -H 'Content-Type: application/json' \
  -d '{"email": "test-long@example.eu", "password": "longpassword12"}' | jq .expires_in
```

**Expected:** `3600` (from the `"1h"` session_duration config).

### B5. Backward compatibility — empty auth_config

Existing projects with `auth_config = '{}'` should use defaults (8-char passwords, 168h sessions). If you have an older project that was never patched, sign up should work with an 8-character password.

---

## C. Console UI — Onboarding Flow

### C1. Fresh onboarding (3-step flow)

1. Open `http://localhost:5173/onboarding`
2. **Step 1** shows step indicator: "Step 1 of 3 - Create"
3. Enter a project name, select plan, click "Create Project"
4. After creation, you should land on **Step 2** ("Step 2 of 3 - Authentication")
5. Verify the auth config form shows:
   - Email + Password toggle (ON by default)
   - Passkeys row (grayed out, "Coming soon" badge)
   - Social Login row (grayed out, "Coming soon" badge)
   - Require email confirmation toggle (OFF)
   - Minimum password length input (8)
   - Session duration dropdown (7 days selected)
   - Allowed redirect URLs textarea (http://localhost:3000)

### C2. Save auth config

1. Change password min length to 10
2. Change session duration to 24 hours
3. Click "Continue"
4. Verify you land on **Step 3** ("Step 3 of 3 - Get Started") — the success screen with API keys

### C3. Skip auth config

1. Create another project via onboarding
2. On Step 2, click "Use defaults and continue"
3. Verify you land on success screen immediately
4. The project's `auth_config` should remain `{}` (defaults applied at runtime)

### C4. Step indicator progress

- Step 1: first bar filled (eurobase color), bars 2-3 gray
- Step 2: first two bars filled, bar 3 gray
- Step 3: all three bars filled

---

## D. Console UI — Dedicated Auth Config Page

### D1. Navigate to Auth page

1. Open a project dashboard (`/p/{id}`)
2. Verify sidebar tabs show: Overview, Database, Storage, **Auth**, **Users**, Logs, API, Connect, Webhooks, Settings
3. Click the "Auth" tab
4. Verify the auth config page loads at `/p/{id}/auth`

### D2. Load existing config

1. If auth_config was previously saved (from onboarding), verify the form shows those values
2. If auth_config is empty, verify defaults are populated (email_password ON, 8, 168h, etc.)

### D3. Save changes

1. Change a setting (e.g., password min length to 16)
2. Click "Save Changes"
3. Verify a success message appears: "Auth configuration saved."
4. Refresh the page — verify the saved value persists

### D4. Validation feedback

1. Try entering a password min length of 3
2. Click "Save Changes"
3. Verify the API returns an error and it's shown in the red error banner

---

## E. Sidebar Tab Rename

1. Open any project page
2. Verify the tab formerly labeled "Authentication" is now two separate tabs:
   - **Auth** → `/p/{id}/auth` (auth config page)
   - **Users** → `/p/{id}/users` (end-user management)
3. Verify the "Users" page still loads the end-user list correctly

---

## Postman Collection

Import `docs/eurobase-auth-config.postman_collection.json` into Postman.

**Setup:**
1. Set `project_id` variable to an existing project ID
2. Set `public_key` variable to the project's public API key
3. Select the "Eurobase Local" environment (or use the collection-level variables)
4. Run the collection in order — Phase 1 (PATCH validation), Phase 2 (enforcement), Phase 3 (session durations)

All 18 tests should pass with green checkmarks.
