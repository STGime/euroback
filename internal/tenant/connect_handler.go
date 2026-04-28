package tenant

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ConnectInfo is the JSON response for the connect endpoint.
//
// NOTE: DATABASE_URL is deliberately NOT returned. The gateway's Postgres
// credential has platform-wide privileges (DDL on public.*, cross-tenant
// access). Tenants access data through the gateway's HTTP API (RLS-scoped)
// and never hold a raw DB connection string. Platform migrations are run
// by the deploy pipeline, not tenants.
type ConnectInfo struct {
	ProjectID   string            `json:"project_id"`
	ProjectName string            `json:"project_name"`
	Slug        string            `json:"slug"`
	APIURL      string            `json:"api_url"`
	Region      string            `json:"region"`
	Plan        string            `json:"plan"`
	MCPURL      string            `json:"mcp_url"`
	Tables      []ConnectTable    `json:"tables"`
	ClaudeMD    string            `json:"claude_md"`
	CodexMD     string            `json:"codex_md"`
	CursorRules string            `json:"cursor_rules"`
	EnvTemplate string            `json:"env_template"`
	MCPConfig   map[string]string `json:"mcp_config"`
	SampleCode  map[string]string `json:"sample_code"`
}

// mcpServerURL is the public Streamable HTTP endpoint of the Eurobase MCP server.
// Routed by deploy/k8s/ingress.yaml at host mcp.eurobase.app.
const mcpServerURL = "https://mcp.eurobase.app/mcp"

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

		_, _, ok := RequireRole(w, r, pool, projectID, "viewer")
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

		// Build AGENTS.md content (for OpenAI Codex).
		codexMD := generateCodexMD(name, slug, apiURL, plan, tables)

		// Build .cursorrules content.
		cursorRules := generateCursorRules(name, slug, apiURL, tables)

		// Build MCP server config snippets per IDE. Users paste these into
		// their IDE's MCP config file along with their platform JWT.
		mcpConfig := generateMCPConfig()

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
			MCPURL:      mcpServerURL,
			Tables:      tables,
			ClaudeMD:    claudeMD,
			CodexMD:     codexMD,
			CursorRules: cursorRules,
			EnvTemplate: envTemplate,
			MCPConfig:   mcpConfig,
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

	fmt.Fprintf(&b, "## MCP Server (Claude Code)\n\n")
	fmt.Fprintf(&b, "Eurobase ships an MCP server so Claude Code can operate on this project directly — list tables, run SQL, inspect users/files, read & write Vault secrets, invoke edge functions.\n\n")
	fmt.Fprintf(&b, "Add it once in your shell:\n\n")
	fmt.Fprintf(&b, "```bash\n")
	fmt.Fprintf(&b, "claude mcp add --transport http eurobase %s \\\n", mcpServerURL)
	fmt.Fprintf(&b, "  --header \"Authorization: Bearer $EUROBASE_PLATFORM_TOKEN\"\n")
	fmt.Fprintf(&b, "```\n\n")
	fmt.Fprintf(&b, "`EUROBASE_PLATFORM_TOKEN` is your console JWT (sign in at https://console.eurobase.app, it's the `access_token` returned by `/platform/auth/signin`). Tools are auto-namespaced as `mcp__eurobase__*`.\n\n")

	fmt.Fprintf(&b, "### MCP vs SDK vs migrations — pick the right channel\n\n")
	fmt.Fprintf(&b, "These are not interchangeable. Same project, three audiences:\n\n")
	fmt.Fprintf(&b, "- **SDK (`@eurobase/sdk`)** — code you write into the *application*. Runs in production at request-time, scoped by RLS to the end-user's JWT. Use it for everything the deployed app does on behalf of its users.\n")
	fmt.Fprintf(&b, "- **MCP** — tool calls *you* make during the coding session, scoped by my platform JWT. Use it to *inspect* (list tables, describe schema, run SELECT) and for *throwaway* operations.\n")
	fmt.Fprintf(&b, "- **Migrations (`migrations/NNNNNN_*.up.sql`)** — durable schema changes, version-controlled, reviewed in PR, replayed in CI on every environment.\n\n")
	fmt.Fprintf(&b, "Rules of thumb when the user asks me to make a change:\n\n")
	fmt.Fprintf(&b, "1. **Schema change of any kind** (`CREATE TABLE`, `ALTER TABLE`, `DROP`, new index, new RLS policy) → write a migration file under `migrations/`. **Do not** call `mcp__eurobase__db_execute_sql` to silently mutate the live DB; the change won't exist on staging or in any teammate's branch.\n")
	fmt.Fprintf(&b, "2. **App feature work** (\"add a sign-up form\", \"render the orders list\") → write SDK code in the app. The user's deployed app runs it.\n")
	fmt.Fprintf(&b, "3. **Inspection** (\"how many rows? what columns?\") → MCP `db_query` / `db_describe_table`. Don't hand-write a `psql` snippet.\n")
	fmt.Fprintf(&b, "4. **Throwaway debugging** (\"add a `tmp_debug` column to poke at this\") → MCP is fine, but say so explicitly and remove it before the session ends.\n")
	fmt.Fprintf(&b, "5. **Vault writes / production data mutations** via MCP → confirm with the user first. There's no undo.\n\n")
	fmt.Fprintf(&b, "Default to read-only via MCP. If a tool call would alter persistent state and isn't backed by a migration or SDK code path, stop and confirm.\n\n")

	fmt.Fprintf(&b, "## Constraints\n\n")
	fmt.Fprintf(&b, "- All infrastructure is EU-only (Scaleway, Paris FR)\n")
	fmt.Fprintf(&b, "- No US-incorporated services (AWS, GCP, Azure, Stripe, Vercel, Cloudflare)\n")
	fmt.Fprintf(&b, "- GDPR-native by design\n")

	return b.String()
}

