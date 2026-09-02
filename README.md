# Eurobase

**The EU-sovereign Backend-as-a-Service.** Postgres, auth, storage, edge functions, realtime, and an encrypted vault — managed on Scaleway (France), operated by [Eurobase OÜ](https://eurobase.app/legal) (Estonia). No US infrastructure anywhere in the critical path.

- **Website + pricing:** [eurobase.app](https://eurobase.app)
- **Console:** [console.eurobase.app](https://console.eurobase.app)
- **Docs:** [docs.eurobase.app](https://docs.eurobase.app)
- **Security & compliance:** [eurobase.app/security](https://eurobase.app/security)

## Repository layout

| Path | What it is |
|---|---|
| `cmd/gateway/` | HTTP gateway — the only public ingress. Serves `/v1/*` (SDK runtime) and `/platform/*` (console + MCP). |
| `cmd/worker/` | Background worker pod — cron, [River](https://riverqueue.com) jobs, retention sweepers. |
| `functions-runner/` | Deno-based edge function runtime, HMAC-signed gateway↔runner. |
| `internal/` | Go domain packages: `auth/`, `billing/`, `tenant/`, `vault/`, `db/`, `plans/`, `workers/`, `functions/`, `storage/`, `webhook/`, `audit/`, `email/`, `dbprovider/`, etc. |
| `migrations/` | Numbered `.up.sql` / `.down.sql` migrations. Applied by CI via a `migrate` Kubernetes Job before each gateway rollout. |
| `console/` | SvelteKit console — the operator UI at `console.eurobase.app`. |
| `sdk/js/` | TypeScript SDK, published as [`@eurobase/sdk`](https://www.npmjs.com/package/@eurobase/sdk). |
| `mcp-server/` | Model Context Protocol server for Claude Code / IDE integrations. |
| `deploy/` | Kubernetes manifests, Dockerfiles, deployment configuration. |
| `scripts/` | Ops + verification scripts (`setup-local.sh`, migration verifiers, ops one-offs). |
| `docs/legal/` | The versioned source-of-truth legal doc set (Terms, Privacy, DPA, AUP, sub-processors, compliance dossier). |
| `CLAUDE.md` | Project-specific conventions for AI-assisted development. |

## Getting started

**As a Eurobase user** (building on top of it):
```bash
npm install @eurobase/sdk
```
See [docs.eurobase.app](https://docs.eurobase.app) for the SDK reference and quickstarts.

**As a contributor or self-hoster** (running the platform locally):
```bash
git clone https://github.com/STGime/euroback.git
cd euroback
./scripts/setup-local.sh   # spins up Postgres + Redis + MinIO via docker-compose, applies migrations
```
Detailed local-dev walkthrough is in the `docs/` directory. A proper `eb dev up` command that packages the whole local stack into one CLI is planned; watch the [public issue tracker](https://github.com/STGime/euroback/issues) for progress.

## Contributing

Issues and pull requests welcome. Please read [`LICENSING.md`](./LICENSING.md) first — this repo uses two licenses depending on the directory (MIT for `sdk/` and `mcp-server/`; BUSL-1.1 for everything else). Your contribution inherits the license of the file you're editing; no separate CLA required today.

Bug reports and feature ideas via the [public issue tracker](https://github.com/STGime/euroback/issues).

## License

Two-license split — see [`LICENSING.md`](./LICENSING.md) for the plain-English explainer and [`LICENSE`](./LICENSE) for the BUSL-1.1 text.

**Quick reference:**
- `sdk/`, `mcp-server/` → **MIT** — permissive; embed anywhere.
- Everything else → **BUSL-1.1** with an Additional Use Grant — read/study/self-host for your own apps + internal tools + single-customer engagements. Auto-converts to Apache 2.0 four years after each release. Offering it as a competing managed BaaS requires a commercial license (contact `licensing@eurobase.app`).

## Contact

- **General:** contact@eurobase.app
- **Security disclosure:** security@eurobase.app (see [RFC 9116 `security.txt`](https://eurobase.app/.well-known/security.txt))
- **Data protection:** dpo@eurobase.app
- **Commercial licensing:** licensing@eurobase.app
