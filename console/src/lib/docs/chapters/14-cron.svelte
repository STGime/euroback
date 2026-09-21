		<section id="cron" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">14. Scheduled Jobs</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex needs to clean up expired sessions and send weekly reports automatically.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					Scheduled jobs let you run SQL statements or database functions on a recurring schedule. No server needed &mdash; Eurobase executes them automatically in your project's database.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Creating a scheduled job</h3>
				<ol class="text-sm text-gray-700 space-y-1.5 ml-4 list-decimal">
					<li>Go to the <strong>Cron</strong> tab in your project</li>
					<li>Click <strong>New Job</strong></li>
					<li>Give it a name (e.g. "Clean expired sessions")</li>
					<li>Choose a schedule preset or write a custom cron expression</li>
					<li>Select the action type: <strong>SQL</strong> (run a query) or <strong>RPC</strong> (call a function)</li>
					<li>Write the SQL or function name</li>
					<li>Click <strong>Create</strong></li>
				</ol>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Common examples</h3>

				<div class="space-y-3">
					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Clean up expired sessions (every hour)</p>
						<p class="text-xs text-gray-500 mt-0.5">Schedule: <code class="bg-white border border-gray-200 rounded px-1">0 * * * *</code></p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">DELETE FROM sessions WHERE expires_at &lt; now()</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Send weekly digest (every Monday at 9am)</p>
						<p class="text-xs text-gray-500 mt-0.5">Schedule: <code class="bg-white border border-gray-200 rounded px-1">0 9 * * 1</code></p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">SELECT send_weekly_digest()</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Archive old records (daily at midnight)</p>
						<p class="text-xs text-gray-500 mt-0.5">Schedule: <code class="bg-white border border-gray-200 rounded px-1">0 0 * * *</code></p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">INSERT INTO archive SELECT * FROM logs WHERE created_at &lt; now() - interval '30 days'; DELETE FROM logs WHERE created_at &lt; now() - interval '30 days';</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Check pending orders (every 5 minutes)</p>
						<p class="text-xs text-gray-500 mt-0.5">Schedule: <code class="bg-white border border-gray-200 rounded px-1">*/5 * * * *</code></p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">SELECT process_pending_orders()</div>
					</div>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Cron schedule reference</h3>
				<div class="rounded-lg border border-gray-200 overflow-hidden">
					<table class="w-full text-xs">
						<thead class="bg-gray-50">
							<tr>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Field</th>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Values</th>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Special</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-gray-100">
							<tr><td class="px-3 py-1.5 text-gray-700">Minute</td><td class="px-3 py-1.5 text-gray-500">0-59</td><td class="px-3 py-1.5 text-gray-500">* , */N</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700">Hour</td><td class="px-3 py-1.5 text-gray-500">0-23</td><td class="px-3 py-1.5 text-gray-500">* , */N</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700">Day of month</td><td class="px-3 py-1.5 text-gray-500">1-31</td><td class="px-3 py-1.5 text-gray-500">* , */N</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700">Month</td><td class="px-3 py-1.5 text-gray-500">1-12</td><td class="px-3 py-1.5 text-gray-500">* , */N</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700">Day of week</td><td class="px-3 py-1.5 text-gray-500">0-6 (Sun=0)</td><td class="px-3 py-1.5 text-gray-500">* , */N</td></tr>
						</tbody>
					</table>
				</div>

				<div class="rounded-lg border border-gray-200 bg-gray-50 p-3 mt-3">
					<p class="text-xs font-semibold text-gray-700 mb-1">Quick reference</p>
					<div class="grid grid-cols-2 gap-x-6 gap-y-0.5 text-xs text-gray-600">
						<span><code class="bg-white border border-gray-200 rounded px-1">* * * * *</code> &mdash; every minute</span>
						<span><code class="bg-white border border-gray-200 rounded px-1">*/5 * * * *</code> &mdash; every 5 minutes</span>
						<span><code class="bg-white border border-gray-200 rounded px-1">0 * * * *</code> &mdash; every hour</span>
						<span><code class="bg-white border border-gray-200 rounded px-1">0 0 * * *</code> &mdash; daily at midnight</span>
						<span><code class="bg-white border border-gray-200 rounded px-1">0 9 * * 1</code> &mdash; Monday 9am</span>
						<span><code class="bg-white border border-gray-200 rounded px-1">0 0 1 * *</code> &mdash; 1st of month</span>
					</div>
				</div>

				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3 mt-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<div class="text-sm text-eurobase-800">
						<p><strong>Plan limits:</strong> Free plan includes 2 scheduled jobs. Pro plan has unlimited jobs.</p>
						<p class="mt-1">Jobs run SQL in your project's database schema with full access. They execute as the system user, not as an end-user &mdash; RLS policies are bypassed.</p>
					</div>
				</div>
			<h3 class="text-lg font-semibold text-gray-900 mt-6">RPC Functions</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					RPC (Remote Procedure Call) functions are reusable PostgreSQL functions stored in your database. Unlike raw SQL cron actions, functions can contain complex logic (loops, conditionals, error handling) and can be called from both cron jobs and your app via the SDK.
				</p>

				<div class="rounded-lg border border-amber-200 bg-amber-50/60 px-4 py-3 mt-2">
					<p class="text-sm text-amber-900"><span class="font-semibold">RPC vs Cron Job vs DB Trigger vs Edge Function:</span> Eurobase has four kinds of "server-side code" and the distinction matters. Quick gist: RPC = callable SQL (this section). Cron Job = scheduled SQL (above). DB Trigger = reactive SQL fired by row events on a table (managed in Database &rarr; Triggers). <a href="/docs/edge-functions" class="text-amber-900 underline cursor-pointer">Edge Function</a> = TypeScript in a Deno container, for external API calls and JS-ecosystem things. The <a href="/docs/edge-functions" class="text-amber-900 underline cursor-pointer">full comparison table</a> in the Edge Functions chapter has language, transactional semantics, and use cases side by side.</p>
				</div>

				<h4 class="text-sm font-semibold text-gray-700 mt-4">Creating a function</h4>
				<p class="text-sm text-gray-700 leading-relaxed">
					When creating a cron job, select "RPC Function" and click "Create New Function". Choose a name, language, return type, and write the function body.
				</p>

				<div class="mt-3 space-y-3">
					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Example: Clean up expired sessions (void — for cron)</p>
						<p class="text-xs text-gray-500 mt-0.5">Language: PL/pgSQL &middot; Returns: void</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">BEGIN<br/>&nbsp;&nbsp;DELETE FROM refresh_tokens WHERE expires_at &lt; now();<br/>&nbsp;&nbsp;DELETE FROM email_tokens WHERE expires_at &lt; now();<br/>END;</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Example: Get active user count (integer — for SDK)</p>
						<p class="text-xs text-gray-500 mt-0.5">Language: SQL &middot; Returns: integer</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">SELECT count(*)::integer FROM users WHERE last_sign_in_at > now() - interval '30 days';</div>
					</div>

					<div class="rounded-lg border border-gray-200 bg-gray-50 p-3">
						<p class="text-xs font-semibold text-gray-700">Example: Generate daily stats (jsonb — for SDK)</p>
						<p class="text-xs text-gray-500 mt-0.5">Language: PL/pgSQL &middot; Returns: jsonb</p>
						<div class="mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 font-mono text-[11px] text-green-400">DECLARE result jsonb;<br/>BEGIN<br/>&nbsp;&nbsp;SELECT jsonb_build_object(<br/>&nbsp;&nbsp;&nbsp;&nbsp;'total_users', (SELECT count(*) FROM users),<br/>&nbsp;&nbsp;&nbsp;&nbsp;'active_today', (SELECT count(*) FROM users WHERE last_sign_in_at > now() - interval '1 day')<br/>&nbsp;&nbsp;) INTO result;<br/>&nbsp;&nbsp;RETURN result;<br/>END;</div>
					</div>
				</div>

				<h4 class="text-sm font-semibold text-gray-700 mt-4">Return types explained</h4>
				<div class="rounded-lg border border-gray-200 overflow-hidden mt-2">
					<table class="w-full text-xs">
						<thead class="bg-gray-50">
							<tr>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Type</th>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">When to use</th>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">SDK result</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-gray-100">
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">void</td><td class="px-3 py-1.5 text-gray-500">Cron jobs, cleanup tasks, side effects only</td><td class="px-3 py-1.5 text-gray-500">null</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">text</td><td class="px-3 py-1.5 text-gray-500">Return a message or formatted string</td><td class="px-3 py-1.5 text-gray-500">"hello world"</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">integer</td><td class="px-3 py-1.5 text-gray-500">Return a count or numeric value</td><td class="px-3 py-1.5 text-gray-500">42</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">boolean</td><td class="px-3 py-1.5 text-gray-500">Return true/false checks</td><td class="px-3 py-1.5 text-gray-500">true</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">jsonb</td><td class="px-3 py-1.5 text-gray-500">Return structured data (objects, arrays)</td><td class="px-3 py-1.5 text-gray-500">{"{'key': 'value'}"}</td></tr>
						</tbody>
					</table>
				</div>

				<h4 class="text-sm font-semibold text-gray-700 mt-4">Calling functions from the SDK</h4>
				<p class="text-sm text-gray-700 leading-relaxed">
					Functions with a return type (not void) can be called from your app. The return value is sent back as JSON.
				</p>
				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto mt-2">
					<pre>// Call an RPC function from the SDK
const {'{'} data, error {'}'} = await eb.db.rpc('get_active_user_count')
console.log(data) // 42

// Call a function that returns JSON
const {'{'} data: stats {'}'} = await eb.db.rpc('generate_daily_stats')
console.log(stats) // {'{'} total_users: 150, active_today: 23 {'}'}</pre>
				</div>

				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3 mt-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<div class="text-sm text-eurobase-800">
						<p><strong>Cron + SDK tip:</strong> Create a function that returns <code class="bg-white/50 rounded px-1">void</code> for cron (e.g. cleanup tasks), and separate functions that return data for your SDK calls (e.g. stats, reports). A function can do both &mdash; perform side effects and return a result.</p>
					</div>
				</div>
			</div>

			<div class="mt-6 text-right">
				<a href="/docs/edge-functions" class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Edge Functions &rarr;
				</a>
			</div>
		</section>
