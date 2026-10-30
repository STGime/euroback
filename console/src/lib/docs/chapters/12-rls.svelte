		<section id="rls" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">12. Row-Level Security (RLS)</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex needs each law firm employee to only see their own cases.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					Row-Level Security lets you control which rows each user can read, insert, update, or delete. Policies are written in SQL and enforced by PostgreSQL itself &mdash; no application code needed.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Auth helper functions</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Eurobase provides built-in functions you can use in RLS policies to access the current user's identity. Both the native (no-dot) and Supabase-style (<code class="bg-gray-100 border border-gray-200 rounded px-1">auth.*</code>) forms are equivalent &mdash; pick whichever reads better. <code class="bg-gray-100 border border-gray-200 rounded px-1">is_service_role()</code> is true for service-key calls and lets you write a single policy that admits both end-users and trusted server-side code.
				</p>

				<div class="rounded-lg border border-gray-200 overflow-hidden">
					<table class="w-full text-xs">
						<thead class="bg-gray-50">
							<tr>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Function</th>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Returns</th>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Description</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-gray-100">
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">auth_uid() / auth.uid()</td><td class="px-3 py-1.5 text-gray-500">uuid</td><td class="px-3 py-1.5 text-gray-500">Current user's ID, NULL when no end-user context</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">auth_email() / auth.email()</td><td class="px-3 py-1.5 text-gray-500">text</td><td class="px-3 py-1.5 text-gray-500">Current user's email</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">auth_role() / auth.role()</td><td class="px-3 py-1.5 text-gray-500">text</td><td class="px-3 py-1.5 text-gray-500">'service_role', 'authenticated', or 'anon'</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">is_service_role()</td><td class="px-3 py-1.5 text-gray-500">boolean</td><td class="px-3 py-1.5 text-gray-500">True for calls made with the service key (bypasses end-user RLS)</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">auth.jwt()</td><td class="px-3 py-1.5 text-gray-500">jsonb</td><td class="px-3 py-1.5 text-gray-500">{'{'} sub, email, role {'}'} &mdash; for policies that read JWT claims</td></tr>
						</tbody>
					</table>
				</div>

				<div class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 mt-3">
					<p class="text-xs text-amber-800">
						<strong>Heads up &mdash; do not redefine <code class="bg-amber-100 border border-amber-200 rounded px-1">auth.uid()</code> in your migrations.</strong>
						The Supabase boilerplate that reads <code class="bg-amber-100 border border-amber-200 rounded px-1">request.jwt.claims</code> won't work here &mdash; Eurobase uses a different session GUC. The built-in <code class="bg-amber-100 border border-amber-200 rounded px-1">auth.uid()</code> already does the right thing.
					</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Common RLS patterns</h3>

				<div class="space-y-3">
					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Users can only read their own rows</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">CREATE POLICY "read own" ON todos FOR SELECT USING (user_id = auth_uid());</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Users can insert with their own ID</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">CREATE POLICY "insert own" ON todos FOR INSERT WITH CHECK (user_id = auth_uid());</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Users can update only their own rows</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">CREATE POLICY "update own" ON todos FOR UPDATE USING (user_id = auth_uid());</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Public read, authenticated write</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400 space-y-0.5">
							<div>CREATE POLICY "public read" ON posts FOR SELECT USING (true);</div>
							<div>CREATE POLICY "auth insert" ON posts FOR INSERT WITH CHECK (auth_role() = 'authenticated');</div>
						</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Admin access by email</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">CREATE POLICY "admin all" ON users FOR ALL USING (auth_email() = 'admin@company.eu');</div>
					</div>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Views</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					A view must apply the RLS policies of its tables to whoever reads it, so create views with <code class="bg-gray-100 border border-gray-200 rounded px-1">security_invoker</code>:
				</p>
				<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">CREATE VIEW open_tasks WITH (security_invoker = true) AS SELECT * FROM tasks WHERE done = false;</div>
				<p class="text-sm text-gray-700 leading-relaxed mt-2">
					Without it, a view reads its tables with its owner's rights and skips their policies, so the SQL editor, migrations and MCP refuse to create one (or to switch <code class="bg-gray-100 border border-gray-200 rounded px-1">security_invoker</code> off). Materialized views are refused too: they store their rows and can't apply RLS. For precomputed data, use a view or a table you refresh from a cron job. Repeat <code class="bg-gray-100 border border-gray-200 rounded px-1">WITH (security_invoker = true)</code> whenever you replace or re-create a view: <code class="bg-gray-100 border border-gray-200 rounded px-1">CREATE OR REPLACE VIEW</code> without it switches the option off. For the same reason, <code class="bg-gray-100 border border-gray-200 rounded px-1">SECURITY DEFINER</code> functions and rules are refused: both run with the owner's rights. Use plain functions with RLS policies, and triggers instead of rules. On the Team plan, SQL you run over a direct database connection isn't checked: create views with <code class="bg-gray-100 border border-gray-200 rounded px-1">security_invoker</code> there too.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Full example: secure a tasks table</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Follow these steps to create a table where each user can only see and manage their own rows.
				</p>

				<div class="mt-3 space-y-3">
					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Step 1: Create the table with a user_id column</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400 space-y-0.5">
							<div>CREATE TABLE tasks (</div>
							<div>&nbsp;&nbsp;id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),</div>
							<div>&nbsp;&nbsp;user_id UUID NOT NULL REFERENCES users(id),</div>
							<div>&nbsp;&nbsp;title TEXT NOT NULL,</div>
							<div>&nbsp;&nbsp;completed BOOLEAN DEFAULT false,</div>
							<div>&nbsp;&nbsp;created_at TIMESTAMPTZ DEFAULT now()</div>
							<div>);</div>
						</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Step 2: RLS is enabled automatically</p>
						<p class="mt-1 text-[10px] text-gray-500">Tables created via the Eurobase console or API have RLS enabled by default. You'll see a green RLS badge on protected tables. If a table shows "RLS OFF" in the sidebar, enable it with:</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">ALTER TABLE tasks ENABLE ROW LEVEL SECURITY;</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Step 3: Add policies for each operation</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400 space-y-1">
							<div class="text-gray-500">-- Users can read only their own tasks</div>
							<div>CREATE POLICY "select own" ON tasks FOR SELECT</div>
							<div>&nbsp;&nbsp;USING (user_id = auth_uid());</div>
							<div class="mt-1.5 text-gray-500">-- Users can insert tasks with their own user_id</div>
							<div>CREATE POLICY "insert own" ON tasks FOR INSERT</div>
							<div>&nbsp;&nbsp;WITH CHECK (user_id = auth_uid());</div>
							<div class="mt-1.5 text-gray-500">-- Users can update only their own tasks</div>
							<div>CREATE POLICY "update own" ON tasks FOR UPDATE</div>
							<div>&nbsp;&nbsp;USING (user_id = auth_uid());</div>
							<div class="mt-1.5 text-gray-500">-- Users can delete only their own tasks</div>
							<div>CREATE POLICY "delete own" ON tasks FOR DELETE</div>
							<div>&nbsp;&nbsp;USING (user_id = auth_uid());</div>
						</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Step 4: Test it from the SDK</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400 space-y-0.5">
							<div class="text-gray-500">// Sign in as a user</div>
							<div>await eb.auth.signIn({'{'} email: 'alice@example.com', password: '...' {'}'})</div>
							<div></div>
							<div class="text-gray-500">// Insert a task — user_id is automatically checked by RLS</div>
							<div>await eb.db.from('tasks').insert({'{'} user_id: session.user.id, title: 'Buy milk' {'}'})</div>
							<div></div>
							<div class="text-gray-500">// Query — only Alice's tasks are returned</div>
							<div>const {'{'} data {'}'} = await eb.db.from('tasks').select('*')</div>
							<div class="text-gray-500">// data = [{'{'} title: "Buy milk", ... {'}'}] — Bob's tasks are invisible</div>
						</div>
					</div>
				</div>

				<div class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 flex gap-3 mt-4">
					<svg class="h-5 w-5 text-amber-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
					</svg>
					<div class="text-sm text-amber-800">
						<p><strong>RLS is on by default</strong> for tables created via the console. But without policies, no rows are visible. Add at least a SELECT policy so users can read data. Tables showing "RLS OFF" in the sidebar need to be secured with <code class="bg-amber-100 rounded px-1">ALTER TABLE ... ENABLE ROW LEVEL SECURITY;</code></p>
					</div>
				</div>

				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3 mt-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<div class="text-sm text-eurobase-800">
						<p><strong>Supabase compatibility:</strong> Eurobase's <code class="bg-white/50 rounded px-1">auth_uid()</code>, <code class="bg-white/50 rounded px-1">auth_role()</code>, and <code class="bg-white/50 rounded px-1">auth_email()</code> follow the same pattern as Supabase's GoTrue. RLS policies written for Supabase work in Eurobase with minimal changes.</p>
						<p class="mt-1"><strong>Secret API key</strong> (<code class="bg-white/50 rounded px-1">eb_sk_</code>) bypasses RLS entirely &mdash; use it for server-side admin access, never in client code.</p>
					</div>
				</div>

				<div class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 flex gap-3 mt-3">
					<svg class="h-5 w-5 text-red-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
					</svg>
					<div class="text-sm text-red-900">
						<p><strong>Common gotcha: <code class="bg-red-100 rounded px-1">row-level security policy denied this operation</code> on INSERT, even with <code class="bg-red-100 rounded px-1">WITH CHECK (true)</code>.</strong></p>
						<p class="mt-1.5 leading-relaxed">The SDK executes every insert as <code class="bg-red-100 rounded px-1">INSERT ... RETURNING *</code> so it can return the created row to your client. On <code class="bg-red-100 rounded px-1">RETURNING</code>, Postgres applies RLS <em>twice</em>: the INSERT policy's <code class="bg-red-100 rounded px-1">WITH CHECK</code> when writing the row, then the SELECT policy's <code class="bg-red-100 rounded px-1">USING</code> when reading it back. If you have an INSERT policy but no matching SELECT policy, the write rolls back with the same error text as a real INSERT failure &mdash; misleading, but that is the wire format.</p>
						<p class="mt-1.5 leading-relaxed"><strong>Fix</strong>: add a SELECT policy that permits the authenticated user to read the row they just wrote &mdash; usually the same predicate as the INSERT policy. If your INSERT policy is <code class="bg-red-100 rounded px-1">WITH CHECK (user_id = auth_uid())</code>, add <code class="bg-red-100 rounded px-1">FOR SELECT USING (user_id = auth_uid())</code>. The Table Editor bypasses this because it runs through the elevated <code class="bg-red-100 rounded px-1">is_service_role()</code> branch, which is why the same insert succeeds there and fails from the SDK.</p>
					</div>
				</div>
				<h3 class="text-lg font-semibold text-gray-900 mt-6">Inspecting your schema, policies and functions</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					You can read your full schema without raw SQL, at two access levels. With <strong>viewer</strong>: the Database &rarr; Schema diagram (foreign keys drawn between tables), <code class="bg-gray-100 rounded px-1">GET /platform/projects/&lt;id&gt;/schema</code> (columns, foreign keys, primary/unique keys, indexes, and whether RLS is on), and <code class="bg-gray-100 rounded px-1">GET .../schema/rls-audit</code> (which tables have RLS and how many policies). With <strong>developer</strong>: the full policy expressions (USING + WITH CHECK) at <code class="bg-gray-100 rounded px-1">GET .../schema/tables/&lt;table&gt;/policies</code>, and a function&rsquo;s definition at <code class="bg-gray-100 rounded px-1">GET .../schema/functions/&lt;name&gt;</code> &mdash; your RPC functions, your trigger functions, and the platform helpers <code class="bg-gray-100 rounded px-1">auth_uid()</code>, <code class="bg-gray-100 rounded px-1">auth_role()</code> and <code class="bg-gray-100 rounded px-1">auth_email()</code> (returned with <code class="bg-gray-100 rounded px-1">"platform": true</code>; they&rsquo;re managed by the platform and the same in every project &mdash; don&rsquo;t change them from SQL, your policies depend on them) (or the MCP tools <code class="bg-gray-100 rounded px-1">describeTable</code> / <code class="bg-gray-100 rounded px-1">getDbFunction</code>). Catalog queries in your own SQL (<code class="bg-gray-100 rounded px-1">pg_policies</code>, <code class="bg-gray-100 rounded px-1">pg_get_expr</code>, <code class="bg-gray-100 rounded px-1">pg_get_functiondef</code>) are refused by a tenant-isolation guard &mdash; these endpoints run that introspection for you. CHECK-constraint expressions aren&rsquo;t returned by the JSON endpoints; read those from your migration files (or, on Team, a direct <code class="bg-gray-100 rounded px-1">pg_dump --schema-only</code>).
				</p>

				<p class="text-sm text-gray-700 leading-relaxed mt-2">
					Two helpers your policies use live in the platform&rsquo;s shared schema, not yours, so the function lookup doesn&rsquo;t return them. They&rsquo;re plain (SECURITY INVOKER) STABLE functions, managed by the platform and the same in every project (don&rsquo;t redefine them), and read values the platform sets for each request:
				</p>
				<div class="rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto mt-2">
					<pre>public.is_service_role()     -- select current_setting('app.end_user_role', true) = 'service'
