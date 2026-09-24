// AUTO-GENERATED chapter registry for the public docs (/docs). Each
// chapter is its own pre-rendered URL so search engines and AI answer
// engines can index and cite one chapter at a time. Descriptions are
// the first paragraph of each chapter; edit the chapter, not this file.
import type { Component } from 'svelte';

export interface DocChapter {
	id: string;
	num: number;
	label: string;
	title: string;
	description: string;
	file: string;
}

export const chapters: DocChapter[] = [
	{"id": "welcome", "num": 0, "label": "Welcome", "title": "Welcome", "description": "A guided tour of Eurobase through the eyes of a real project.", "file": "00-welcome.svelte"},
	{"id": "signup", "num": 1, "label": "1. Signing Up", "title": "1. Signing Up", "description": "Alex has heard about Eurobase and navigates to the login page.", "file": "01-signup.svelte"},
	{"id": "create-project", "num": 2, "label": "2. Creating Your First Project", "title": "2. Creating Your First Project", "description": "Alex is signed in and ready to set up LexVault's backend.", "file": "02-create-project.svelte"},
	{"id": "dashboard", "num": 3, "label": "3. The Project Dashboard", "title": "3. The Project Dashboard", "description": "LexVault is created. Alex lands on the project overview.", "file": "03-dashboard.svelte"},
	{"id": "database", "num": 4, "label": "4. Building the Database", "title": "4. Building the Database", "description": "Alex needs a clients table for LexVault's law firm customers.", "file": "04-database.svelte"},
	{"id": "storage", "num": 5, "label": "5. File Storage", "title": "5. File Storage", "description": "Alex needs to store legal documents that clients upload.", "file": "05-storage.svelte"},
	{"id": "auth", "num": 6, "label": "6. Authentication Setup", "title": "6. Authentication Setup", "description": "Alex needs to let law firm employees sign into LexVault securely.", "file": "06-auth.svelte"},
	{"id": "rate-limits", "num": 7, "label": "7. Rate Limits", "title": "7. Rate Limits", "description": "LexVault is about to launch publicly. Alex wants safeguards so a runaway script can't burn through the project's email budget or hammer the auth endpoints.", "file": "07-rate-limits.svelte"},
	{"id": "custom-smtp", "num": 8, "label": "8. Custom SMTP", "title": "8. Custom SMTP", "description": "LexVault grows past the platform email cap. Alex's signups start hitting the 2-emails-per-hour ceiling. He plugs in the firm's own SMTP provider and the…", "file": "08-custom-smtp.svelte"},
	{"id": "users", "num": 9, "label": "9. Managing End Users", "title": "9. Managing End Users", "description": "A law firm onboards new employees. Alex needs to manage their accounts.", "file": "09-users.svelte"},
	{"id": "api", "num": 10, "label": "10. Exploring the API", "title": "10. Exploring the API", "description": "Alex wants to see every API endpoint available for the LexVault tables.", "file": "10-api.svelte"},
	{"id": "webhooks", "num": 11, "label": "11. Webhooks", "title": "11. Webhooks", "description": "Alex wants LexVault to be notified whenever a new client record is created.", "file": "11-webhooks.svelte"},
	{"id": "rls", "num": 12, "label": "12. Row-Level Security", "title": "12. Row-Level Security (RLS)", "description": "Alex needs each law firm employee to only see their own cases.", "file": "12-rls.svelte"},
	{"id": "vault", "num": 13, "label": "13. Vault (Secrets)", "title": "13. Vault (Encrypted Secrets)", "description": "Alex needs to store API keys for Mollie payments and Twilio SMS securely.", "file": "13-vault.svelte"},
	{"id": "cron", "num": 14, "label": "14. Scheduled Jobs", "title": "14. Scheduled Jobs", "description": "Alex needs to clean up expired sessions and send weekly reports automatically.", "file": "14-cron.svelte"},
	{"id": "edge-functions", "num": 15, "label": "15. Edge Functions", "title": "15. Edge Functions", "description": "Alex needs to process a payment webhook and update an order — this requires custom server-side logic beyond SQL.", "file": "15-edge-functions.svelte"},
	{"id": "logs", "num": 16, "label": "16. Monitoring with Logs", "title": "16. Monitoring with Logs", "description": "Alex notices slow responses and wants to investigate API traffic.", "file": "16-logs.svelte"},
	{"id": "compliance", "num": 17, "label": "17. Compliance, Audit Log & DSAR", "title": "17. Compliance, Audit Log & DSAR", "description": "Alex's client asks for proof that their data stays in the EU and a trail of who changed what — and a user emails LexVault asking \"what do you have on…", "file": "17-compliance.svelte"},
	{"id": "settings", "num": 18, "label": "18. Project Settings", "title": "18. Project Settings", "description": "Alex needs to rotate an API key after an intern accidentally committed it.", "file": "18-settings.svelte"},
	{"id": "team", "num": 19, "label": "19. Team Collaboration", "title": "19. Team Collaboration", "description": "Alex wants to give a colleague access to the project without sharing API keys.", "file": "19-team.svelte"},
	{"id": "cli", "num": 20, "label": "20. CLI Tool", "title": "20. CLI Tool", "description": "Alex wants to manage projects, run queries, and test RLS policies from the terminal.", "file": "20-cli.svelte"},
	{"id": "migrations", "num": 21, "label": "21. Schema Migrations", "title": "21. Schema Migrations", "description": "Alex wants schema changes that are versioned, reviewable, and repeatable across environments.", "file": "21-migrations.svelte"},
	{"id": "connect", "num": 22, "label": "22. Connecting Your IDE", "title": "22. Connecting Your IDE", "description": "Alex wants their AI coding assistant to understand the LexVault schema.", "file": "22-connect.svelte"},
	{"id": "mcp", "num": 23, "label": "23. MCP Server", "title": "23. MCP Server", "description": "Alex wants their AI assistant to actually do things in LexVault — list users, run a SELECT, rotate a Vault secret — not just read schema docs.", "file": "23-mcp.svelte"},
	{"id": "account", "num": 24, "label": "24. Your Account", "title": "24. Your Account", "description": "Alex wants to set a display name, update their password, and turn on multi-factor authentication with a passkey.", "file": "24-account.svelte"},
	{"id": "connect-db", "num": 25, "label": "25. Direct Postgres connection (Team)", "title": "25. Direct Postgres connection (Team & Legal Team)", "description": "When the SDK / REST / edge-functions / SQL editor / MCP aren't enough — because your stack expects a raw postgres:// URL.", "file": "25-connect-db.svelte"},
	{"id": "team-tier", "num": 26, "label": "26. Team tier — dedicated Postgres, backups, snapshots", "title": "26. Team tier (closed beta)", "description": "Alex's first pilot law firm signs on. LexVault needs production isolation, daily backups, and a way to safely try schema changes without holding their…", "file": "26-team-tier.svelte"},
	{"id": "orgs-sso", "num": 27, "label": "27. Organizations & SSO (OIDC)", "title": "27. Organizations & SSO (Team & Legal Team)", "description": "Alex hires Bea. She needs her own login for LexVault's projects — no shared passwords, no forwarded API keys.", "file": "27-orgs-sso.svelte"},
	{"id": "legal-tech", "num": 28, "label": "28. German legal-tech retention (Legal Team)", "title": "28. German legal-tech retention (Legal Team)", "description": "Per-prefix WORM policies and row/object-scoped retention holds for tenants subject to §50 BRAO, §257 HGB, or §147 AO.", "file": "28-legal-tech.svelte"},
	{"id": "next", "num": 29, "label": "What's Next", "title": "What's Next", "description": "Alex has a fully configured backend. Time to build LexVault's frontend.", "file": "29-next.svelte"},
];

export const chapterById: Record<string, DocChapter> = Object.fromEntries(chapters.map((c) => [c.id, c]));

// 'welcome' is rendered on the /docs index itself; every other chapter is a page.
export const pageChapters = chapters.filter((c) => c.id !== 'welcome');

const modules = import.meta.glob<{ default: Component }>('./chapters/*.svelte');

export async function loadChapterComponent(id: string): Promise<Component> {
	const c = chapterById[id];
	if (!c) throw new Error(`unknown docs chapter: ${id}`);
	const loader = modules[`./chapters/${c.file}`];
	if (!loader) throw new Error(`missing component for docs chapter: ${id}`);
	return (await loader()).default;
}

export function prevNext(id: string): { prev?: DocChapter; next?: DocChapter } {
	const i = pageChapters.findIndex((c) => c.id === id);
	return { prev: i > 0 ? pageChapters[i - 1] : undefined, next: i >= 0 && i < pageChapters.length - 1 ? pageChapters[i + 1] : undefined };
}
