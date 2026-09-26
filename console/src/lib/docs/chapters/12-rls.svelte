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

				<h3 id="security-definer" class="text-lg font-semibold text-gray-900 mt-6 scroll-mt-20">Why no SECURITY DEFINER, and what to do instead</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Eurobase rejects <code class="bg-gray-100 border border-gray-200 rounded px-1">SECURITY DEFINER</code> in the SQL editor, migrations, scheduled jobs and edge-function SQL. A definer function runs with the privileges of the role that owns it, not the caller's. In your project that owner is a platform role, so a definer function would step around both your project's isolation and your row-level security &mdash; for every caller, forever. Functions are <code class="bg-gray-100 border border-gray-200 rounded px-1">SECURITY INVOKER</code> by default: they run as the caller, and your RLS policies decide what they may touch. The usual reasons for reaching for a definer function each have a direct replacement:
				</p>
				<div class="space-y-3">
					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">"Trusted code must see rows the end user can't" &rarr; admit the service role in the policy</p>
						<p class="mt-1 text-[11px] text-gray-500">Calls with the secret key, edge functions called without a signed-in user, and scheduled jobs set to run as the service role all pass <code>is_service_role()</code>. End users still see only their own rows.</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400 space-y-0.5">
							<div>CREATE POLICY "own or service" ON bookings FOR ALL</div>
							<div>&nbsp;&nbsp;USING (is_service_role() OR user_id = auth_uid())</div>
							<div>&nbsp;&nbsp;WITH CHECK (is_service_role() OR user_id = auth_uid());</div>
						</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">"A nightly job must clean up across all users" &rarr; a scheduled job with <em>Run as service role</em></p>
						<p class="mt-1 text-[11px] text-gray-500">Example: a retention job that deletes bookings older than two years, whoever made them. Create it under <a href="/docs/cron" class="text-eurobase-600 hover:underline cursor-pointer">Scheduled Jobs</a> with <strong>Run as service role</strong> switched on (the default for new jobs), and give the service role a DELETE policy:</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400 space-y-1">
							<div class="text-gray-500">-- once, in the SQL editor or a migration</div>
							<div>CREATE POLICY "service housekeeping" ON bookings FOR DELETE USING (is_service_role());</div>
							<div class="mt-1.5 text-gray-500">-- the job's SQL, e.g. daily at 03:00</div>
							<div>DELETE FROM bookings WHERE ends_at &lt; now() - interval '2 years';</div>
						</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">"A reusable function called from the SDK" &rarr; a plain (invoker) function</p>
						<p class="mt-1 text-[11px] text-gray-500">It runs as whoever calls it, so an end user gets their own rows and the service role gets all of them &mdash; the same policies apply as for a direct query.</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400 space-y-0.5">
							<div>CREATE OR REPLACE FUNCTION upcoming_bookings() RETURNS SETOF bookings</div>
							<div>&nbsp;&nbsp;LANGUAGE sql STABLE AS $$</div>
							<div>&nbsp;&nbsp;&nbsp;&nbsp;SELECT * FROM bookings WHERE starts_at &gt; now() ORDER BY starts_at</div>
							<div>&nbsp;&nbsp;$$;</div>
							<div class="mt-1.5 text-gray-500">// from the SDK</div>
							<div>const {'{'} data {'}'} = await eb.db.rpc('upcoming_bookings')</div>
						</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">"An end user's action must change other users' rows" &rarr; a narrow policy, or your server</p>
						<p class="mt-1 text-[11px] text-gray-500">Write a policy that allows exactly that change (for example, members of the same team may update the team's rows). If it can't be expressed as a policy, do it on your own server with the secret key after checking the request there. An edge function called with the user's session acts as that user, so it can't do more than the user can.</p>
					</div>
				</div>
			</div>

			<div class="mt-6 text-right">
				<a href="/docs/vault" class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Vault &rarr;
				</a>
			</div>
		</section>
