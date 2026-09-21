		<section id="connect-db" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">25. Direct Postgres connection <span class="text-sm font-normal text-emerald-700">(Team &amp; Legal Team)</span></h2>
			<p class="text-sm italic text-gray-500 mb-4">
				When the SDK / REST / edge-functions / SQL editor / MCP aren't enough — because your stack expects a raw <code class="rounded bg-gray-100 px-1 text-[11px]">postgres://</code> URL.
			</p>

			<div class="rounded-md bg-amber-50 border border-amber-200 p-3 text-xs text-amber-900 mb-4">
				<strong>Closed beta — invite only.</strong> Team &amp; Legal Team tiers are not self-serve yet. If you'd like access,
				email <a href="mailto:contact@eurobase.app" class="underline hover:no-underline">contact@eurobase.app</a> with a
				one-line description of the workload. Grants are manual during the beta window.
			</div>

			<div class="rounded-xl border border-gray-200 bg-white p-6 space-y-4">
				<h3 class="text-base font-semibold text-gray-900">Why this exists</h3>
				<p class="text-sm text-gray-700">
					On Free and Pro, every project shares one Postgres cluster with per-tenant schema isolation + RLS.
					We don't hand out direct connection strings on those tiers — the isolation model relies on you
					going through our authenticated surface (SDK, REST, MCP, edge functions with <code class="rounded bg-gray-100 px-1 text-[11px]">ctx.db.sql</code>, the console SQL editor).
				</p>
				<p class="text-sm text-gray-700">
					That covers most application code — but not tools that want to own the Postgres connection themselves:
				</p>
				<ul class="list-disc pl-5 text-sm text-gray-700 space-y-1">
					<li><strong>Payload CMS</strong> — Postgres adapter requires <code class="rounded bg-gray-100 px-1 text-[11px]">DATABASE_URL</code></li>
					<li><strong>Prisma</strong> — same</li>
					<li><strong>Drizzle</strong> — same</li>
					<li><strong>Directus</strong>, <strong>Strapi</strong> (Postgres backend) — same</li>
					<li>Migration runners: Flyway, Liquibase, <code class="rounded bg-gray-100 px-1 text-[11px]">node-pg-migrate</code>, <code class="rounded bg-gray-100 px-1 text-[11px]">sqlx</code></li>
					<li>Ad-hoc <code class="rounded bg-gray-100 px-1 text-[11px]">psql</code> from your laptop</li>
				</ul>

				<h3 class="text-base font-semibold text-gray-900 pt-2">What Team unlocks</h3>
				<p class="text-sm text-gray-700">
					Every Team-tier project (and Legal-Team) gets its own <strong>dedicated Postgres instance</strong> in
					Scaleway fr-par — no shared cluster, no cross-tenant noise. That instance has its own connection
					string, exposed at <strong>Project → Database → Connection</strong> in the console.
				</p>
				<p class="text-sm text-gray-700">
					Two roles: <strong>read-only</strong> (default — safe for analytics, debugging, most reads) and
					<strong>read/write</strong> (owner role — for migrations + writes). Both work with any Postgres client.
				</p>

				<div class="rounded-md bg-amber-50 border border-amber-200 p-3 text-xs text-amber-900">
					<strong>Bearer credential.</strong> The URL contains the password. Every console fetch is audited
					(<code class="rounded bg-amber-100 px-1">team.connection.url_viewed</code>). If a URL leaks, hit
					the <strong>Rotate password</strong> button — the old URL stops working within seconds.
				</div>

				<h3 class="text-base font-semibold text-gray-900 pt-2">Client examples</h3>

				<div>
					<p class="text-xs font-medium text-gray-500 mb-1">Payload CMS</p>
					<pre class="rounded-md bg-gray-900 p-3 text-xs text-gray-100 overflow-x-auto"><code>// payload.config.ts
import {'{'} postgresAdapter {'}'} from '@payloadcms/db-postgres';

export default buildConfig({'{'}
  db: postgresAdapter({'{'}
    pool: {'{'} connectionString: process.env.DATABASE_URL {'}'},
  {'}'}),
{'}'});</code></pre>
				</div>

				<div>
					<p class="text-xs font-medium text-gray-500 mb-1">Prisma</p>
					<pre class="rounded-md bg-gray-900 p-3 text-xs text-gray-100 overflow-x-auto"><code>// schema.prisma
datasource db {'{'}
  provider = "postgresql"
  url      = env("DATABASE_URL")
{'}'}</code></pre>
				</div>

				<div>
					<p class="text-xs font-medium text-gray-500 mb-1">Drizzle</p>
					<pre class="rounded-md bg-gray-900 p-3 text-xs text-gray-100 overflow-x-auto"><code>import {'{'} drizzle {'}'} from 'drizzle-orm/postgres-js';
import postgres from 'postgres';

const client = postgres(process.env.DATABASE_URL!);
export const db = drizzle(client);</code></pre>
				</div>

				<div>
					<p class="text-xs font-medium text-gray-500 mb-1">psql</p>
					<pre class="rounded-md bg-gray-900 p-3 text-xs text-gray-100 overflow-x-auto"><code>psql "$DATABASE_URL"</code></pre>
				</div>

				<h3 class="text-base font-semibold text-gray-900 pt-2">Free / Pro alternatives</h3>
				<p class="text-sm text-gray-700">
					If you don't need direct connection, all Free/Pro projects can still work with tools that speak
					our SDK / REST — see chapters
					<a href="/docs/database" class="text-eurobase-700 hover:underline cursor-pointer">4</a>,
					<a href="/docs/api" class="text-eurobase-700 hover:underline cursor-pointer">10</a>,
					<a href="/docs/mcp" class="text-eurobase-700 hover:underline cursor-pointer">23</a>.
					When you're ready for a stack that wants raw Postgres, upgrade to Team from Project → Settings.
				</p>
			</div>

			<div class="mt-4">
				<a href="/docs/team-tier" class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Team tier — dedicated Postgres, backups, snapshots &rarr;
				</a>
			</div>
		</section>