func generateCodexMD(name, slug, apiURL, plan string, tables []ConnectTable) string {
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

	fmt.Fprintf(&b, "## MCP Server (Codex)\n\n")
	fmt.Fprintf(&b, "Eurobase ships a Streamable-HTTP MCP server so Codex can operate on this project directly — list tables, run SQL, inspect users/files, read & write Vault secrets, invoke edge functions.\n\n")
	fmt.Fprintf(&b, "Add the following to `~/.codex/config.toml` (Codex 0.20+):\n\n")
	fmt.Fprintf(&b, "```toml\n")
	fmt.Fprintf(&b, "[mcp_servers.eurobase]\n")
	fmt.Fprintf(&b, "url = \"%s\"\n", mcpServerURL)
	fmt.Fprintf(&b, "headers = { Authorization = \"Bearer ${EUROBASE_PLATFORM_TOKEN}\" }\n")
	fmt.Fprintf(&b, "```\n\n")
	fmt.Fprintf(&b, "`EUROBASE_PLATFORM_TOKEN` is your console JWT (the `access_token` returned by `/platform/auth/signin`).\n\n")

	fmt.Fprintf(&b, "### MCP vs SDK vs migrations — pick the right channel\n\n")
	fmt.Fprintf(&b, "These are not interchangeable. Same project, three audiences:\n\n")
	fmt.Fprintf(&b, "- **SDK (`@eurobase/sdk`)** — code you write into the *application*. Runs in production at request-time, scoped by RLS to the end-user's JWT. Use it for everything the deployed app does on behalf of its users.\n")
	fmt.Fprintf(&b, "- **MCP** — tool calls *you* make during the coding session, scoped by my platform JWT. Use it to *inspect* (list tables, describe schema, run SELECT) and for *throwaway* operations.\n")
	fmt.Fprintf(&b, "- **Migrations (`migrations/NNNNNN_*.up.sql`)** — durable schema changes, version-controlled, reviewed in PR, replayed in CI on every environment.\n\n")
	fmt.Fprintf(&b, "Rules of thumb when the user asks me to make a change:\n\n")
	fmt.Fprintf(&b, "1. **Schema change of any kind** (`CREATE TABLE`, `ALTER TABLE`, `DROP`, new index, new RLS policy) → write a migration file under `migrations/`. **Do not** call `db_execute_sql` to silently mutate the live DB; the change won't exist on staging or in any teammate's branch.\n")
	fmt.Fprintf(&b, "2. **App feature work** (\"add a sign-up form\", \"render the orders list\") → write SDK code in the app. The user's deployed app runs it.\n")
	fmt.Fprintf(&b, "3. **Inspection** (\"how many rows? what columns?\") → MCP `db_query` / `db_describe_table`. Don't hand-write a `psql` snippet.\n")
	fmt.Fprintf(&b, "4. **Throwaway debugging** (\"add a `tmp_debug` column to poke at this\") → MCP is fine, but say so explicitly and remove it before the session ends.\n")
	fmt.Fprintf(&b, "5. **Vault writes / production data mutations** via MCP → confirm with the user first. There's no undo.\n\n")
	fmt.Fprintf(&b, "Default to read-only via MCP. If a tool call would alter persistent state and isn't backed by a migration or SDK code path, stop and confirm.\n\n")

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

	fmt.Fprintf(&b, "## MCP Server\n\n")
	fmt.Fprintf(&b, "Eurobase ships an MCP server. Add it to `~/.cursor/mcp.json` (or your workspace's `.cursor/mcp.json`) so Cursor can operate on this project directly — list tables, run SQL, inspect users/files, read & write Vault secrets, invoke edge functions:\n\n")
	fmt.Fprintf(&b, "```json\n")
	fmt.Fprintf(&b, "{\n")
	fmt.Fprintf(&b, "  \"mcpServers\": {\n")
	fmt.Fprintf(&b, "    \"eurobase\": {\n")
	fmt.Fprintf(&b, "      \"url\": \"%s\",\n", mcpServerURL)
	fmt.Fprintf(&b, "      \"headers\": { \"Authorization\": \"Bearer YOUR_PLATFORM_TOKEN\" }\n")
	fmt.Fprintf(&b, "    }\n")
	fmt.Fprintf(&b, "  }\n")
	fmt.Fprintf(&b, "}\n")
	fmt.Fprintf(&b, "```\n\n")
	fmt.Fprintf(&b, "`YOUR_PLATFORM_TOKEN` is your console JWT (the `access_token` returned by `/platform/auth/signin`).\n\n")
	fmt.Fprintf(&b, "## When to use MCP vs SDK vs migrations\n\n")
	fmt.Fprintf(&b, "- SDK (`@eurobase/sdk`) → code that runs in the *deployed app* at request-time, scoped by end-user RLS\n")
	fmt.Fprintf(&b, "- MCP → tool calls the *agent* makes during a coding session; default to read-only (SELECT, describe)\n")
	fmt.Fprintf(&b, "- Migrations (`migrations/NNNNNN_*.up.sql`) → durable schema changes, reviewed in PR, replayed in CI\n\n")
	fmt.Fprintf(&b, "Rules:\n\n")
	fmt.Fprintf(&b, "- Any `CREATE TABLE` / `ALTER TABLE` / `DROP` / index / RLS policy → write a migration file. Do not silently mutate the live DB via MCP `db_execute_sql`; the change won't exist on staging or in any teammate's branch.\n")
	fmt.Fprintf(&b, "- App feature work → SDK code in the app, not MCP.\n")
	fmt.Fprintf(&b, "- Inspection (counts, schema, sample rows) → MCP `db_query` / `db_describe_table`.\n")
	fmt.Fprintf(&b, "- Vault writes or production data mutations via MCP → confirm with the user first.\n\n")

	fmt.Fprintf(&b, "## Constraints\n\n")
	fmt.Fprintf(&b, "- EU-only infrastructure, no US cloud services\n")
	fmt.Fprintf(&b, "- GDPR compliant — handle user data accordingly\n")

	return b.String()
}

