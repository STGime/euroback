# Plan Limits — Manual Testing Plan

## Prerequisites

1. Gateway running locally: `source .env.local && go run ./cmd/gateway/`
2. Migration 017 applied: `source .env.local && migrate -path migrations -database "$DATABASE_URL" up`
3. Platform user signed in (have `platform_token`)
4. At least one project created (have `project_id` and `public_key`)

## Test 1: Plans endpoint

```bash
curl -s http://localhost:8080/platform/config/plans \
  -H "Authorization: Bearer $TOKEN" | jq .
```

Expected: JSON array with `free` and `pro` plan objects with all limit fields.

## Test 2: Usage endpoint

```bash
curl -s http://localhost:8080/platform/projects/$PROJECT_ID/usage \
  -H "Authorization: Bearer $TOKEN" | jq .
```

Expected: `{"usage": {...}, "limits": {...}}` with real DB size, MAU count, etc.

## Test 3: Webhook limit (free = 3)

Create 3 webhooks, then try a 4th:

```bash
# Should succeed (1-3)
for i in 1 2 3; do
  curl -s http://localhost:8080/platform/projects/$PROJECT_ID/webhooks \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"url\":\"https://example.com/hook$i\",\"events\":[\"insert\"]}" | jq .status
done

# Should fail with 403
curl -s -w "\n%{http_code}\n" http://localhost:8080/platform/projects/$PROJECT_ID/webhooks \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"url":"https://example.com/hook4","events":["insert"]}'
```

Expected: 4th webhook returns 403 with error mentioning "upgrade to pro".

## Test 4: Custom email templates (free = blocked)

```bash
curl -s -w "\n%{http_code}\n" \
  -X PUT http://localhost:8080/platform/projects/$PROJECT_ID/email-templates/verification \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"subject":"Custom","body_html":"<p>Custom</p>"}'
```

Expected: 403 with error mentioning "upgrade to pro".

Listing templates should still work (read is not gated):

```bash
curl -s http://localhost:8080/platform/projects/$PROJECT_ID/email-templates \
  -H "Authorization: Bearer $TOKEN" | jq '.[].template_type'
```

Expected: 200 with 4 template types.

## Test 5: Project limit (free = 2)

If you already have 2 projects:

```bash
curl -s -w "\n%{http_code}\n" \
  -X POST http://localhost:8080/v1/tenants \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"should-fail","slug":"should-fail"}'
```

Expected: 403 with error mentioning "2 projects" and "upgrade to pro".

Note: Project limit enforcement requires wiring into the tenant handler
(check if `CheckProjectLimit` is called in tenant.HandleCreateProject).

## Test 6: Rate limit headers

```bash
curl -s -D - http://localhost:8080/v1/db/todos \
  -H "apikey: $PUBLIC_KEY" 2>&1 | grep -i ratelimit
```

Expected: `X-RateLimit-Limit: 100` (free plan) with `Remaining` and `Reset` headers.
Requires Redis running.

## Test 7: Usage bars in console

1. Open http://localhost:5173
2. Sign in, navigate to a project
3. Overview page should show Usage section with:
   - Database bar (MB used / 500 MB)
   - Storage bar (MB used / 1 GB)
   - Auth Users bar (count / 10,000)
   - Plan card showing "free" with rate limit info

## Test 8: Upgrade to pro (manual DB)

Temporarily upgrade a project to test pro limits:

```sql
UPDATE projects SET plan = 'pro' WHERE id = 'YOUR_PROJECT_ID';
```

Then re-run tests 3-6:
- Webhook 4th should succeed (pro = unlimited)
- Custom templates should succeed
- Rate limit header should show 1000
- Usage endpoint should show pro limits

Reset after:
```sql
UPDATE projects SET plan = 'free' WHERE id = 'YOUR_PROJECT_ID';
```

## Test 9: Edge cases

- Usage endpoint with invalid project ID → 404
- Plans endpoint without auth → 401
- Usage endpoint without auth → 401
- Webhook create on project with 0 webhooks → should succeed
- Custom template preview (POST) on free plan → should work (only PUT is gated)
