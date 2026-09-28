# Eurobase skill for Lovable

Build Lovable apps on [Eurobase](https://eurobase.app), the EU-sovereign backend (Postgres, auth, storage, realtime, edge functions), instead of Lovable Cloud or Supabase. This skill teaches Lovable to use `@eurobase/sdk` correctly: the right method signatures, row-level security, file privacy, server-side rendering, and where secrets belong.

The same steps, pre-filled for your project, are on the Eurobase console → your project → **Connect** → **Lovable**.

## Install in Lovable

1. **Stop Lovable from auto-creating a Supabase backend.** Connectors → Cloud → Manage my agent's permissions → set **Enable Cloud** to **Ask each time**. (Decline if it asks during a Eurobase project.)
2. **Import the skill.** Settings → Skills → Add → Import from GitHub, and paste this repository's URL. Requires workspace owner or admin.
3. **Connect the Eurobase MCP server** so Lovable can see and create your tables. Connectors → + → MCP server. Server URL: `https://mcp.eurobase.app/mcp`. Authentication: Bearer token — paste a Personal Access Token from [console.eurobase.app](https://console.eurobase.app) → Account → Personal Access Tokens (create a dedicated one with an expiry; it can reach all your Eurobase projects).
4. **Allow your app's URLs** in the Eurobase console → your project → **Auth**: add the Lovable preview URL, your `*.lovable.app` URL and any custom domain to **Allowed CORS origins** and **Allowed redirect URLs** — or paste them into Connect → Lovable, which adds them to both.
5. **Start your project** with a prompt like: "Use the /eurobase skill. This app uses Eurobase as its backend, not Lovable Cloud. My project URL is https://my-app.eurobase.app and my public key is eb_pk_…. Build a …"

Only the public key (`eb_pk_…`) ever goes into Lovable. Never paste a secret key (`eb_sk_…`).

## Not a workspace admin?

Paste the contents of `SKILL.md` into **Project settings → Knowledge** instead. It then applies to that project only.

## Use with Claude Code, Cursor and others

`SKILL.md` follows the Agent Skills format. For Claude Code, copy this folder to `.claude/skills/eurobase/` in your repo.

## License

MIT