// generateMCPConfig returns ready-to-paste MCP-server config snippets keyed
// by IDE: claude (CLI invocation), claude_json (settings.json block),
// codex (config.toml block), cursor (mcp.json block), windsurf (mcp.json block).
func generateMCPConfig() map[string]string {
	return map[string]string{
		"claude": fmt.Sprintf(`claude mcp add --transport http eurobase %s \
  --header "Authorization: Bearer $EUROBASE_PLATFORM_TOKEN"`, mcpServerURL),

		"claude_json": fmt.Sprintf(`{
  "mcpServers": {
    "eurobase": {
      "type": "http",
      "url": "%s",
      "headers": {
        "Authorization": "Bearer YOUR_PLATFORM_TOKEN"
      }
    }
  }
}`, mcpServerURL),

		"codex": fmt.Sprintf(`[mcp_servers.eurobase]
url = "%s"
headers = { Authorization = "Bearer ${EUROBASE_PLATFORM_TOKEN}" }`, mcpServerURL),

		"cursor": fmt.Sprintf(`{
  "mcpServers": {
    "eurobase": {
      "url": "%s",
      "headers": {
        "Authorization": "Bearer YOUR_PLATFORM_TOKEN"
      }
    }
  }
}`, mcpServerURL),

		"windsurf": fmt.Sprintf(`{
  "mcpServers": {
    "eurobase": {
      "serverUrl": "%s",
      "headers": {
        "Authorization": "Bearer YOUR_PLATFORM_TOKEN"
      }
    }
  }
}`, mcpServerURL),
	}
}
