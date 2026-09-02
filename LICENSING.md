# Licensing

This repository ships under **two different licenses** depending on the directory. Read this before opening a PR, forking, or embedding any of the code.

## Quick reference

| Directory / Component | License | What you can do |
|---|---|---|
| `sdk/` — the TypeScript SDK (`@eurobase/sdk` on npm) | **MIT** | Anything. Use it, ship it in your product, fork it, sell it. Attribution required per the MIT terms. |
| `mcp-server/` — the Model Context Protocol server (`@eurobase/mcp-server` on npm) | **MIT** | Same as SDK — permissive by design. |
| `docs/legal/` — Terms, Privacy, DPA, AUP, sub-processors, compliance dossier | **Separately governed** — each document sets its own effective date, versioning, and change-notice rules (see the reviewer note at the top of `terms.md`). Not code; the BUSL grant below does not apply. | Read as the authoritative published contract; changes flow via the `/legal/v{major}/` versioning convention. |
| **Everything else** — Go backend (`cmd/`, `internal/`), functions runner (`functions-runner/`), console (`console/`), migrations (`migrations/`), scripts, deploy manifests | **[Business Source License 1.1 (BUSL-1.1)](./LICENSE)** with an Additional Use Grant | Read the code, study it, contribute PRs, and run it in production for your own applications and internal tools. You may **NOT** operate it as a hosted, managed, or embedded backend-as-a-service in competition with Eurobase's commercial offering at eurobase.app. Automatically converts to Apache 2.0 four years after each Git commit is first pushed to the public repository. |

If you're unsure whether your intended use falls inside or outside the Additional Use Grant, email **licensing@eurobase.app** — we'll answer plainly.

## Why the split

**SDK + MCP server: MIT.** Developers embed these in their applications. A restrictive license on client-side integration code hurts adoption without protecting anything commercial — the SDK reveals nothing about our operational moat, it just talks to our API.

**Everything else: BUSL-1.1.** The commercial value of Eurobase is the *managed, EU-sovereign operation* of the backend, not the code itself. BUSL lets us keep the code readable, forkable for self-hosting, and open to PR contributions — while preventing a well-resourced third party (a US hyperscaler, a competing EU cloud) from taking the codebase and offering "Eurobase-compatible managed service" for less. The four-year Change Date means every version eventually becomes Apache 2.0 anyway.

This is the same shape used by CockroachDB, HashiCorp Terraform / Vault, Sentry, and Grafana Labs. It's a well-understood pattern; your legal team has probably seen it before.

## The Additional Use Grant, in plain English

The BUSL-1.1 default is *"no production use."* We add an Additional Use Grant that widens that considerably. What's **allowed** under our grant:

- Running Eurobase in production for your own apps
- Running Eurobase in production for your organisation's internal tools
- Running Eurobase on behalf of a single client engagement (agency work, consultancy) where the client is the operator's customer
- Any non-production use — dev, staging, evaluation, CI, teaching, homelab, coursework
- Forking the code, submitting PRs, running the tests, learning from the implementation

What's **not allowed** under our grant (requires a commercial license from us):

- Offering Eurobase (or a modified version) to third parties as a hosted / managed / embedded BaaS
- Rebadging Eurobase as a competing product
- Multi-tenant SaaS operation of Eurobase where users can sign up and get their own project

If you're building a product on top of Eurobase, you're fine — your product is the customer, Eurobase is the backend. If you're building a *replacement for Eurobase's paid tiers*, that's the case we're carving out.

## Contributing

By opening a PR to any part of this repository you agree to license your contribution under the same license that governs the file you're editing (BUSL-1.1 for backend / console / migrations / etc; MIT for `sdk/` or `mcp-server/`). We don't require a separate CLA today; the license inheritance covers it.

If your PR crosses the boundary (e.g. adds a new SDK method that requires a backend change), submit the two halves as separate commits so each side inherits the correct license.

## Trademarks

"Eurobase" and the Eurobase logo are trademarks of Eurobase OÜ. The BUSL and MIT grants above do **not** include any trademark license. You may reference "Eurobase" nominatively (e.g. "compatible with Eurobase") but you may not use the name or logo to imply endorsement, affiliation, or continuity with our operated service.

## Contact

- **Commercial licensing / dual-license exceptions:** licensing@eurobase.app
- **Trademark questions:** legal@eurobase.app
- **General:** contact@eurobase.app

---

*This document is informational. In case of conflict between this document and the actual LICENSE files, the LICENSE files govern.*
