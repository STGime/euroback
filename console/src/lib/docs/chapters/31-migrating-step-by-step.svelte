<section id="migrating-step-by-step" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">31. Migrating from Supabase, step by step</h2>
			<p class="text-sm italic text-gray-500 mb-4">Move a Supabase project — schema, users, data, files, edge functions, scheduled jobs and the app's code — with the CLI, or let Claude do it with the migration skill.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">This chapter walks through a whole migration: schema, users and data, files, edge functions, scheduled jobs, the app's code, and the switch-over. Each step reads your Supabase project and writes only to the Eurobase project that is active in the CLI; Supabase keeps running until you switch. Do it into a test project first. The answers to common questions are in <a href="/docs/migrating-from-supabase" class="text-eurobase-700 hover:underline">Migrating from Supabase</a>.</p>
				<p class="text-sm text-amber-800 bg-amber-50 border-l-4 border-amber-400 px-3 py-2 rounded leading-relaxed">Prefer to have an agent do it? The Claude skill in the next section drives these same steps, stops for every decision that is yours, and changes your app's code at the end.</p>
				<h3 id="with-claude" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">Use Claude Code</h3>
				<p class="text-sm text-gray-700 leading-relaxed">Install the skill in your app's repository, then ask Claude to migrate the project. It runs the CLI, reports what each step found, waits for your decisions, and checks the results.</p>
				<div class="rounded-lg bg-gray-900 px-4 py-3 font-mono text-xs text-green-400 space-y-0.5 overflow-x-auto">
					<div>mkdir -p .claude/skills/eurobase-supabase-migration</div>
					<div>curl -fsSL https://raw.githubusercontent.com/STGime/euroback/main/integrations/supabase-migration/SKILL.md \</div>
					<div>  -o .claude/skills/eurobase-supabase-migration/SKILL.md</div>
				</div>
				<p class="text-sm text-gray-700 leading-relaxed">Keep secrets out of the chat: set the Supabase variables below in your own terminal; the agent refers to them by name.</p>
				<h3 id="prepare" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">0. Prepare</h3>
				<ul class="list-disc pl-5 text-sm text-gray-700 leading-relaxed space-y-1">
					<li>The Eurobase CLI 0.6.9 or later (see <a href="/docs/cli" class="text-eurobase-700 hover:underline">CLI</a>), signed in: <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase login</code>.</li>
					<li><code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">pg_dump</code> with the same major version as your Supabase database or newer (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">brew install libpq</code> on macOS).</li>
					<li>In your terminal: <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">SUPABASE_DB_URL</code> from Supabase → Connect → Session pooler (the direct <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">db.&lt;ref&gt;.supabase.co</code> host is IPv6-only). For files also <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">SUPABASE_URL</code> (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">https://&lt;ref&gt;.supabase.co</code>) and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">SUPABASE_SERVICE_ROLE_KEY</code>.</li>
					<li>Your Supabase project folder, with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">supabase/functions/</code> and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">supabase/config.toml</code> if you use edge functions (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">supabase functions download &lt;name&gt;</code> fetches one that isn't in git).</li>
					<li>A fresh target project. Every command acts on the active project, marked <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">*</code> in the list.</li>
					<li>A folder of its own for the migration (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">mkdir migration &amp;&amp; cd migration</code>): <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase migrations up</code> applies <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">./migrations</code> of the current folder, and your app may have a <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">migrations/</code> folder already. The commands below run from there and reach your Supabase folder with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">../</code>. Keep it out of git — from step 3 on it holds user data.</li>
				</ul>
				<div class="rounded-lg bg-gray-900 px-4 py-3 font-mono text-xs text-green-400 space-y-0.5 overflow-x-auto">
					<div>export SUPABASE_DB_URL='postgresql://postgres.&lt;ref&gt;:…@aws-0-&lt;region&gt;.pooler.supabase.com:5432/postgres'</div>
					<div>eurobase projects create my-app-test</div>
					<div>eurobase switch my-app-test</div>
					<div>eurobase projects list  <span class="text-gray-500"># * marks the active project</span></div>
				</div>
				<h3 id="assess" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">1. Assess</h3>
				<div class="rounded-lg bg-gray-900 px-4 py-3 font-mono text-xs text-green-400 space-y-0.5 overflow-x-auto">
					<div>eurobase import supabase assess --functions-dir ../supabase/functions</div>
				</div>
				<p class="text-sm text-gray-700 leading-relaxed">Writes a report of everything the migration will meet, graded ✅ / ⚠️ / ❌: tables, policies, database functions and triggers, extensions, users, buckets and edge functions. Read the ⚠️ and ❌ items first. The common ones:</p>
				<ul class="list-disc pl-5 text-sm text-gray-700 leading-relaxed space-y-1">
					<li><code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">SECURITY DEFINER</code> functions become normal functions that run with the caller's rights. Whatever relied on the owner's rights moves to server code: an edge function with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">ctx.db.asService()</code>, or your backend with the secret key.</li>
					<li>A trigger on <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth.users</code> (creating a profile at sign-up) has no equivalent; that work moves into the sign-up code or an edge function.</li>
					<li>GRANT / REVOKE restrictions (a column hidden from <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">anon</code>) are not carried over, and a view doesn't hide columns. Restricted columns move to their own table with stricter policies, or behind an edge function.</li>
				</ul>
				<h3 id="schema" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">2. Schema</h3>
				<div class="rounded-lg bg-gray-900 px-4 py-3 font-mono text-xs text-green-400 space-y-0.5 overflow-x-auto">
					<div>eurobase import supabase schema --migrations-dir ./migrations</div>
					<div>eurobase migrations up</div>
				</div>
				<p class="text-sm text-gray-700 leading-relaxed">Translates your tables, policies, functions, triggers and views, and writes one migration. If access rules can't be carried over as they are, it lists them and writes nothing until you accept them with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">--accept-access-changes</code>; the accepted list stays at the top of the file. Every table with RLS also gets a <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase_service_role</code> policy, so your secret key reads and writes it as Supabase's service role did; app users keep what their policies give them.</p>
				<p class="text-sm text-amber-800 bg-amber-50 border-l-4 border-amber-400 px-3 py-2 rounded leading-relaxed">New projects start with a sample <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">todos</code> table. If your Supabase project has its own <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">todos</code>, drop the sample table before <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">migrations up</code>.</p>
				<h3 id="data" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">3. Users and data</h3>
				<div class="rounded-lg bg-gray-900 px-4 py-3 font-mono text-xs text-green-400 space-y-0.5 overflow-x-auto">
					<div>eurobase import supabase data --migrations-dir ./migrations</div>
					<div>eurobase migrations up</div>
				</div>
				<p class="text-sm text-gray-700 leading-relaxed">Copies the users (ids, emails, password hashes, OAuth links), then every table's rows, parents before children. The files hold real users' data — apply them only into a project you mean to keep it in. Sessions are not copied: each user signs in once on Eurobase. The list under "Read before applying" says what was changed or left out. For a first test, leave large tables out with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">--exclude-table logs,events</code>.</p>
				<p class="text-sm text-gray-700 leading-relaxed">Check: compare <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">select count(*)</code> per table on both sides. Apply into a project that has no data yet; running it twice writes the rows twice.</p>
				<h3 id="storage" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">4. Files</h3>
				<div class="rounded-lg bg-gray-900 px-4 py-3 font-mono text-xs text-green-400 space-y-0.5 overflow-x-auto">
					<div>eurobase import supabase storage  <span class="text-gray-500"># report: buckets, files, sizes</span></div>
					<div>export SUPABASE_URL=https://&lt;ref&gt;.supabase.co</div>
					<div>export SUPABASE_SERVICE_ROLE_KEY=…</div>
					<div>eurobase import supabase storage --apply  <span class="text-gray-500"># copy</span></div>
				</div>
				<p class="text-sm text-gray-700 leading-relaxed">Each file is streamed from Supabase straight to your project, a few at a time, so gigabytes are fine. A bucket becomes a folder, a file keeps its owner, and a public bucket becomes a folder shared with everyone. Stop it whenever you like; running it again copies only what is missing and never replaces a file your project already has.</p>
				<p class="text-sm text-gray-700 leading-relaxed">Check: a second <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">--apply</code> reports every file as already there.</p>
				<h3 id="functions" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">5. Edge functions</h3>
				<div class="rounded-lg bg-gray-900 px-4 py-3 font-mono text-xs text-green-400 space-y-0.5 overflow-x-auto">
					<div>eurobase import supabase functions --input ../supabase/functions</div>
					<div> eurobase vault set STRIPE_SECRET_KEY '…'  <span class="text-gray-500"># each secret the report lists; or console → Vault</span></div>
					<div>eurobase import supabase functions --input ../supabase/functions --deploy</div>
				</div>
				<p class="text-sm text-gray-700 leading-relaxed">Your functions run as written: <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">Deno.serve</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">Deno.env.get</code> (function settings, then the Vault), the <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">Authorization</code> header and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">createClient()</code> queries are supplied by a compatibility layer, and imports are bundled in. The first command builds every function and changes nothing; it lists, by file and line, the few calls that need a change. <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">--deploy</code> deploys the ready ones, with the service-role setting where the code uses the service role key.</p>
				<p class="text-sm text-gray-700 leading-relaxed">“Ready” means it builds. Check that the tables each function uses exist and that something still calls it — deploy only what your app uses. Free projects allow 50 functions, Pro 500.</p>
				<p class="text-sm text-gray-700 leading-relaxed">Check: call a function signed in and once without; <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase edge-functions logs &lt;name&gt;</code> shows its log, including <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">console.log</code> lines.</p>
				<h3 id="cron" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">6. Scheduled jobs</h3>
				<div class="rounded-lg bg-gray-900 px-4 py-3 font-mono text-xs text-green-400 space-y-0.5 overflow-x-auto">
					<div>eurobase import supabase cron  <span class="text-gray-500"># plan</span></div>
					<div>eurobase import supabase cron --apply  <span class="text-gray-500"># create, switched off</span></div>
				</div>
				<p class="text-sm text-gray-700 leading-relaxed">Your pg_cron jobs become cron jobs at the same times: a SQL command stays a SQL job, a call to one of your functions becomes a job that runs it. They are created switched off, because the same jobs still run on Supabase.</p>
				<h3 id="app" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">7. Your app's code</h3>
				<p class="text-sm text-gray-700 leading-relaxed">Replace <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">@supabase/supabase-js</code> with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">@eurobase/sdk</code>. The calls are close; the ones that differ most are below, and the full list is in the <a href="https://github.com/STGime/euroback/blob/main/integrations/lovable/references/sdk-reference.md" class="text-eurobase-700 hover:underline">SDK reference</a>.</p>
				<ul class="list-disc pl-5 text-sm text-gray-700 leading-relaxed space-y-1">
					<li><code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">createClient(&#123; url, apiKey: publicKey, projectId &#125;)</code> instead of <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">createClient(url, anonKey)</code>.</li>
					<li><code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eb.auth.signIn(&#123; email, password &#125;)</code> instead of <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">signInWithPassword</code>; OAuth is <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eb.auth.signInWithOAuth('google', &#123; redirectTo &#125;)</code> with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eb.auth.handleOAuthCallback()</code> on the callback page.</li>
					<li><code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eb.db.from('t').update(id, values)</code> and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">.delete(id)</code> take the row's id; <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">insert</code> takes one row.</li>
					<li><code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">select('id', 'name')</code> takes the columns as separate arguments; a row this row points to can be embedded one level deep, named by its table (no aliases): <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">select('*', 'profiles(*)')</code>.</li>
					<li>No <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">upsert</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">or()</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">not()</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">is()</code> or <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">maybeSingle()</code>: put such queries into a database function and call it with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eb.db.rpc(fn, args)</code>; return a list as one <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">jsonb</code> value (rpc gives only the first row of a set).</li>
					<li><code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eb.auth.getSession()</code> is synchronous, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">getUser()</code> returns the user as <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">data</code>, and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">onAuthStateChange</code> returns the function that unsubscribes.</li>
					<li>Storage has no buckets: <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eb.storage.upload('avatars/' + path, file)</code> — the old bucket is the first folder.</li>
					<li>Errors are strings: show <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">error</code>, not <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">error.message</code> (except <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">functions.invoke</code>).</li>
					<li>Realtime doesn't apply custom RLS policies: signed-in users get only their own rows on tables with an owner column (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">user_id</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">owner_id</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">created_by</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">uploaded_by</code>). Check what the app subscribes to.</li>
				</ul>
				<p class="text-sm text-gray-700 leading-relaxed">The Claude skill (section above) carries the full table of supabase-js calls and their Eurobase equivalents.</p>
				<p class="text-sm text-gray-700 leading-relaxed">A backend that uses supabase-js on the server (service role client, a client with a user's token, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth.getUser(jwt)</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth.admin</code>) has its own counterparts: see <a href="/docs/server-side" class="text-eurobase-700 hover:underline">Using the SDK on a server</a> and <a href="/docs/user-admin" class="text-eurobase-700 hover:underline">Managing users from your server</a>. A direct database connection (pg-boss, Prisma, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">pg</code>) needs a Team project, whose dedicated database comes with an owner URL; on Free and Pro, data goes through the SDK and REST.</p>
				<p class="text-sm text-gray-700 leading-relaxed">Set each OAuth provider up in Auth with the same OAuth app (client ID) as on Supabase, so imported users are recognised, and add your app's domains to the allowed CORS origins and redirect URLs. Only the public key goes into the app.</p>
				<h3 id="switch" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">8. Switch over</h3>
				<p class="text-sm text-gray-700 leading-relaxed">The production project is a new, empty project; keep the test project for tests. Prepare it while Supabase keeps running:</p>
				<ul class="list-disc pl-5 text-sm text-gray-700 leading-relaxed space-y-1">
					<li>Create the production project and switch to it. In a new folder, put the schema migration you tested into <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">migrations/</code> (not the test's data files) and run <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase migrations up</code>. The data import writes new files next to old ones, and applying old and new data together writes the rows twice.</li>
					<li>Copy the files, set the secrets, deploy the functions, import the cron jobs, and set up auth providers, CORS origins and redirect URLs — steps 4 to 7 against the production project.</li>
				</ul>
				<p class="text-sm text-gray-700 leading-relaxed">Then switch:</p>
				<ul class="list-disc pl-5 text-sm text-gray-700 leading-relaxed space-y-1">
					<li>Stop writes on Supabase (a maintenance window).</li>
					<li>In the new folder, run <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">data</code> and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">migrations up</code>, and compare row counts with Supabase.</li>
					<li>Run <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">storage --apply</code> once more; it copies only new files.</li>
					<li>Switch the cron jobs on (console → Cron) and off on Supabase.</li>
					<li>Deploy the app with the Eurobase keys. Keep the Supabase project as it is for a while, as the way back.</li>
				</ul>
				<p class="text-sm text-gray-700 leading-relaxed">When you're done with a test project that holds real users, delete it: <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase projects delete &lt;id&gt; --confirm</code>.</p>
			</div>
		</section>
