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

### 2. Read-only MCP `runSQL` by default (#165)

The MCP server's `runSQL` and `runSQLTransaction` tools now set `read_only: true` on the platform `/data/sql` request body by default. The backend wraps the transaction in `SET TRANSACTION READ ONLY`, so any embedded INSERT / UPDATE / DELETE / DDL raises `SQLSTATE 25006` (`read_only_sql_transaction`) and rolls back.

Effect on the attack chain: even if a prompt-injected `runSQL` query could read tokens (defeated by layer 1, but defence in depth), it cannot write them back into a row the attacker can later read. The exfil step fails.

**Opt out** by setting `EUROBASE_MCP_ALLOW_WRITES=true` on the MCP server's environment and restarting. Intended for migration-running scripts (`eurobase admin migrate`) — **never enable for interactive Cursor / Claude Code sessions** where prompt-injection-via-data is in scope.

The tool description visible to the LLM updates dynamically to reflect the current mode, so the LLM doesn't try to coach the user into bypassing the limit.

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
- **Leave `EUROBASE_MCP_ALLOW_WRITES` unset for interactive sessions.** If you need a migration, run the CLI directly: `eurobase admin migrate up`.
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
