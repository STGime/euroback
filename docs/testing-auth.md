# Testing Plan: Per-Project End-User Authentication

## Postman Collection

Import these two files into Postman:

1. **Collection:** `docs/eurobase-auth-tests.postman_collection.json`
2. **Environment:** `docs/eurobase-auth-tests.postman_environment.json`

Select the **Eurobase Local** environment before running.

## Prerequisites

```bash
# 1. Start local services (Postgres, Redis, MinIO)
make setup

# 2. Apply new migrations
source .env.local
migrate -path migrations -database "$DATABASE_URL" up

# 3. Start the gateway with auth enforced
export DEV_MODE=false
source .env.local && go run ./cmd/gateway/
```

The gateway must run with `DEV_MODE=false` so platform auth middleware is active. The Postman environment points to `http://localhost:8080`.

## Running the Tests

**Run the full collection in order** using Postman's Collection Runner (or click through each request manually). Tests are ordered sequentially — later requests depend on environment variables set by earlier ones.

### Execution order and variable flow

```
T0.1 Sign Up       → sets {{platform_token}}
T0.4 Sign In        → refreshes {{platform_token}}
T1.1 Create Project → sets {{project_id}}, {{public_key}}, {{secret_key}}
T2.1 Sign Up Alice  → sets {{alice_token}}, {{alice_refresh}}, {{alice_id}}
T2.3 Sign In Alice  → refreshes {{alice_token}}, {{alice_refresh}}
T2.7 Token Refresh  → rotates tokens, stores {{alice_refresh_old}}
T2.10 Sign Up Bob   → sets {{bob_token}}, {{bob_id}}
T2.11 Re-sign Alice → fresh {{alice_token}} for Phase 3
TC.6 Delete Project → cleanup
```

## Test Matrix

| # | Request | Method | Auth | Expected |
|---|---------|--------|------|----------|
| **Phase 0 — Platform Auth** |
| T0.1 | `/platform/auth/signup` | POST | none | 201, access_token + user |
| T0.2 | `/platform/auth/signup` (dup) | POST | none | 400, "email already registered" |
| T0.3 | `/platform/auth/signup` (weak pw) | POST | none | 400, "8 characters" |
| T0.4 | `/platform/auth/signin` | POST | none | 200, access_token + user |
| T0.5 | `/platform/auth/signin` (wrong pw) | POST | none | 401, "invalid email or password" |
| T0.6 | `/platform/auth/signin` (no user) | POST | none | 401, "invalid email or password" |
| T0.7 | `/v1/tenants/` | GET | platform JWT | 200, [] |
| T0.8 | `/v1/tenants/` | GET | none | 401 |
| T0.9 | `/v1/tenants/` | GET | garbage token | 401 |
| **Phase 1 — API Key Validation** |
| T1.1 | `/v1/tenants/` | POST | platform JWT | 201, project + API keys |
| T1.2 | `/v1/db/todos` | GET | apikey (public) | 200, sample todos |
| T1.3 | `/v1/db/todos` | GET | none | 401, "missing apikey" |
| T1.4 | `/v1/db/todos` | GET | apikey (invalid) | 401, "invalid API key" |
| T1.5 | `/v1/db/todos` | GET | apikey (secret) | 200, todos |
| **Phase 2 — End-User Auth** |
| T2.1 | `/v1/auth/signup` | POST | apikey | 201, tokens + user |
| T2.2 | `/v1/auth/signup` (dup) | POST | apikey | 400, "email already registered" |
| T2.3 | `/v1/auth/signin` | POST | apikey | 200, tokens + user |
| T2.4 | `/v1/auth/signin` (wrong pw) | POST | apikey | 401 |
| T2.5 | `/v1/auth/user` | GET | apikey + JWT | 200, user object |
| T2.6 | `/v1/auth/user` | GET | apikey only | 401, "authentication required" |
| T2.7 | `/v1/auth/refresh` | POST | apikey | 200, rotated tokens |
| T2.8 | `/v1/auth/refresh` (old token) | POST | apikey | 401, "invalid refresh token" |
| T2.9 | `/v1/auth/signout` | POST | apikey | 200 |
| T2.10 | `/v1/auth/signup` (Bob) | POST | apikey | 201 |
| T2.11 | `/v1/auth/signin` (Alice) | POST | apikey | 200, fresh tokens |
| **Phase 3 — Data with Auth** |
| T3.1 | `/v1/db/todos` | GET | apikey (anon) | 200, 3 todos |
| T3.2 | `/v1/db/todos` | GET | apikey + JWT | 200, 3 todos |
| T3.3 | `/v1/db/todos` | POST | apikey + JWT | 201, new todo |
| T3.4 | `/v1/db/todos` | GET | apikey (secret) | 200, >= 4 todos |
| **Phase 4 — Cross-Cutting** |
| TC.1 | `/health` | GET | none | 200 |
| TC.2 | `/v1/db/todos` | OPTIONS | CORS preflight | 204, apikey in allowed headers |
| TC.3 | `/v1/auth/user` | GET | apikey + platform JWT | 401 (wrong token type) |
| TC.4 | `/v1/tenants/` | GET | end-user JWT | 401 (wrong token type) |
| TC.5 | `/v1/auth/signup` | POST | no apikey | 401 |
| TC.6 | `/v1/tenants/:id` | DELETE | platform JWT | 204 |

## All Tests Pass Criteria

- **30 requests** in the collection
- All test scripts (green checkmarks) pass in Postman Collection Runner
- Zero manual intervention needed between requests (variables auto-propagate)

## Notes

- The collection is designed for a clean database. If re-running, delete the test user first: `psql $DATABASE_URL -c "DELETE FROM platform_users WHERE email = 'dev@test.eu'"`
- For RLS per-user isolation testing (Phase 4 from the plan), create a table with a user-scoped RLS policy via psql and run manual queries. The Postman collection covers the `todos` table which uses `USING (true)` (public access).