public.current_end_user_id() -- select nullif(current_setting('app.end_user_id', true), '')::uuid</pre>
				</div>
				<p class="text-sm text-gray-700 leading-relaxed mt-2">
					<code class="bg-gray-100 rounded px-1">GET .../schema</code> lists tables, not views, and nothing lists which functions use a given table: search your function definitions and migration files. New and changed views must be <code class="bg-gray-100 rounded px-1">security_invoker</code>, so they apply RLS for whoever reads them.
				</p>

				<p class="text-sm text-gray-700 leading-relaxed mt-2">
					The platform-managed tables in your schema (<code class="bg-gray-100 rounded px-1">users</code>, <code class="bg-gray-100 rounded px-1">user_identities</code>, <code class="bg-gray-100 rounded px-1">refresh_tokens</code>, <code class="bg-gray-100 rounded px-1">email_tokens</code>, <code class="bg-gray-100 rounded px-1">storage_objects</code>, <code class="bg-gray-100 rounded px-1">storage_shared_prefixes</code>, <code class="bg-gray-100 rounded px-1">vault_secrets</code>, <code class="bg-gray-100 rounded px-1">user_passkey_credentials</code>, <code class="bg-gray-100 rounded px-1">webauthn_challenges</code>) come with their own policies, which the platform depends on. You can read them, but the console and the schema API refuse to change them: RLS presets, policy create/drop, RLS on/off, and creating, dropping or renaming these tables are refused (403). On shared projects SQL gets <code class="bg-gray-100 rounded px-1">must be owner</code> too; on Team, leave them unchanged in your own SQL as well. If one of them looks wrong in your schema, contact support.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Who can call your functions</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Roles and grants are managed by the platform (<code class="bg-gray-100 rounded px-1">GRANT</code> / <code class="bg-gray-100 rounded px-1">REVOKE</code> are refused). Your app&rsquo;s data requests &mdash; REST, SQL and RPC, with the public key (with or without a user token) or the secret key &mdash; run as a role that doesn&rsquo;t own the tables you create, so the policies on every table with RLS enabled always apply; the secret key passes them through their <code class="bg-gray-100 rounded px-1">is_service_role()</code> branch. Owner rights, which skip RLS, are limited to the console and the MCP/CLI tools (SQL editor, table editor, schema changes), migrations, schema changes made with the secret key, and project exports. Any function in your schema can be called through <code class="bg-gray-100 rounded px-1">POST /v1/db/rpc/&lt;name&gt;</code> by anyone with your public key, because who may execute it can&rsquo;t be restricted: a function meant only for your server must check <code class="bg-gray-100 rounded px-1">public.is_service_role()</code> itself (and raise otherwise), and one for signed-in users should check <code class="bg-gray-100 rounded px-1">auth_uid()</code>.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Verifying a session&rsquo;s identity</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Your policies act on the end-user the request carries: <code class="bg-gray-100 rounded px-1">app.end_user_id</code> (the UUID, read by <code class="bg-gray-100 rounded px-1">auth_uid()</code> / <code class="bg-gray-100 rounded px-1">current_end_user_id()</code>) and <code class="bg-gray-100 rounded px-1">app.end_user_role</code> (read by <code class="bg-gray-100 rounded px-1">auth_role()</code> / <code class="bg-gray-100 rounded px-1">is_service_role()</code>), both set by the platform per request. To see what a given session resolves to, call <code class="bg-gray-100 rounded px-1">GET /v1/auth/user</code> with that user&rsquo;s access token &mdash; it returns the user record and claims. How your app maps that UUID onward (to a profile row and its relationships) is your own schema design; read it with the introspection methods above.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Making a column immutable</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					RLS decides <em>who</em> may update <em>which rows</em>, but a policy only sees the new row, so it can&rsquo;t stop one column from changing (there&rsquo;s no old row to compare against). To freeze a column such as <code class="bg-gray-100 rounded px-1">id</code> or <code class="bg-gray-100 rounded px-1">created_at</code> while still allowing other updates over the normal API, add a <code class="bg-gray-100 rounded px-1">BEFORE UPDATE</code> trigger in a <a href="/docs/migrations" class="text-eurobase-600 hover:underline">migration</a>:
				</p>
				<div class="rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto mt-2">
					<pre>create or replace function enforce_clubs_immutable()
