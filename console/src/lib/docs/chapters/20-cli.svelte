<script lang="ts">
	import { goto } from '$app/navigation';
	// In the single-page docs this scrolled; each chapter is now its own
	// URL, so cross-references navigate instead.
	function scrollTo(id: string) {
		goto(`/docs/${id}`);
	}
</script>

		<section id="cli" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">20. CLI Tool</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex wants to manage projects, run queries, and test RLS policies from the terminal.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					The Eurobase CLI lets you manage your projects, database, storage, vault, and more from the command line. Install it via Homebrew or download the Go binary.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Installation</h3>
				<div class="rounded-lg bg-gray-900 px-4 py-3 font-mono text-xs text-green-400">
					<div>brew install stgime/tap/eurobase</div>
					<div class="mt-1 text-gray-500"># or download a binary: github.com/STGime/homebrew-tap/releases</div>
					<div class="mt-1 text-gray-500"># macOS manual download only: xattr -d com.apple.quarantine ./eurobase (not notarized; brew installs unaffected)</div>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Getting started</h3>
				<div class="rounded-lg bg-gray-900 px-4 py-3 font-mono text-xs text-green-400 space-y-0.5">
					<div class="text-gray-500"># Log in to your account</div>
					<div>eurobase login</div>
					<div class="mt-2 text-gray-500"># List your projects</div>
					<div>eurobase projects list</div>
					<div class="mt-2 text-gray-500"># Set the active project</div>
					<div>eurobase switch my-project</div>
					<div class="mt-2 text-gray-500"># See project status and usage</div>
					<div>eurobase status</div>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Command reference</h3>
				<div class="rounded-lg border border-gray-200 overflow-hidden">
					<table class="w-full text-xs">
						<thead class="bg-gray-50">
							<tr>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Command</th>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Description</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-gray-100">
							<tr><td colspan="2" class="px-3 py-1.5 bg-gray-50 text-[10px] font-semibold text-gray-500 uppercase">Auth & Projects</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">login</td><td class="px-3 py-1 text-gray-500">Sign in with email and password</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">logout</td><td class="px-3 py-1 text-gray-500">Clear stored credentials</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">version</td><td class="px-3 py-1 text-gray-500">Print the CLI version</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">projects list</td><td class="px-3 py-1 text-gray-500">List all projects</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">projects create &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Create a new project</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">projects delete &lt;id&gt;</td><td class="px-3 py-1 text-gray-500">Delete a project (requires --confirm)</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">switch &lt;slug&gt;</td><td class="px-3 py-1 text-gray-500">Set active project</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">status</td><td class="px-3 py-1 text-gray-500">Show usage and plan info</td></tr>

							<tr><td colspan="2" class="px-3 py-1.5 bg-gray-50 text-[10px] font-semibold text-gray-500 uppercase">Database</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">db tables</td><td class="px-3 py-1 text-gray-500">List tables (excludes system tables)</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">db schema [table]</td><td class="px-3 py-1 text-gray-500">Show columns and types</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">db query "SQL"</td><td class="px-3 py-1 text-gray-500">Execute SQL and print results</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">db create-table &lt;name&gt; &lt;col:type&gt;...</td><td class="px-3 py-1 text-gray-500">Create a table with RLS preset</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">db add-column &lt;table&gt; &lt;col:type&gt;</td><td class="px-3 py-1 text-gray-500">Add a column</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">db drop-column &lt;table&gt; &lt;column&gt;</td><td class="px-3 py-1 text-gray-500">Drop a column</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">db drop-table &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Drop a table</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">db dump</td><td class="px-3 py-1 text-gray-500">Export schema as text</td></tr>

							<tr><td colspan="2" class="px-3 py-1.5 bg-gray-50 text-[10px] font-semibold text-gray-500 uppercase">Schema Migrations</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">migrations new &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Create the next numbered migration file in ./migrations</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">migrations up</td><td class="px-3 py-1 text-gray-500">Apply pending migrations to the active project (idempotent)</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">migrations status</td><td class="px-3 py-1 text-gray-500">Local files vs applied versions</td></tr>

							<tr><td colspan="2" class="px-3 py-1.5 bg-gray-50 text-[10px] font-semibold text-gray-500 uppercase">Keys & Config</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">keys show</td><td class="px-3 py-1 text-gray-500">Display API keys</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">keys regenerate</td><td class="px-3 py-1 text-gray-500">Rotate API keys</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">init</td><td class="px-3 py-1 text-gray-500">Generate .env, CLAUDE.md, .cursorrules</td></tr>

							<tr><td colspan="2" class="px-3 py-1.5 bg-gray-50 text-[10px] font-semibold text-gray-500 uppercase">Logs</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">logs</td><td class="px-3 py-1 text-gray-500">Show recent request logs</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">logs --tail</td><td class="px-3 py-1 text-gray-500">Stream logs in real time</td></tr>

							<tr><td colspan="2" class="px-3 py-1.5 bg-gray-50 text-[10px] font-semibold text-gray-500 uppercase">Vault</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">vault list</td><td class="px-3 py-1 text-gray-500">List secret names</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">vault get &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Get decrypted value</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">vault set &lt;name&gt; &lt;value&gt;</td><td class="px-3 py-1 text-gray-500">Store a secret</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">vault delete &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Delete a secret</td></tr>

							<tr><td colspan="2" class="px-3 py-1.5 bg-gray-50 text-[10px] font-semibold text-gray-500 uppercase">Edge Functions (serverless TypeScript/JavaScript)</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">edge-functions list</td><td class="px-3 py-1 text-gray-500">List deployed edge functions</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">edge-functions deploy &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Deploy from functions/&lt;name&gt;.ts (or --file); --no-verify-jwt for public functions</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">edge-functions get &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Show details and source code</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">edge-functions invoke &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Invoke with optional --data JSON body</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">edge-functions logs &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">View invocation logs</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">edge-functions delete &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Delete an edge function</td></tr>

							<tr><td colspan="2" class="px-3 py-1.5 bg-gray-50 text-[10px] font-semibold text-gray-500 uppercase">Cron & RPC Functions</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">cron list</td><td class="px-3 py-1 text-gray-500">List scheduled jobs</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">cron logs &lt;id&gt;</td><td class="px-3 py-1 text-gray-500">Show run history</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">functions list</td><td class="px-3 py-1 text-gray-500">List RPC functions (Postgres)</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">functions create &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Create from file</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">functions delete &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Drop function</td></tr>

							<tr><td colspan="2" class="px-3 py-1.5 bg-gray-50 text-[10px] font-semibold text-gray-500 uppercase">Storage</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">storage ls</td><td class="px-3 py-1 text-gray-500">List files</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">storage upload &lt;local&gt; &lt;key&gt;</td><td class="px-3 py-1 text-gray-500">Upload a file</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">storage download &lt;key&gt; &lt;local&gt;</td><td class="px-3 py-1 text-gray-500">Download a file</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">storage delete &lt;key&gt;</td><td class="px-3 py-1 text-gray-500">Delete a file</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">storage url &lt;key&gt;</td><td class="px-3 py-1 text-gray-500">Generate signed URL</td></tr>

							<tr><td colspan="2" class="px-3 py-1.5 bg-gray-50 text-[10px] font-semibold text-gray-500 uppercase">Testing & Compliance</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">test [file-or-dir]</td><td class="px-3 py-1 text-gray-500">Run pgTAP database tests</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">compliance report</td><td class="px-3 py-1 text-gray-500">Generate the DPA / sub-processor report</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">compliance sub-processors</td><td class="px-3 py-1 text-gray-500">List active sub-processors</td></tr>
						</tbody>
					</table>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Testing RLS policies with pgTAP</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Create SQL test files in a <code class="bg-gray-100 rounded px-1">tests/</code> directory. Each file uses pgTAP assertions to verify your RLS policies work correctly.
				</p>

				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto mt-2">
					<pre>-- tests/rls_tasks.sql
