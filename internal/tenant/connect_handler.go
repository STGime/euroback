package tenant

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ConnectInfo is the JSON response for the connect endpoint.
type ConnectInfo struct {
	ProjectID   string            `json:"project_id"`
	ProjectName string            `json:"project_name"`
	Slug        string            `json:"slug"`
	APIURL      string            `json:"api_url"`
	Region      string            `json:"region"`
	Plan        string            `json:"plan"`
	DatabaseURL string            `json:"database_url,omitempty"`
	Tables      []ConnectTable    `json:"tables"`
	ClaudeMD    string            `json:"claude_md"`
	CursorRules string            `json:"cursor_rules"`
	EnvTemplate string            `json:"env_template"`
	SampleCode  map[string]string `json:"sample_code"`
}

// ConnectTable is a simplified table schema for connect info.
type ConnectTable struct {
	Name    string          `json:"name"`
	Columns []ConnectColumn `json:"columns"`
}

// ConnectColumn is a simplified column description.
type ConnectColumn struct {
	Name     string `json:"name"`
	DataType string `json:"data_type"`
	Nullable bool   `json:"nullable"`
}

// HandleConnect returns an http.HandlerFunc that generates connection info,
// CLAUDE.md, .cursorrules, and sample code for a project.
//
// GET /platform/projects/{id}/connect
func HandleConnect(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")

		_, _, ok := requireRole(w, r, pool, projectID, "viewer")
		if !ok {
			return
		}

		// Get project info.
		var name, slug, region, plan, schemaName string
		err := pool.QueryRow(r.Context(),
			`SELECT p.name, p.slug, p.region, p.plan, p.schema_name
			 FROM projects p
			 WHERE p.id = $1`, projectID,
		).Scan(&name, &slug, &region, &plan, &schemaName)
		if err != nil {
			http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
			return
		}

		apiURL := fmt.Sprintf("https://%s.eurobase.app", slug)

		// Introspect tables in the tenant schema.
		tables := introspectSchema(r.Context(), pool, schemaName)

		// Build CLAUDE.md content.
		claudeMD := generateClaudeMD(name, slug, apiURL, plan, tables)

		// Build .cursorrules content.
		cursorRules := generateCursorRules(name, slug, apiURL, tables)

		// Build .env template.
		envTemplate := fmt.Sprintf("EUROBASE_URL=%s\nEUROBASE_PUBLIC_KEY=eb_pk_...\nEUROBASE_SECRET_KEY=eb_sk_...", apiURL)

		// Sample code snippets.
		sampleCode := map[string]string{
			"javascript": fmt.Sprintf(`import { createClient } from '@eurobase/sdk'

const eb = createClient({
  url: '%s',
  apiKey: process.env.EUROBASE_PUBLIC_KEY
})

// Query
const { data } = await eb.db.from('todos').select('*')

// Insert
await eb.db.from('todos').insert({ title: 'New task', completed: false })

// Update
await eb.db.from('todos').update({ completed: true }).eq('id', rowId)

// Delete
await eb.db.from('todos').delete().eq('id', rowId)

// Upload file
await eb.storage.upload('documents/report.pdf', file)

// ── Authentication ──

// Sign up a new user
const { data: signUpData, error: signUpErr } = await eb.auth.signUp({
  email: 'user@example.com', password: 'securepassword'
})

// Sign in
await eb.auth.signIn({ email: 'user@example.com', password: 'securepassword' })

// After sign-in, JWT is sent automatically with every query (RLS enforced)
eb.auth.onAuthStateChange((event, session) => {
  console.log(event) // SIGNED_IN | SIGNED_OUT | TOKEN_REFRESHED
})

// Get current user
const { data: user } = await eb.auth.getUser()

// Sign out
await eb.auth.signOut()`, apiURL),

			"curl": fmt.Sprintf(`# List todos
curl -s '%s/v1/db/todos' \
  -H 'Authorization: Bearer $EUROBASE_PUBLIC_KEY' | jq .

# Insert a todo
curl -s '%s/v1/db/todos' \
  -X POST -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer $EUROBASE_PUBLIC_KEY' \
  -d '{"title":"New task","completed":false}' | jq .

# ── Authentication ──

# Sign up a new user
curl -s '%s/v1/auth/signup' \
  -X POST -H 'Content-Type: application/json' \
  -H 'apikey: $EUROBASE_PUBLIC_KEY' \
  -d '{"email":"user@example.com","password":"securepassword"}' | jq .

# Sign in (returns access_token + refresh_token)
curl -s '%s/v1/auth/signin' \
  -X POST -H 'Content-Type: application/json' \
  -H 'apikey: $EUROBASE_PUBLIC_KEY' \
  -d '{"email":"user@example.com","password":"securepassword"}' | jq .

# Query as authenticated user (RLS enforced)
curl -s '%s/v1/db/todos' \
  -H 'apikey: $EUROBASE_PUBLIC_KEY' \
  -H 'Authorization: Bearer $ACCESS_TOKEN' | jq .`, apiURL, apiURL, apiURL, apiURL, apiURL),
		}

		resp := ConnectInfo{
			ProjectID:   projectID,
			ProjectName: name,
			Slug:        slug,
			APIURL:      apiURL,
			Region:      region,
			Plan:        plan,
			DatabaseURL: os.Getenv("DATABASE_URL"),
			Tables:      tables,
			ClaudeMD:    claudeMD,
			CursorRules: cursorRules,
			EnvTemplate: envTemplate,
			SampleCode:  sampleCode,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func introspectSchema(ctx context.Context, pool *pgxpool.Pool, schemaName string) []ConnectTable {
	tableRows, err := pool.Query(ctx,
		`SELECT table_name FROM information_schema.tables
		 WHERE table_schema = $1 AND table_type = 'BASE TABLE'
		 ORDER BY table_name`, schemaName,
	)
	if err != nil {
		slog.Error("introspect schema: list tables", "error", err)
		return nil
	}
	defer tableRows.Close()

	var tables []ConnectTable
	for tableRows.Next() {
		var tableName string
		if err := tableRows.Scan(&tableName); err != nil {
			continue
		}
		tables = append(tables, ConnectTable{Name: tableName})
	}
	tableRows.Close()

	// Fetch columns for each table.
	for i := range tables {
		colRows, err := pool.Query(ctx,
			`SELECT column_name, data_type, is_nullable
			 FROM information_schema.columns
			 WHERE table_schema = $1 AND table_name = $2
			 ORDER BY ordinal_position`, schemaName, tables[i].Name,
		)
		if err != nil {
			slog.Error("introspect schema: list columns", "error", err, "table", tables[i].Name)
			continue
		}

		for colRows.Next() {
			var name, dataType, isNullable string
			if err := colRows.Scan(&name, &dataType, &isNullable); err != nil {
				continue
			}
			tables[i].Columns = append(tables[i].Columns, ConnectColumn{
				Name:     name,
				DataType: dataType,
				Nullable: isNullable == "YES",
			})
		}
		colRows.Close()
	}

	return tables
}

func generateClaudeMD(name, slug, apiURL, plan string, tables []ConnectTable) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# %s — Eurobase Project\n\n", name)
	fmt.Fprintf(&b, "EU-sovereign backend powered by Eurobase. Zero US CLOUD Act exposure.\n\n")
	fmt.Fprintf(&b, "## Connection\n\n")
	fmt.Fprintf(&b, "- **API URL**: %s\n", apiURL)
	fmt.Fprintf(&b, "- **SDK**: `@eurobase/sdk`\n")
	fmt.Fprintf(&b, "- **Install**: `npm install @eurobase/sdk`\n")
	fmt.Fprintf(&b, "- **Plan**: %s\n\n", plan)

	if len(tables) > 0 {
		fmt.Fprintf(&b, "## Database Schema\n\n")
		for _, t := range tables {
			fmt.Fprintf(&b, "### %s\n\n", t.Name)
			fmt.Fprintf(&b, "| Column | Type | Nullable |\n")
			fmt.Fprintf(&b, "|--------|------|----------|\n")
			for _, c := range t.Columns {
				nullable := "no"
				if c.Nullable {
					nullable = "yes"
				}
				fmt.Fprintf(&b, "| %s | %s | %s |\n", c.Name, c.DataType, nullable)
			}
			fmt.Fprintf(&b, "\n")
		}
	}

	fmt.Fprintf(&b, "## SDK Usage\n\n")
	fmt.Fprintf(&b, "```typescript\n")
	fmt.Fprintf(&b, "import { createClient } from '@eurobase/sdk'\n\n")
	fmt.Fprintf(&b, "const eb = createClient({\n")
	fmt.Fprintf(&b, "  url: '%s',\n", apiURL)
	fmt.Fprintf(&b, "  apiKey: process.env.EUROBASE_PUBLIC_KEY\n")
	fmt.Fprintf(&b, "})\n\n")
	fmt.Fprintf(&b, "// Query\nconst { data } = await eb.db.from('todos').select('*')\n\n")
	fmt.Fprintf(&b, "// Insert\nawait eb.db.from('todos').insert({ title: 'New task' })\n\n")
	fmt.Fprintf(&b, "// Update\nawait eb.db.from('todos').update({ completed: true }).eq('id', id)\n\n")
	fmt.Fprintf(&b, "// Delete\nawait eb.db.from('todos').delete().eq('id', id)\n\n")
	fmt.Fprintf(&b, "// File upload\nawait eb.storage.upload('path/file.pdf', file)\n\n")
	fmt.Fprintf(&b, "// Realtime\neb.realtime.on('todos', 'INSERT', (e) => console.log(e))\n")
	fmt.Fprintf(&b, "```\n\n")

	fmt.Fprintf(&b, "## Authentication\n\n")
	fmt.Fprintf(&b, "```typescript\n")
	fmt.Fprintf(&b, "// Sign up\nconst { data, error } = await eb.auth.signUp({ email: 'user@example.com', password: 'securepassword' })\n\n")
	fmt.Fprintf(&b, "// Sign in\nawait eb.auth.signIn({ email: 'user@example.com', password: 'securepassword' })\n\n")
	fmt.Fprintf(&b, "// Get current user\nconst { data: user } = await eb.auth.getUser()\n\n")
	fmt.Fprintf(&b, "// Listen for auth state changes\neb.auth.onAuthStateChange((event, session) => {\n")
	fmt.Fprintf(&b, "  console.log(event) // SIGNED_IN | SIGNED_OUT | TOKEN_REFRESHED\n})\n\n")
	fmt.Fprintf(&b, "// Sign out\nawait eb.auth.signOut()\n")
	fmt.Fprintf(&b, "```\n\n")
	fmt.Fprintf(&b, "After sign-in, the JWT is sent automatically with every `eb.db` query. RLS policies are enforced server-side.\n\n")

	fmt.Fprintf(&b, "## Constraints\n\n")
	fmt.Fprintf(&b, "- All infrastructure is EU-only (Scaleway, Paris FR)\n")
	fmt.Fprintf(&b, "- No US-incorporated services (AWS, GCP, Azure, Stripe, Vercel, Cloudflare)\n")
	fmt.Fprintf(&b, "- GDPR-native by design\n")

	return b.String()
}

func generateCursorRules(name, slug, apiURL string, tables []ConnectTable) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# %s — Eurobase Project Rules\n\n", name)
	fmt.Fprintf(&b, "This project uses Eurobase as its backend (EU-sovereign BaaS).\n\n")
	fmt.Fprintf(&b, "API URL: %s\n", apiURL)
	fmt.Fprintf(&b, "SDK: @eurobase/sdk (npm install @eurobase/sdk)\n\n")
	fmt.Fprintf(&b, "## Key patterns\n\n")
	fmt.Fprintf(&b, "- Use `createClient()` from `@eurobase/sdk` for all backend operations\n")
	fmt.Fprintf(&b, "- API key from env: `process.env.EUROBASE_PUBLIC_KEY`\n")
	fmt.Fprintf(&b, "- Query: `eb.db.from('table').select('*')`\n")
	fmt.Fprintf(&b, "- Insert: `eb.db.from('table').insert({ ... })`\n")
	fmt.Fprintf(&b, "- Storage: `eb.storage.upload(key, file)`\n")
	fmt.Fprintf(&b, "- Realtime: `eb.realtime.on('table', 'INSERT', callback)`\n")
	fmt.Fprintf(&b, "- Auth: `eb.auth.signUp/signIn/signOut({ email, password })`\n")
	fmt.Fprintf(&b, "- Auth state: `eb.auth.onAuthStateChange(callback)`\n")
	fmt.Fprintf(&b, "- After sign-in, JWT is sent automatically with all db queries (RLS enforced)\n\n")

	if len(tables) > 0 {
		fmt.Fprintf(&b, "## Available tables\n\n")
		for _, t := range tables {
			cols := make([]string, len(t.Columns))
			for i, c := range t.Columns {
				cols[i] = fmt.Sprintf("%s (%s)", c.Name, c.DataType)
			}
			fmt.Fprintf(&b, "- %s: %s\n", t.Name, strings.Join(cols, ", "))
		}
		fmt.Fprintf(&b, "\n")
	}

	fmt.Fprintf(&b, "## Constraints\n\n")
	fmt.Fprintf(&b, "- EU-only infrastructure, no US cloud services\n")
	fmt.Fprintf(&b, "- GDPR compliant — handle user data accordingly\n")

	return b.String()
}