returns trigger
language plpgsql
as $$
begin
  if new.id is distinct from old.id
     or new.created_at is distinct from old.created_at then
    raise exception 'clubs.id and clubs.created_at are immutable';
  end if;
  return new;
end;
$$;

create trigger clubs_immutable
  before update on clubs
  for each row
  execute function enforce_clubs_immutable();</pre>
				</div>
				<p class="text-sm text-gray-700 leading-relaxed mt-2">
					Keep the function plain (not <code class="bg-gray-100 rounded px-1">SECURITY DEFINER</code>, which self-serve surfaces refuse). It fires on every update &mdash; SDK, REST and SQL editor alike &mdash; so any attempt to change <code class="bg-gray-100 rounded px-1">id</code> or <code class="bg-gray-100 rounded px-1">created_at</code> is rejected while other columns update normally. Pair it with your RLS policy, which restricts which rows a given role may update.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Keeping a change history</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					A plain trigger can write an append-only history of corrections, with no <code class="bg-gray-100 rounded px-1">SECURITY DEFINER</code> and no extra grants. It runs inside the request that made the change, as the same caller, so <code class="bg-gray-100 rounded px-1">auth_uid()</code> is the user making the correction and its insert into the history table is checked against that table&rsquo;s policies:
				</p>
				<div class="rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto mt-2">
					<pre>create table assessment_history (
  id            uuid primary key default gen_random_uuid(),
  assessment_id uuid not null references assessments(id),
  changed_by    uuid default auth_uid(),
  changed_at    timestamptz not null default now(),
  old_row       jsonb not null,
  new_row       jsonb not null
);
alter table assessment_history enable row level security;

