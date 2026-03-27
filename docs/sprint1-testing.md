# Sprint 1 — Manual Testing Plan

## Prerequisites

1. Gateway running: `source .env.local && go run ./cmd/gateway/`
2. Migrations through 017 applied
3. Platform user signed in (have token)
4. Project with `todos` table (default from provisioning)

## Setup test data

Create tables with a foreign key relationship for testing nested relations:

```bash
TOKEN="your-platform-token"
PROJECT_ID="your-project-id"
SECRET_KEY="your-secret-key"

# Create categories table
curl -s http://localhost:8080/platform/projects/$PROJECT_ID/schema/tables \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "categories",
    "columns": [
      {"name": "id", "type": "uuid", "nullable": false, "is_primary_key": true},
      {"name": "name", "type": "text", "nullable": false, "is_primary_key": false},
      {"name": "created_at", "type": "timestamptz", "nullable": false, "is_primary_key": false, "default_value": "now()"}
    ]
  }' | jq .

# Create posts table with FK to categories
curl -s http://localhost:8080/platform/projects/$PROJECT_ID/schema/tables \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "posts",
    "columns": [
      {"name": "id", "type": "uuid", "nullable": false, "is_primary_key": true},
      {"name": "title", "type": "text", "nullable": false, "is_primary_key": false},
      {"name": "body", "type": "text", "nullable": true, "is_primary_key": false},
      {"name": "category_id", "type": "uuid", "nullable": true, "is_primary_key": false,
       "foreign_key": {"column": "category_id", "referenced_table": "categories", "referenced_column": "id"}},
      {"name": "created_at", "type": "timestamptz", "nullable": false, "is_primary_key": false, "default_value": "now()"}
    ]
  }' | jq .

# Insert a category
CATEGORY_ID=$(curl -s http://localhost:8080/v1/db/categories \
  -H "apikey: $SECRET_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name": "Technology"}' | jq -r .id)
echo "Category ID: $CATEGORY_ID"

# Insert posts
curl -s http://localhost:8080/v1/db/posts \
  -H "apikey: $SECRET_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"title\": \"EU Data Sovereignty Guide\", \"body\": \"European data sovereignty means keeping all citizen data within EU jurisdiction under GDPR.\", \"category_id\": \"$CATEGORY_ID\"}" | jq .

curl -s http://localhost:8080/v1/db/posts \
  -H "apikey: $SECRET_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"title\": \"Building with Eurobase\", \"body\": \"Eurobase is an EU-sovereign backend-as-a-service platform.\", \"category_id\": \"$CATEGORY_ID\"}" | jq .
```

---

## Test 1: Aggregations

### COUNT all rows
```bash
curl -s "http://localhost:8080/v1/db/posts?aggregate=count" \
  -H "apikey: $SECRET_KEY" | jq .
```
Expected: `{"result": 2}`

### COUNT with filter
```bash
curl -s "http://localhost:8080/v1/db/posts?aggregate=count&title=eq.Building%20with%20Eurobase" \
  -H "apikey: $SECRET_KEY" | jq .
```
Expected: `{"result": 1}`

### MIN
```bash
curl -s "http://localhost:8080/v1/db/posts?aggregate=min:created_at" \
  -H "apikey: $SECRET_KEY" | jq .
```
Expected: `{"result": "2026-03-27T..."}` (earliest timestamp)

### MAX
```bash
curl -s "http://localhost:8080/v1/db/posts?aggregate=max:created_at" \
  -H "apikey: $SECRET_KEY" | jq .
```
Expected: `{"result": "2026-03-27T..."}` (latest timestamp)

### COUNT on todos
```bash
curl -s "http://localhost:8080/v1/db/todos?aggregate=count" \
  -H "apikey: $SECRET_KEY" | jq .
```
Expected: `{"result": 3}` (default provisioned rows)

### Invalid aggregate function
```bash
curl -s "http://localhost:8080/v1/db/posts?aggregate=median:title" \
  -H "apikey: $SECRET_KEY" | jq .
```
Expected: 400 error mentioning "unsupported"

### Aggregate on invalid column
```bash
curl -s "http://localhost:8080/v1/db/posts?aggregate=sum:nonexistent" \
  -H "apikey: $SECRET_KEY" | jq .
```
Expected: 400 error mentioning column not found

---

## Test 2: Full-Text Search

