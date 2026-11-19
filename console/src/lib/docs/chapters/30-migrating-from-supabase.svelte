		<section id="migrating-from-supabase" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">30. Migrating from Supabase (FAQ)</h2>
			<p class="text-sm italic text-gray-500 mb-4">Common questions from teams moving an existing Supabase project to Eurobase — what ports cleanly, what needs a rewrite, and where we're honest about the differences.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					Eurobase is close to Supabase in shape — Postgres, Row-Level Security, an SDK with a familiar API, EU storage with signed URLs — but it is not a drop-in replacement. A migration CLI (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase import supabase</code>) automates most of a move, starting with an <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">assess</code> step that reports what transfers cleanly and what needs changes. The next step, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase import supabase schema</code>, writes your tables, policies, functions and views as a migration file you review and apply. Both only read from Supabase and need CLI 0.6.3 or later. If you'd like us to go through the report with you, <a href="mailto:contact@eurobase.app" class="cursor-pointer text-eurobase-700 hover:underline">get in touch</a>. The answers below are deliberately straight about the gaps.
				</p>

				<p class="text-sm text-gray-700 leading-relaxed">
					For the whole migration in order, with every command, see <a href="/docs/migrating-step-by-step" class="text-eurobase-700 hover:underline">chapter 31, Migrating from Supabase, step by step</a>; it also has a Claude skill that does it for you.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-2">Migrating in</h3>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Can I import my existing users without password resets?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Yes. We use bcrypt in the same format as Supabase, so password hashes import as-is and your users keep their passwords. The importer keeps user ids (so RLS keeps working), email-confirmed status, metadata, bans that are still running, and OAuth links. The one exception: a hash with a bcrypt cost above 12 is not imported, and that user sets a new password with &ldquo;forgot password&rdquo;. Without the CLI, the users API's batch import takes the same bcrypt hashes and ids (see below).</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">How do my users and data come over?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">With <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase import supabase data</code> (CLI 0.6.5 or later), after the schema migration has been applied, into a project that has no data yet. It reads your Supabase database in one read-only snapshot and writes migration files; <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase migrations up</code> applies them. Users come first, then each table's rows, parents before children. Values are copied as they are, sequences continue after the imported ids, and your triggers are off while rows are inserted, so audit rows and timestamps aren't written a second time. To try it on part of your data first, leave out large tables with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">--exclude-table logs,events</code> (CLI 0.6.8 or later); a table whose rows refer to a left-out table has to be left out too.</p>
					<p class="mt-2 text-sm text-gray-700 leading-relaxed">The command lists what it changed or left out. Read that list before you apply:</p>
					<ul class="mt-1 list-disc pl-5 text-sm text-gray-700 leading-relaxed space-y-1">
						<li>Users deleted on Supabase and SSO users are not imported. Anonymous users are, so their rows stay valid, but they can't sign in again.</li>
						<li>OAuth links come over for Google, GitHub, Apple, Microsoft and LinkedIn. Set each provider up in the project with the same OAuth app (client ID) as on Supabase, and try one sign-in per provider before you switch your app over. Links for other providers are left out; those users sign in with a magic link or a password reset.</li>
						<li>Emails are stored in lower case, and phone numbers with a leading <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">+</code>.</li>
						<li>Sessions and refresh tokens are not copied: every user signs in once on Eurobase.</li>
						<li>A table whose name a Eurobase project already uses (such as <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">users</code> or <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">storage_objects</code>) is skipped. Rename it in Supabase and in your app first.</li>
						<li>Every new project starts with a sample <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">todos</code> table. If your Supabase project has a table with that name, drop the sample table in the new project before you apply the schema migration.</li>
						<li>Foreign keys that form a cycle between two or more tables stop the import. Drop one of them in the project, import those tables with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">--table</code>, and add it back.</li>
					</ul>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Do I have to rewrite my edge functions?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Mostly not. <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase edge-functions deploy</code> (CLI 0.6.6 or later) builds a function written for Supabase with a compatibility layer, so the code runs as it is: <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">Deno.serve(handler)</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">Deno.env.get(…)</code>, the <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">Authorization</code> header and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">createClient(url, key)</code> with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth.getUser</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">from().select / insert / update / upsert / delete</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">rpc()</code> and storage uploads and signed URLs. Imports (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">https://</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">npm:</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">jsr:</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">../_shared</code> files) are bundled into the function. Underneath, the queries run as SQL through <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">ctx.db</code>: there is no second API call.</p>
					<p class="mt-2 text-sm text-gray-700 leading-relaxed">Run <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase import supabase functions</code> in your Supabase project folder first. It builds every function, changes nothing, and tells you which are ready. Then <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">--deploy</code> deploys those.</p>
					<ul class="mt-1 list-disc pl-5 text-sm text-gray-700 leading-relaxed space-y-1">
						<li><strong>Secrets:</strong> <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">Deno.env.get(&quot;NAME&quot;)</code> reads the function's env vars, then your project's Vault. The command lists the names your functions read; set each with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase vault set NAME VALUE</code>.</li>
						<li><strong>Service role key:</strong> a client made with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">SUPABASE_SERVICE_ROLE_KEY</code> runs its queries with the service role, and the function is deployed with the allow-service-role setting. A client made with the anon key runs as the caller, under your RLS policies.</li>
						<li><strong>Calls without a signed-in user:</strong> on Supabase an anon-key client then acts as the <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">anon</code> role. A Eurobase function's SQL has no anon role (without a user it is the service role), so an anon-key client refuses its queries when nobody is signed in. For what such a function does on its own behalf, use the service role key client.</li>
						<li><strong>Trusted callers:</strong> a function that recognises a cron job or another server by comparing a header with the service role key needs a secret of your own: set <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">SUPABASE_SERVICE_ROLE_KEY</code> (or any name you compare with) in the Vault and send it in a custom header. The <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">Authorization</code> and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">apikey</code> headers of a call aren't passed to the function, and without a value of your own the key is a new random value in every request.</li>
						<li><strong>Who is calling:</strong> the platform verifies the caller's token before your function runs. <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth.getUser(token)</code> returns that user; the token you pass is not looked at.</li>
						<li><strong>What needs a change:</strong> requests to the Supabase API by URL (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">/rest/v1</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">/functions/v1</code>), calling another function with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">functions.invoke()</code>, realtime channels, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">select()</code> with an embedded table, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">or()</code> filters, most <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth.admin</code> calls and a direct database connection. The command lists each by file and line with what to use instead; an unsupported call that slips through throws with its name when the function runs.</li>
						<li><strong>Size and libraries:</strong> a function with its imports can be up to 1 MB, and libraries that need Node.js built-ins can't be used.</li>
					</ul>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">How do my files (storage buckets) come over?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed"><code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase import supabase storage</code> (CLI 0.6.8 or later) shows your buckets, file counts and sizes; with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">--apply</code> it copies every file into your project. Each file is streamed from Supabase straight to your project's bucket, a few at a time, so gigabytes are fine and nothing is stored on your machine. Stop it whenever you like: run it again and files already copied are skipped. It reads the files with your Supabase project's service role key (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">SUPABASE_URL</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">SUPABASE_SERVICE_ROLE_KEY</code>).</p>
					<ul class="mt-1 list-disc pl-5 text-sm text-gray-700 leading-relaxed space-y-1">
						<li><strong>A bucket becomes a folder:</strong> <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">avatars/u1/me.png</code> stays <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">avatars/u1/me.png</code>. A function deployed with the Supabase compatibility layer reads <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">storage.from(&quot;avatars&quot;)</code> from the same folder.</li>
						<li><strong>Files keep their owners:</strong> the end user who uploaded a file keeps the access they had, and nobody else gets it. Import your users first (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">import supabase data</code>). A file whose owner isn't a user of the project becomes a developer file (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">--missing-owner skip</code> leaves it out).</li>
						<li><strong>Public buckets</strong> become a folder shared with everyone, and their files become developer files: on Supabase anyone could read them. Their uploaders can no longer replace or delete them from your app. If your project already has files in that folder, the copy stops instead of making them public.</li>
						<li><strong>Nothing is overwritten:</strong> a file your project already has under the same name, or one changed since it was copied, is left as it is and listed. A file deleted in your project after the copy comes back if you run it again: delete it on Supabase too.</li>
						<li><strong>Storage policies</strong> are not carried over. Check that per-user ownership and shared folders cover what your policies allowed.</li>
						<li><strong>Limits:</strong> one file can be up to 5 GB. The copy is not checked against your plan's storage limit: compare the total the command shows first.</li>
					</ul>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">What about my scheduled jobs (pg_cron)?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed"><code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase import supabase cron</code> (CLI 0.6.7 or later) reads the jobs in your Supabase project and shows how each becomes a Eurobase cron job; <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">--apply</code> creates them. A SQL command becomes a SQL job with the same statement. A command that calls one of your edge functions with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">net.http_post(…/functions/v1/name)</code> becomes a job that runs the function <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">name</code>, with the JSON body as its payload.</p>
					<ul class="mt-1 list-disc pl-5 text-sm text-gray-700 leading-relaxed space-y-1">
						<li><strong>Created switched off.</strong> The same jobs keep running on Supabase until you switch over, and most must not run twice. Switch them on in the console (Cron) when you do.</li>
						<li><strong>Schedules</strong> are rewritten where the syntax differs: <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">1-5</code> becomes <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">1,2,3,4,5</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">MON</code> becomes <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">1</code>. The time zone is your <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">cron.timezone</code> (UTC unless you changed it).</li>
						<li><strong>Same job, same times, or not at all.</strong> A job is only created when it would do what the pg_cron job does, at the same times. Everything else is listed with the reason, so you know what is missing.</li>
						<li><strong>Headers and keys are not carried over.</strong> A cron job runs a function directly, so no request header from your pg_cron command is kept. Their names are listed; set one on the job in the console if your function needs it.</li>
						<li><strong>What needs a change:</strong> a command with several statements, or a function call wrapped in other SQL (put the steps into one database function); SQL that uses Supabase's own schemas (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth.</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">storage.</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">net.</code>) or another schema of yours; maintenance statements such as <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">VACUUM</code> or <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">REFRESH MATERIALIZED VIEW</code>; a call to a URL that isn't one of your functions, or to a function with a query string (make it an edge function, or pass the values in the payload); a schedule in seconds (the shortest interval is a minute); and a schedule that limits both the day of the month and the weekday. A request body built in SQL isn't carried over: the function is called with an empty body.</li>
						<li><strong>Limits:</strong> a SQL job's statement may take up to 30 seconds. If your <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">cron.timezone</code> isn't an IANA name, pass <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">--timezone Europe/Berlin</code>.</li>
					</ul>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Can I keep using <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">supabase-js</code>?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">No — you switch to <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">@eurobase/sdk</code>. It is not wire-compatible, but the API shape is deliberately familiar (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">createClient</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth.signUp</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">.from()</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">.rpc()</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">.storage</code>), so for a typical app it is a contained change.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Can I run a trial alongside my current setup?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Yes. Your account can have up to 10 projects on Pro, so you can stand up a test project next to your live backend and trial the migration before switching anything.</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Database &amp; auth differences</h3>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Is Row-Level Security supported?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Yes — RLS is the core authorization model, with the same helpers you know (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth.uid()</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth.role()</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth.email()</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">is_service_role()</code>). The importer auto-translates Supabase policies and adds an <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">is_service_role()</code> branch to each. It also gives every table with RLS a policy that lets the service role read and write all rows (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase_service_role</code>): Supabase's service role skips RLS entirely, so a table whose policies only let app users read was still writable by your server code. Tables you create later need such a policy too &mdash; no API key skips RLS on its own.</p>
				</div>

				<div class="rounded-xl border border-amber-200 bg-amber-50 p-5">
					<p class="text-sm font-semibold text-gray-900">Are <code class="rounded bg-amber-100 px-1.5 py-0.5 text-xs font-mono text-amber-900">SECURITY DEFINER</code> functions supported?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Not on self-serve surfaces — this is the biggest difference to plan for. Re-express them as plain (invoker) functions plus RLS, as server-side code using the service key, or as edge functions. Plain functions and trigger functions port fine. For the common <code class="rounded bg-gray-100 px-1 text-xs font-mono">is_member_of()</code>-style helper used in team or organisation policies, see <a href="/docs/rls#rls-group-access" class="text-eurobase-600 hover:underline">Group and team access</a>.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">What happens to my access rules when I import the schema?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Policies come over, rewritten for how Eurobase identifies callers. There are no <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">anon</code> / <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">authenticated</code> database roles: every API request runs as the same role, the signed-in user is <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth_uid()</code> (NULL when no one is signed in), and the secret key is <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">public.is_service_role()</code>. So <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">TO authenticated</code> becomes an <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth_uid() IS NOT NULL</code> condition, each policy gets a secret-key branch, and a table with RLS on and no policies gets a secret-key-only policy (on Supabase the service role skipped RLS there).</p>
					<p class="mt-2 text-sm text-gray-700 leading-relaxed">Some rules can't be carried over, and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">import supabase schema</code> won't write the file until you've seen them and passed <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">--accept-access-changes</code>:</p>
					<ul class="mt-2 list-disc pl-5 text-sm text-gray-700 leading-relaxed space-y-1">
						<li><strong>GRANT / REVOKE restrictions</strong> (column-level security, tables or functions closed to <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">anon</code>). Eurobase has no per-role grants, only RLS, and RLS works on rows, not columns: close a whole table with policies, move restricted columns into their own table with stricter policies, or serve them through an edge function. A view doesn't hide columns here, because callers can read the table it selects from.</li>
						<li><strong>Checks on the database role or JWT settings</strong> (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">current_user</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">current_setting('role')</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">request.jwt.*</code>). Rewrite them with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth_uid()</code> or <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">public.is_service_role()</code>.</li>
						<li><strong>Policies, triggers, views or defaults that call a SECURITY DEFINER function.</strong> The function now runs with the caller's rights and only sees the rows the caller may see.</li>
						<li><strong>Policies or triggers that had to be left out</strong>, for example because they read a materialized view.</li>
					</ul>
					<p class="mt-2 text-sm text-gray-700 leading-relaxed">The accepted list is written at the top of the migration file. Work through it before your app goes live on Eurobase; <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">assess</code> lists the GRANT / REVOKE restrictions and the SECURITY DEFINER functions as blockers up front.</p>
				</div>

				<div class="rounded-xl border border-amber-200 bg-amber-50 p-5">
					<p class="text-sm font-semibold text-gray-900">Does the <code class="rounded bg-amber-100 px-1.5 py-0.5 text-xs font-mono text-amber-900">trigger on auth.users</code> (profile-on-signup) pattern work?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">No — the users table is platform-managed, so you can't attach a trigger to it. You can still reference it (<code class="rounded bg-amber-100 px-1.5 py-0.5 text-xs font-mono text-amber-900">REFERENCES users(id)</code>) and read it server-side. Create the profile row in your signup code or an edge function; list and delete users through the admin API.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">How do I see the source of a database function?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Click it under Cron &amp; RPC &rarr; Functions, or run <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase functions get &lt;name&gt;</code>. You get the full <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">CREATE OR REPLACE FUNCTION</code> statement; edit and run it in the SQL editor to change the function. <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">pg_get_functiondef()</code> and other system-catalog queries are refused in your own SQL. See <a href="/docs/cron" class="cursor-pointer text-eurobase-700 hover:underline">Cron &amp; RPC</a>.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">How do I keep a test project apart from production in the CLI?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Log in with a personal access token scoped to the test project, under its own <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">HOME</code>: the CLI keeps its login and active project only in <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">$HOME/.eurobase/config.json</code>, and a scoped token is refused for every other project. See <a href="/docs/cli" class="cursor-pointer text-eurobase-700 hover:underline">CLI</a>.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Can I copy my schema to a test project without the data?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Yes. Apply your migration files to a fresh, empty project (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase migrations up</code>) &mdash; you get tables, foreign keys, CHECK constraints, RLS policies and functions, with no rows copied. There&rsquo;s no standalone schema-only export yet; on Team you can also <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">pg_dump --schema-only</code> over the direct connection. See <a href="/docs/migrations" class="cursor-pointer text-eurobase-700 hover:underline">Migrations</a>.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">How do I check a policy change and roll it back safely?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Apply it as a migration (one transaction: all or nothing), with a reviewed rollback migration written beforehand, since there are no down migrations. Rehearse both on a test project, save the policies and function definitions before and after through the schema endpoints, and test real calls as a user, anonymously and with your secret key. See <a href="/docs/migrations" class="cursor-pointer text-eurobase-700 hover:underline">Migrations</a>.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">How do I see my full RLS policies for a table?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Use the schema endpoints rather than catalog SQL (which is refused): <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">GET /platform/projects/&lt;id&gt;/schema/tables/&lt;table&gt;/policies</code> returns each policy&rsquo;s USING and WITH CHECK (developer role); <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">GET .../schema</code> and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">.../schema/rls-audit</code> give foreign keys and RLS status at viewer level. <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">GET .../schema/functions/&lt;name&gt;</code> returns a function&rsquo;s definition, including trigger functions and the platform helpers <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth_uid()</code> / <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth_role()</code> / <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth_email()</code>. See <a href="/docs/rls" class="cursor-pointer text-eurobase-700 hover:underline">Row-Level Security</a>.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">How do I make several writes atomic without client transactions?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Every API request runs in its own transaction. Put the related writes in a trigger (it commits or rolls back with the write that fired it) or in an RPC function (the whole function is one transaction; an exception undoes everything). Both run with the caller&rsquo;s rights, so RLS applies; <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">SECURITY DEFINER</code> isn&rsquo;t accepted. See <a href="/docs/rls" class="cursor-pointer text-eurobase-700 hover:underline">Row-Level Security</a> for a change-history example and the isolation level requests run at (READ COMMITTED).</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Can I send auth emails (reset, magic link) to my own site, with my own templates and sender?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Redirect URLs to your own app are fully configurable (allow-listed, same idea as Supabase's site URL). Custom email templates are available on Pro. A custom SMTP sender is a Pro feature we're finalising — validate your provider in a test project before relying on it.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Private storage with signed URLs?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Yes — a private per-project EU bucket with signed download URLs. Access differs from Supabase's <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">storage.foldername()</code> policies: files are owned per end-user automatically, with a separate mechanism for developer-shared folders (authenticated or public). SDK signed-URL expiry caps at 1 hour, so long playback sessions re-issue the URL.</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Auth from your server</h3>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Can I create or import users from my server, e.g. with their email already confirmed?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Yes, with the users API and a <strong>project token with the Admin role</strong> (not the secret key &mdash; there's no <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">/v1/auth/admin</code>). <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">POST /platform/projects/&lt;id&gt;/users</code> creates one user; <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">POST …/users/batch</code> imports up to 500 per request. The password is optional (users then sign in by magic link or set one with a password reset), <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">email_confirmed</code> defaults to true, and a batch can carry bcrypt <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">password_hash</code>es and keep existing user ids. Re-running an import is safe. See <a href="/docs/users" class="cursor-pointer text-eurobase-700 hover:underline">Managing End Users</a>.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Can I fetch, list, change and delete users from my server?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Yes, the same API: <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">GET …/users?email=</code> (exact) or <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">?search=</code>, a list with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">last_sign_in_at</code> and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">email_confirmed_at</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">PATCH …/users/&lt;id&gt;</code> to change the email, name, metadata or confirmation, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">DELETE</code>, suspend and reset password.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Can I verify access tokens on my server without a network call?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Yes. Access tokens are ES256-signed with your project's own key pair; the public key is at <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">https://your-project.eurobase.app/v1/auth/.well-known/jwks.json</code>. Verify with any JWT library that supports JWKS (issuer <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eurobase</code>, audience = your project ID); see <a href="/docs/auth" class="text-eurobase-600 hover:underline cursor-pointer">chapter 6</a> for an example. A locally verified token stays valid until it expires, so use <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">GET /v1/auth/user</code> where you need to confirm the user still exists and read their current profile.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">With email confirmation on, users who haven't confirmed get "invalid email or password". Why?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">That changed: signing in with the right password before confirming now answers <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">403 email_not_confirmed</code> (SDK: <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">EMAIL_NOT_CONFIRMED</code>), so you can offer <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">resendVerification(email)</code>. Sign-up returns no session until the user confirms. Whether a user has confirmed is in <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">email_confirmed_at</code>. Use <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">@eurobase/sdk</code> 0.10.0 or later with this setting.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Can a signed-in user change their password?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Yes: <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">POST /v1/auth/user/password</code> with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">current_password</code> and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">new_password</code> and the user's access token. Their other sessions end once their current access token expires, and the response is a fresh session. An SDK helper (<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">updatePassword</code>) needs <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">@eurobase/sdk</code> 0.11.0 or later.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">What rate limits should I set for a launch or migration day?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">When many users sign up or sign in at once, raise sign-ups / sign-ins to 30 and token verifications (which include magic-link sign-ins) to 150 per 5 minutes under Auth &rarr; Rate Limits beforehand. See <a href="/docs/rate-limits" class="cursor-pointer text-eurobase-700 hover:underline">Rate Limits</a>.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Is there an hourly limit on auth emails with the built-in sender?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">No hourly cap on the number of emails sent. Per address: 3 magic links and 3 password-reset emails per 15 minutes, and one verification resend per 5 minutes. Per client IP: 60 password-reset and 60 verification emails an hour, and 300 magic-link requests an hour. For a migration day, magic links (300/h) go further than mass password resets (60/h).</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Reliability, backups &amp; leaving</h3>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">What backups and uptime do I get?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">The managed database is backed up automatically about every 24 hours at the infrastructure level on every tier (worst-case data loss ≈ 24 hours). Customer-controlled backups and self-serve restore are a Team-tier feature (dedicated EU database). Pro carries a 99.5% monthly availability target — see the <a href="/legal/sla" class="cursor-pointer text-eurobase-700 hover:underline">SLA</a> for the full detail.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">Do you provide a DPA and a sub-processor list?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">Yes — an auto-generated DPA report per project, and a published, EU-only sub-processor list (Scaleway for compute/database/storage/email in France, Mollie for payments, GatewayAPI for SMS). See the <a href="/docs/compliance" class="cursor-pointer text-eurobase-700 hover:underline">Compliance</a> chapter.</p>
				</div>

				<div class="rounded-xl border border-gray-200 bg-white p-5">
					<p class="text-sm font-semibold text-gray-900">What happens to my data if Eurobase shuts down?</p>
					<p class="mt-1 text-sm text-gray-700 leading-relaxed">If we cease operations, we keep the Service and your data online and exportable for at least 90 days before any deletion, with email notice to project owners. Your data is portable at any time regardless: export your schema, all table data, and a storage manifest from the console or API.</p>
				</div>

				<div class="rounded-xl border border-eurobase-200 bg-eurobase-50/50 p-5">
					<div class="flex gap-3">
						<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M12 21a9.004 9.004 0 0 0 8.716-6.747M12 21a9.004 9.004 0 0 1-8.716-6.747M12 21c2.485 0 4.5-4.03 4.5-9S14.485 3 12 3m0 18c-2.485 0-4.5-4.03-4.5-9S9.515 3 12 3m0 0a8.997 8.997 0 0 1 7.843 4.582M12 3a8.997 8.997 0 0 0-7.843 4.582m15.686 0A11.953 11.953 0 0 1 12 10.5c-2.998 0-5.74-1.1-7.843-2.918m15.686 0A8.959 8.959 0 0 1 21 12c0 .778-.099 1.533-.284 2.253m0 0A17.919 17.919 0 0 1 12 16.5c-3.162 0-6.133-.815-8.716-2.247m0 0A9.015 9.015 0 0 1 3 12c0-1.605.42-3.113 1.157-4.418" />
						</svg>
						<p class="text-sm text-eurobase-800">
							Everything runs in the EU on Scaleway (France) — database, storage, and custom Go auth services — end to end, under EU jurisdiction. That data-sovereignty guarantee is the main reason most teams make this move.
						</p>
					</div>
				</div>
			</div>
		</section>