-- Insert only in your own name; no UPDATE or DELETE policy,
-- so the app can never change or remove history rows.
create policy history_insert on assessment_history for insert
  with check (changed_by = auth_uid() or public.is_service_role());
-- add a SELECT policy for whoever may read the history

create or replace function record_assessment_change()
returns trigger
language plpgsql
as $$
begin
  insert into assessment_history (assessment_id, changed_by, old_row, new_row)
  values (old.id, auth_uid(), to_jsonb(old), to_jsonb(new));
  return new;
end;
$$;

create trigger assessments_history
  after update on assessments
  for each row when (old.* is distinct from new.*)
  execute function record_assessment_change();</pre>
				</div>
				<p class="text-sm text-gray-700 leading-relaxed mt-2">
					<code class="bg-gray-100 rounded px-1">auth_uid()</code> is NULL without a signed-in user. With the policy above, an anonymous update is refused (its history row doesn&rsquo;t pass the check), and changes made with the secret key are recorded with <code class="bg-gray-100 rounded px-1">changed_by</code> NULL unless you write the ID yourself. The SQL editor and migrations run as the table owner, where RLS doesn&rsquo;t apply: to keep history rows unchangeable there too, add a <code class="bg-gray-100 rounded px-1">BEFORE UPDATE OR DELETE</code> trigger on the history table that raises an exception (removing it takes owner-level SQL in the editor or a migration, and both appear in your SQL log). Because history rows reference the assessment, an assessment with history can&rsquo;t be deleted; mark it as withdrawn instead, or decide whether its history should go with it (<code class="bg-gray-100 rounded px-1">on delete cascade</code>). A signed-in user allowed to insert history rows can also add one directly, though only in their own name. If history must come only from the trigger, make the history table service-only and make corrections through your server.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Several writes in one transaction</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Apps can&rsquo;t open a transaction of their own, but every API request runs in one, so related writes are atomic when they happen in one request:
				</p>
				<ul class="list-disc pl-5 text-sm text-gray-700 space-y-1 mt-2">
					<li><strong>Triggers:</strong> an insert, update or delete through the API (SDK or REST) and every trigger it fires commit or roll back together. If the trigger fails, the write is undone.</li>
					<li><strong>RPC functions:</strong> <code class="bg-gray-100 rounded px-1">POST /v1/db/rpc/&lt;function&gt;</code> (SDK: <code class="bg-gray-100 rounded px-1">eb.db.rpc('name', &#123; ... &#125;)</code>) runs the whole function in one transaction. If any statement fails or the function raises an exception, nothing is kept. RPC functions run with the caller&rsquo;s rights, so your policies apply to every statement inside.</li>
				</ul>
				<p class="text-sm text-gray-700 leading-relaxed mt-2">
					Use a trigger for what must happen on every change, such as the history above: it fires however the row is changed. Use an RPC function when one operation needs inputs or checks beyond a single row update; its writes still fire your triggers in the same transaction.
				</p>

			</div>

			<div class="mt-6 text-right">
				<a href="/docs/vault" class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Vault &rarr;
				</a>
			</div>
		</section>