### Search by body content
```bash
curl -s "http://localhost:8080/v1/db/posts?body=fts.sovereignty" \
  -H "apikey: $SECRET_KEY" | jq .
```
Expected: Returns the "EU Data Sovereignty" post

### Search with multiple terms (AND)
```bash
curl -s "http://localhost:8080/v1/db/posts?body=fts.european%20data" \
  -H "apikey: $SECRET_KEY" | jq .
```
Expected: Returns posts matching BOTH "european" AND "data"

### Search with no results
```bash
curl -s "http://localhost:8080/v1/db/posts?body=fts.xyznonexistent" \
  -H "apikey: $SECRET_KEY" | jq .
```
Expected: `{"data": [], "count": 0}`

### Search on title
```bash
curl -s "http://localhost:8080/v1/db/posts?title=fts.Eurobase" \
  -H "apikey: $SECRET_KEY" | jq .
```
Expected: Returns the "Building with Eurobase" post

### FTS + regular filter combined
```bash
curl -s "http://localhost:8080/v1/db/posts?body=fts.sovereignty&title=like.*Guide*" \
  -H "apikey: $SECRET_KEY" | jq .
```
Expected: Returns only the sovereignty post (both filters match)

---

## Test 3: Nested Relation Selects

### Select all with related category
```bash
curl -s "http://localhost:8080/v1/db/posts?select=*,categories(*)" \
  -H "apikey: $SECRET_KEY" | jq .
```
Expected: Each post has a nested `categories` object:
```json
{
  "data": [{
    "id": "...",
    "title": "EU Data Sovereignty Guide",
    "body": "...",
    "category_id": "...",
    "categories": {
      "id": "...",
      "name": "Technology",
      "created_at": "..."
    }
  }]
}
```

### Select specific columns from relation
```bash
curl -s "http://localhost:8080/v1/db/posts?select=title,categories(name)" \
  -H "apikey: $SECRET_KEY" | jq .
```
Expected: Posts with only `title` and nested `categories.name`

### Normal select (no relations — unchanged behavior)
```bash
curl -s "http://localhost:8080/v1/db/posts?select=title,body" \
  -H "apikey: $SECRET_KEY" | jq .
```
Expected: Flat result, no nested objects

### Non-existent relation
```bash
curl -s "http://localhost:8080/v1/db/posts?select=*,nonexistent(name)" \
  -H "apikey: $SECRET_KEY" | jq .
```
Expected: 400 error about missing foreign key

---

## Test 4: Usage Dashboard

### Get usage
```bash
curl -s "http://localhost:8080/platform/projects/$PROJECT_ID/usage" \
  -H "Authorization: Bearer $TOKEN" | jq .
```
Expected:
```json
{
  "usage": {
    "database_size_mb": 0.05,
    "storage_size_mb": 0,
    "mau_count": 0,
    "webhook_count": 0,
    "project_count": 1
  },
  "limits": {
    "plan": "free",
    "db_size_mb": 500,
    ...
  }
}
```

### Usage with invalid project
```bash
curl -s "http://localhost:8080/platform/projects/00000000-0000-0000-0000-000000000000/usage" \
  -H "Authorization: Bearer $TOKEN" | jq .
```
Expected: 404

### Console verification
1. Open http://localhost:5173 (or console.eurobase.app)
2. Navigate to a project overview page
3. Verify the Usage section appears with:
   - Database bar (shows MB used / 500 MB)
   - Storage bar
   - Auth Users bar
   - Plan card showing "free"

---

## Test 5: Combined features

### Count + FTS
```bash
curl -s "http://localhost:8080/v1/db/posts?aggregate=count&body=fts.sovereignty" \
  -H "apikey: $SECRET_KEY" | jq .
```
Expected: `{"result": 1}`

### Relation + filter
```bash
curl -s "http://localhost:8080/v1/db/posts?select=title,categories(name)&title=like.*Eurobase*" \
  -H "apikey: $SECRET_KEY" | jq .
```
Expected: Only the Eurobase post, with nested category

---

## Test 6: Edge cases

- Aggregate on empty table → `{"result": 0}` for count, `{"result": null}` for sum/avg/min/max
- FTS on empty string → should return all rows (or error)
- Relation select on table with no FKs → 400 error
- FTS on a column that doesn't exist → 400 error
- All features work with both public_key and secret_key
- All features return 401 without any API key
