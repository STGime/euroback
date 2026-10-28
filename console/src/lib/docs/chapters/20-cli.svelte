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

				<p class="text-sm text-gray-700 leading-relaxed">
					Without Homebrew (CI, agents, Linux servers), download the binary for your platform (macOS or Linux, amd64 or arm64):
				</p>
				<div class="rounded-lg bg-gray-900 px-4 py-3 font-mono text-xs text-green-400 space-y-0.5">
					<div>VER=0.5.0  <span class="text-gray-500"># latest: github.com/STGime/homebrew-tap/releases</span></div>
					<div>OS=$(uname -s | tr '[:upper:]' '[:lower:]')</div>
					<div>ARCH=$(uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')</div>
					<div>BASE=https://github.com/STGime/homebrew-tap/releases/download/cli-v$&#123;VER&#125;</div>
					<div>TGZ=eurobase_$&#123;VER&#125;_$&#123;OS&#125;_$&#123;ARCH&#125;.tar.gz</div>
					<div>curl -fsSLO "$BASE/$TGZ" &amp;&amp; curl -fsSLO "$BASE/eurobase_$&#123;VER&#125;_checksums.txt"</div>
					<div>grep " $TGZ\$" eurobase_$&#123;VER&#125;_checksums.txt | shasum -a 256 -c -</div>
					<div>tar xzf "$TGZ" &amp;&amp; sudo mv eurobase /usr/local/bin/  <span class="text-gray-500"># or anywhere on your PATH</span></div>
					<div>eurobase version</div>
				</div>

				<div class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-amber-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
					</svg>
					<p class="text-sm text-amber-800">
						<strong>The Eurobase CLI is not on npm.</strong> The npm package named <code class="rounded bg-amber-100 px-1.5 py-0.5 text-xs font-mono">eurobase</code> is an unrelated product from another publisher. It describes itself as the official Eurobase CLI, but it isn't ours and doesn't work with your Eurobase projects. Don't install it or run <code class="rounded bg-amber-100 px-1.5 py-0.5 text-xs font-mono">npx eurobase</code>, and never give it a Eurobase token. Install only from Homebrew (<code class="rounded bg-amber-100 px-1.5 py-0.5 text-xs font-mono">stgime/tap/eurobase</code>) or the releases at <code class="rounded bg-amber-100 px-1.5 py-0.5 text-xs font-mono">github.com/STGime/homebrew-tap</code>. If you set up a coding agent, tell it the same. (Our only npm package is the app SDK, <code class="rounded bg-amber-100 px-1.5 py-0.5 text-xs font-mono">@eurobase/sdk</code>.)
					</p>
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

				<p class="text-sm text-gray-700 leading-relaxed">
					In CI or scripts, log in with a <a href="/docs/account" class="text-eurobase-600 hover:underline cursor-pointer">personal access token</a> instead: <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono">eurobase login --token "$EUROBASE_PAT"</code>. A project token selects its project automatically and can run the project commands with the access you gave it. A project token can't create or delete projects: do that in the console (or after <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono">eurobase login</code>, if your account signs in with a password only, without a passkey or SSO).
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Where the CLI keeps your login, and working with several projects</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Every command reads one file, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono">$HOME/.eurobase/config.json</code> (mode 600): your token, the active project and the API URL. <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono">eurobase login</code> and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono">eurobase projects switch</code> write it; nothing is kept in the macOS Keychain or any other credential store, and the CLI finds the file only through <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono">HOME</code>. There&rsquo;s no profile option or <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono">--project</code> flag yet, so to keep a test project fully apart from production, give each its own <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono">HOME</code> and a token scoped to that project:
				</p>
				<div class="mt-2 rounded-lg bg-gray-900 p-4 font-mono text-sm text-gray-100 overflow-x-auto">
					<div>export HOME="$(mktemp -d)"</div>
					<div>eurobase login --token "$TEST_PROJECT_TOKEN"   <span class="text-gray-500"># selects the token's project</span></div>
					<div>eurobase status                               <span class="text-gray-500"># confirm the project</span></div>
					<div>eurobase migrations up</div>
				</div>
				<p class="mt-2 text-sm text-gray-700 leading-relaxed">
					A project token is refused for every other project, so even a mixed-up configuration can&rsquo;t reach production. <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono">eurobase migrations up</code> reads <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono">./migrations/*.sql</code> from the current directory.
				</p>

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
							<tr><td class="px-3 py-1 font-mono text-gray-700">functions list</td><td class="px-3 py-1 text-gray-500">List RPC functions (Postgres) with their arguments</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">functions get &lt;name&gt; [--args "…"]</td><td class="px-3 py-1 text-gray-500">Print the function's CREATE OR REPLACE statement (next CLI release)</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">functions create &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Create from file</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">functions delete &lt;name&gt;</td><td class="px-3 py-1 text-gray-500">Drop function</td></tr>

							<tr><td colspan="2" class="px-3 py-1.5 bg-gray-50 text-[10px] font-semibold text-gray-500 uppercase">Storage</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">storage ls</td><td class="px-3 py-1 text-gray-500">List files</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">storage upload &lt;local&gt; &lt;key&gt;</td><td class="px-3 py-1 text-gray-500">Upload a file</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">storage download &lt;key&gt; &lt;local&gt;</td><td class="px-3 py-1 text-gray-500">Download a file</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">storage delete &lt;key&gt;</td><td class="px-3 py-1 text-gray-500">Delete a file</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">storage url &lt;key&gt;</td><td class="px-3 py-1 text-gray-500">Generate signed URL</td></tr>

							<tr><td colspan="2" class="px-3 py-1.5 bg-gray-50 text-[10px] font-semibold text-gray-500 uppercase">Compliance</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">compliance report</td><td class="px-3 py-1 text-gray-500">Generate the DPA / sub-processor report</td></tr>
							<tr><td class="px-3 py-1 font-mono text-gray-700">compliance sub-processors</td><td class="px-3 py-1 text-gray-500">List active sub-processors</td></tr>
						</tbody>
					</table>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Testing RLS policies</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Test your policies the way your users actually reach your data: as an authenticated client. Create a few test end-users in a separate project, sign each one in, and exercise the SDK (or REST API) with that user's access token. Row-Level Security is evaluated under the same non-owner role as in production, so what a test user can read or write is exactly what a real client would get.
				</p>

				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto mt-2">
					<pre>import {'{'} createClient {'}'} from '@eurobase/sdk'

const eb = createClient({'{'} url: PROJECT_URL, apiKey: PUBLIC_KEY {'}'})

// Sign in as a seeded test user, then read/write under their session.
await eb.auth.signIn({'{'} email: 'alice@example.test', password: '...' {'}'})

const {'{'} data: mine {'}'} = await eb.db.from('tasks').select('*')        // only Alice's rows
const {'{'} error {'}'} = await eb.db.from('tasks').update(BOB_TASK_ID, {'{'} title: 'x' {'}'})  // RLS blocks updating Bob's row

// Sign out -> anonymous: a protected table returns nothing.
await eb.auth.signOut()
const {'{'} data: anon {'}'} = await eb.db.from('tasks').select('*')        // []</pre>
				</div>

				<p class="text-sm text-gray-700 leading-relaxed mt-3">
					Each session carries its identity automatically: the platform sets <code class="bg-gray-100 rounded px-1">app.end_user_id</code> and <code class="bg-gray-100 rounded px-1">app.end_user_role</code> for the request, which your policies read through <code class="bg-gray-100 rounded px-1">auth_uid()</code>, <code class="bg-gray-100 rounded px-1">auth_role()</code> and <code class="bg-gray-100 rounded px-1">is_service_role()</code>. Confirm what a session resolves to with <code class="bg-gray-100 rounded px-1">GET /v1/auth/user</code> using that user's token. The <a href="/docs/rls" class="text-eurobase-600 hover:text-eurobase-700">Row-Level Security</a> chapter has the policy patterns and a "Test it from the SDK" walkthrough.
				</p>

				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3 mt-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<div class="text-sm text-eurobase-800">
						<p><strong>Why not test from inside the database (e.g. pgTAP)?</strong> Every statement you run on Eurobase goes through a non-owner, RLS-enforced role, one statement at a time, with no transaction control &mdash; the same constraints that make your RLS guarantees trustworthy. Testing policies from inside the database would mean relaxing exactly that boundary. Our EU-managed Postgres also ships a vetted, security-reviewed set of extensions, and in-database test frameworks such as pgTAP aren't part of it. Testing as an authenticated client covers the whole path &mdash; sign-in &rarr; identity &rarr; policy &rarr; response &mdash; which is what your users actually exercise.</p>
					</div>
				</div>
			</div>

			<div class="mt-6 text-right">
				<a href="/docs/connect" class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Connecting Your IDE &rarr;
				</a>
			</div>
		</section>