BEGIN;
SELECT plan(3);

-- Test as Alice
SET LOCAL app.end_user_id = 'alice-uuid';

SELECT ok(
    (SELECT count(*) FROM tasks WHERE user_id = 'alice-uuid') > 0,
    'Alice can see her own tasks'
);

SELECT ok(
    (SELECT count(*) FROM tasks WHERE user_id = 'bob-uuid') = 0,
    'Alice cannot see Bob tasks'
);

-- Test as anonymous
SET LOCAL app.end_user_id = '';
SELECT ok(
    (SELECT count(*) FROM tasks) = 0,
    'Anonymous cannot see any tasks'
);

SELECT * FROM finish();
ROLLBACK;</pre>
				</div>

				<div class="rounded-lg bg-gray-900 px-4 py-3 font-mono text-xs text-green-400 mt-3">
					<div class="text-gray-500"># Run all tests</div>
					<div>eurobase test</div>
					<div class="mt-1 text-gray-500"># Run a specific test file</div>
					<div>eurobase test tests/rls_tasks.sql</div>
				</div>

				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3 mt-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<div class="text-sm text-eurobase-800">
						<p><strong>Tip:</strong> Tests run inside a transaction that is rolled back &mdash; no data is modified. Use <code class="bg-white/50 rounded px-1">SET LOCAL app.end_user_id</code> to simulate different users and verify RLS policies enforce correct access.</p>
					</div>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('connect')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Connecting Your IDE &rarr;
				</button>
			</div>
		</section>
