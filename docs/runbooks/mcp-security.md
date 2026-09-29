# Runbook — MCP server security model

## Threat model

The Eurobase MCP server (`mcp-server/`) is a Node/TS HTTP proxy in front of the platform API. Developers run it locally and connect to it from Cursor, Claude Code, Codex, or Windsurf via the Model Context Protocol. The MCP authenticates to Eurobase with a platform JWT and exposes tools like `runSQL`, `listTables`, `getSecret`, `setSecret`.

The threat the rest of this document addresses: **indirect prompt injection through tool outputs**. An end-user of a Eurobase-backed project posts data through the SDK (anon role — public access). That data lands in a tenant table — e.g. a `support_messages.body` field. When a developer later reviews that row via MCP, the LLM reads the row content and may treat embedded text as instructions. If the LLM is convinced to call `runSQL` with a constructed query, it can:

1. Read sensitive tables in the developer's project (`refresh_tokens`, `email_tokens`, `vault_secrets`)
2. Exfiltrate the data back into a reply visible to the attacker

This is the same vulnerability class disclosed by General Analysis against Supabase in April 2026.

## Defences

Three layers, applied in order:

### 1. RLS gate on credential tables (#164, migration 000055)

`refresh_tokens`, `email_tokens`, `vault_secrets` had `USING (public.is_service_role())` — true whenever `app.end_user_role='service'`. The generic SQL handler sets that GUC for developer-authenticated traffic, so a prompt-injected `runSQL` would read those tables.

Migration 000055 narrows the policy to require a NEW, more-specific GUC:

```sql
USING (public.is_internal_auth_path())
-- which is:  current_setting('app.intent', true) = 'internal_auth_path'
```

Only the legitimate Go code paths that need these tables set `app.intent`:

| Path | Helper |
|---|---|
| `internal/email/service.go` | `asService()` → `db.RunAsAuthService` |
| `internal/sms/service.go` | `RunAsAuthService` |
| `internal/enduser/auth_service.go` | `asService()` → `RunAsAuthService` |
| `internal/enduser/platform_handler.go` (revoke flows) | `RunAsAuthService` |
| `internal/enduser/gdpr_export.go` | `RunAsAuthService` |
| `internal/vault/service.go` | `RunAsAuthService` |
| `internal/gateway/token_cleanup.go` | `RunAsAuthService` |

A generic `runSQL` call via MCP gets `app.end_user_role='service'` but **not** `app.intent='internal_auth_path'`. RLS rejects at the policy layer. **This is the primary fix.**

Adding a new caller to that list is a security-review-worthy change. If you're tempted to use `RunAsAuthService` from a new path, ask: does this path read or write credential / token / vault data? If not, use the existing `RunAsService`.

### 2. The token decides what MCP may write (#702, replaces #165's switch)

The MCP server has no access of its own, and no server-side switch: what a connection may do comes from its token.

- **Project tokens** (`eb_ptk_…`, one project, one access level). The SQL tools send statements as asked and the gateway allows or refuses them by the token's role. A **Read-only** token can't run SQL at all; it reads through `queryTable` / `downloadFile`. A **Developer** or **Admin** token can write and change the schema — including under a prompt-injected instruction.
- **Legacy all-projects tokens** (`eb_pat_…`). The SQL tools set `read_only: true`, so the backend wraps the transaction in `SET TRANSACTION READ ONLY` and any INSERT / UPDATE / DELETE / DDL raises `SQLSTATE 25006` and rolls back — #165's defence, unchanged for these tokens.

**Trade-off (accepted):** a Developer token given to an AI tool (Lovable, Claude Code, Cursor) no longer has #165's read-only backstop against prompt injection via data. Guidance: give tools a **Read-only** token unless they need to change the project, scope write tokens to one project (they can't reach any other), and revoke them when done. `EUROBASE_MCP_ALLOW_WRITES` no longer exists.

### 3. Output sanitisation (planned)

A follow-up will scan stringy column values for prompt-injection signatures (`CLAUDE`, `IGNORE PREVIOUS`, `<|im_start|>`, etc.) before returning rows to the LLM and replace them with `[REDACTED — possible prompt injection]`. Not bulletproof — encoded payloads will get through — but every layer the attacker has to defeat costs them.

**Not in this PR.** See the OPEN section below.

## Audit trail

Every MCP-origin SQL call writes an `audit_log` row:

| Action | When |
|---|---|
| `mcp.sql.executed` | Successful MCP `runSQL` / `runSQLTransaction` |
| `mcp.sql.rejected_write_in_readonly` | LLM attempted a write under read-only mode |

Filter by these in Compliance → Audit Log to spot unusual MCP traffic. A spike of `mcp.sql.rejected_write_in_readonly` in a single session is a strong signal of a prompt-injection attempt.

## Developer guidance

When you run the MCP server locally and connect Cursor or Claude Code:

- **Treat your project DB as a hostile input source when reviewing data via the LLM.** Rows that came in through the public SDK are attacker-controlled text. Don't assume the LLM will recognise instructions embedded in support messages, comments, profile bios, etc.
- **Give interactive sessions a Read-only project token** unless they must change the schema. For migrations, run the CLI directly: `eurobase admin migrate up`.
- **Audit-log review is part of the on-call rotation.** Look for `mcp.sql.*` actions in the Compliance feed of any project you administer.

## What this DOES NOT protect against

- **A compromised developer machine.** The MCP runs as the developer; the LLM doesn't matter — anything the developer can read, the attacker controls.
- **A model-aware encoded payload** that passes through the sanitiser unchanged.
- **A genuinely-broken policy** elsewhere in the database. Layer 1 only covers the three tables we manually narrowed.
- **Vault secret encryption breaches.** Layer 1 prevents reading `vault_secrets.ciphertext`. It does NOT protect the encryption key itself (single global key today — see #51 for the planned per-tenant rotation).

## Open follow-ups

- **Output sanitiser** — flag rows containing common prompt-injection signatures before returning to the LLM.
- **MCP-aware audit metadata** — add `source: 'mcp'` to the existing SQL-call audit rows so they're filterable as a distinct stream.
- **Per-tenant vault encryption keys (#51)** — moves the encryption-key blast radius from "platform-wide" to "single tenant".
- **Output sanitiser configuration** — operator-controlled patterns + redaction template.
