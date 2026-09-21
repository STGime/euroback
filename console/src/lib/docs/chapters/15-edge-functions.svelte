<script lang="ts">
	import { goto } from '$app/navigation';
	// In the single-page docs this scrolled; each chapter is now its own
	// URL, so cross-references navigate instead.
	function scrollTo(id: string) {
		goto(`/docs/${id}`);
	}
</script>

		<section id="edge-functions" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">15. Edge Functions</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex needs to process a payment webhook and update an order — this requires custom server-side logic beyond SQL.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					Edge Functions are serverless TypeScript/JavaScript functions that run on Eurobase's EU-sovereign infrastructure. They let you write custom server-side logic — payment processing, external API integrations, data transformations — without managing any servers.
				</p>

				<div class="rounded-lg bg-blue-50 border border-blue-200 px-4 py-3">
					<p class="text-sm text-blue-800"><span class="font-semibold">EU Sovereign:</span> Edge Functions run on Scaleway infrastructure in France. Unlike other platforms that route through US-hosted runtimes, your code and secrets never leave the EU.</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Server-side logic in Eurobase: four kinds, when to pick which</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Eurobase has four distinct surfaces for "code that runs on the server." The SDK shapes look similar in places, so it's worth getting the distinction clear before you start building. <strong>RPC functions</strong> and <strong>DB triggers</strong> are both PostgreSQL functions but with different invocation models; <strong>Cron jobs</strong> are scheduled wrappers around either an SQL statement or an RPC; <strong>Edge functions</strong> are a separate Deno runtime entirely.
				</p>
				<div class="overflow-x-auto">
					<table class="w-full text-sm border border-gray-200 rounded-lg">
						<thead class="bg-gray-50">
							<tr>
								<th class="px-3 py-2 text-left font-medium text-gray-700 border-b align-top">&nbsp;</th>
								<th class="px-3 py-2 text-left font-medium text-gray-700 border-b align-top">Cron Job</th>
								<th class="px-3 py-2 text-left font-medium text-gray-700 border-b align-top">RPC Function</th>
								<th class="px-3 py-2 text-left font-medium text-gray-700 border-b align-top">DB Trigger</th>
								<th class="px-3 py-2 text-left font-medium text-gray-700 border-b align-top">Edge Function</th>
							</tr>
						</thead>
						<tbody class="text-xs">
							<tr>
								<td class="px-3 py-2 border-b font-medium text-gray-700 align-top">What it is</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">A schedule that runs SQL or an RPC at a cron expression</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">A reusable Postgres function (SQL/PL-pgSQL) you call by name</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">A Postgres function bound to a row event on a table</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Serverless TS/JS in a Deno container</td>
							</tr>
							<tr class="bg-gray-50/40">
								<td class="px-3 py-2 border-b font-medium text-gray-700 align-top">Where it runs</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Inside Postgres (cron worker fires the SQL/RPC)</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Inside Postgres</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Inside Postgres, in the row's transaction</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Deno container, outside the database</td>
							</tr>
							<tr>
								<td class="px-3 py-2 border-b font-medium text-gray-700 align-top">How it's invoked</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">By the cron schedule (no caller)</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top"><code class="text-[11px]">eb.db.rpc('name')</code> from the SDK, or from a cron job</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Automatically, when an INSERT / UPDATE / DELETE / TRUNCATE happens on the attached table</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top"><code class="text-[11px]">eb.functions.invoke('name')</code> or HTTP</td>
							</tr>
							<tr class="bg-gray-50/40">
								<td class="px-3 py-2 border-b font-medium text-gray-700 align-top">Language</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">SQL (or pick an RPC)</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">SQL or PL/pgSQL</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">PL/pgSQL (return type <code class="text-[11px]">trigger</code>)</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">TypeScript / JavaScript</td>
							</tr>
							<tr>
								<td class="px-3 py-2 border-b font-medium text-gray-700 align-top">Transactional</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Yes — its own transaction at run time</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Yes — runs in the caller's transaction</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Yes — runs in the row operation's transaction (can roll it back by raising)</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">No — separate process</td>
							</tr>
							<tr class="bg-gray-50/40">
								<td class="px-3 py-2 border-b font-medium text-gray-700 align-top">Where to manage it</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Cron &amp; RPC &rarr; Scheduled Jobs</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Cron &amp; RPC &rarr; Functions</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Function: Cron &amp; RPC &rarr; Functions (Returns: trigger). Attachment: Database &rarr; table &rarr; Triggers panel</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Functions tab</td>
							</tr>
							<tr>
								<td class="px-3 py-2 border-b font-medium text-gray-700 align-top">Use case examples</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Daily cleanup of expired sessions; weekly digest aggregation; archiving old rows</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Computed leaderboard, multi-statement bulk update, an atomic check-and-decrement, complex aggregate the SDK can call by name</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Enforcing a max-N-rows-per-user constraint on INSERT; auto-stamping <code class="text-[11px]">updated_at</code>; mirroring inserts into an audit table</td>
								<td class="px-3 py-2 border-b text-gray-700 align-top">Stripe / Mollie webhook; sending email via TEM; calling an external image-processing API; OAuth callback handling</td>
							</tr>
							<tr class="bg-gray-50/40">
								<td class="px-3 py-2 font-medium text-gray-700 align-top">Pick when</td>
								<td class="px-3 py-2 text-gray-700 align-top">You want something to run on a schedule, regardless of user activity</td>
								<td class="px-3 py-2 text-gray-700 align-top">Your app needs to call a chunk of DB-only logic by name, atomically</td>
								<td class="px-3 py-2 text-gray-700 align-top">A row change must always be accompanied by side-effect SQL — and the side effect must succeed or fail with the row change</td>
								<td class="px-3 py-2 text-gray-700 align-top">You need the JS ecosystem, an external HTTP call, or anything Postgres can't reach from within the database</td>
							</tr>
						</tbody>
					</table>
				</div>
				<p class="text-sm text-gray-700 leading-relaxed">
					<strong>Mental model</strong>: Cron jobs are <em>scheduled SQL</em>; RPC is <em>callable SQL</em>; DB triggers are <em>reactive SQL</em>; edge functions are <em>everything else</em>. The first three all run inside Postgres and share its transactional model. Edge functions are a separate runtime that talks to the DB over the SDK like your app does.
				</p>
				<div class="rounded-lg border border-amber-200 bg-amber-50/60 px-4 py-3">
					<p class="text-sm text-amber-900"><strong>One subtle gotcha</strong>: a function that <code class="rounded bg-white border border-amber-200 px-1 text-xs">RETURNS trigger</code> is created in the same place as RPC functions, but it won't appear in the RPC list and can't be called via <code class="rounded bg-white border border-amber-200 px-1 text-xs">eb.db.rpc()</code>. It only exists to be attached to a table by a trigger. The Functions list filters it out so the surfaces stay honest. To attach it: Database tab &rarr; pick the table &rarr; expand the <strong>Triggers</strong> panel.</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Creating a Function</h3>
				<p class="text-sm text-gray-700">From the console, navigate to <span class="font-mono text-sm bg-gray-100 px-1 rounded">Functions</span> tab and click <span class="font-mono text-sm bg-gray-100 px-1 rounded">+ New Function</span>. Give it a lowercase name with hyphens (e.g., <code>process-order</code>).</p>
				<p class="text-sm text-gray-700 mt-2">Or via CLI:</p>
				<pre class="rounded-lg bg-gray-900 px-4 py-3 text-sm text-green-400 font-mono overflow-x-auto">eurobase edge-functions deploy process-order --file functions/process-order.ts</pre>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Function Structure</h3>
				<p class="text-sm text-gray-700">Write TypeScript or JavaScript and export your handler with <code>export default</code> (or <code>module.exports</code>). The runner passes <code>req</code> (a <code>Request</code>) and <code>ctx</code> (context). Code is compiled on deploy — types are stripped, not checked.</p>
				<p class="text-sm text-amber-700 bg-amber-50 border-l-4 border-amber-400 px-3 py-2 mt-2 rounded"><strong>Heads-up:</strong> third-party imports (<code>import … from "https://…"</code> or npm packages) are not supported yet — inline dependencies into the function file.</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Reading the request (webhooks & APIs)</h3>
				<p class="text-sm text-gray-700">Your function receives the full incoming request, so it can act as a webhook receiver or small HTTP API:</p>
				<ul class="list-disc pl-5 text-sm text-gray-700 space-y-1 mt-2">
					<li><strong>Query string</strong> — <code>new URL(req.url).searchParams.get('token')</code></li>
					<li><strong>Custom headers</strong> — <code>req.headers.get('X-Signature')</code>, <code>X-Api-Key</code>, <code>X-Webhook-Id</code>, etc. are forwarded as sent.</li>
					<li><strong>Body</strong> — <code>await req.json()</code> / <code>req.text()</code>; <code>Content-Type</code> is preserved.</li>
				</ul>
				<p class="text-sm text-gray-700 mt-2">Withheld from your function so platform credentials don't leak: the auth that authenticates your call to Eurobase — the <code>Authorization</code>, <code>apikey</code>, <code>Cookie</code> headers <em>and</em> the <code>?apikey=</code> query param — plus Eurobase's internal <code>X-Eurobase-*</code> / <code>X-Project-*</code> / <code>X-Function-*</code> / <code>X-User-*</code> headers. For end-user identity on a <code>verify_jwt</code> function, use <code>ctx.user</code>. Put a partner's auth token in any other custom header (e.g. <code>X-Api-Key</code>, <code>X-Signature</code>) or query param (e.g. <code>?token=</code>).</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">TypeScript types (autocomplete + type-checking)</h3>
				<p class="text-sm text-gray-700">The full <code>req</code> and <code>ctx</code> shapes are shipped from the SDK as a types-only subpath. Import in your function source with a <code>type</code>-only import (bundlers elide it at build time):</p>
				<pre class="rounded-lg bg-gray-900 px-4 py-3 text-sm text-green-400 font-mono overflow-x-auto whitespace-pre">import type &#123; EdgeHandler &#125; from '@eurobase/sdk/functions'

const handler: EdgeHandler = async (req, ctx) =&gt; &#123;
  const &#123; orderId &#125; = (await req.json()) as &#123; orderId: string &#125;
  const rows = await ctx.db.sql&lt;&#123; id: string; total: number &#125;&gt;(
    "SELECT id, total FROM orders WHERE id = $1",
    [orderId],
  )
  return Response.json(rows[0])
&#125;

export default handler</pre>
				<p class="text-sm text-gray-700 mt-2">Requires <code>@eurobase/sdk@0.7.0</code> or later. The exported types (<code>EdgeContext</code>, <code>EdgeHandler</code>, <code>EdgeUser</code>, <code>EdgeStorageUploadResult</code>, <code>EdgeSignedUrlResult</code>, <code>EdgeLogger</code>) mirror the runtime <code>ctx</code> the runner passes to your handler, so autocomplete and type-checking cover every helper. Note: <code>ctx.db.sql(...)</code> resolves to the bare row array — assign directly with <code>const rows = ...</code>, not destructured with <code>const &#123; rows &#125; = ...</code>.</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Example handler</h3>
				<pre class="rounded-lg bg-gray-900 px-4 py-3 text-sm text-green-400 font-mono overflow-x-auto whitespace-pre">module.exports = async (req, ctx) =&gt; {"{"}{"\n"}  // Parse the incoming request{"\n"}  const {"{"} orderId {"}"} = await req.json();{"\n"}{"\n"}  // Query the database (scoped to your project){"\n"}  // ctx.db.sql resolves to the rows array directly.{"\n"}  const rows = await ctx.db.sql({"\n"}    "SELECT * FROM orders WHERE id = $1",{"\n"}    [orderId]{"\n"}  );{"\n"}  const order = rows[0];{"\n"}{"\n"}  // Read a secret from Vault{"\n"}  const apiKey = await ctx.vault.get("PAYMENT_API_KEY");{"\n"}{"\n"}  // Call an external API{"\n"}  const payment = await fetch("https://api.mollie.com/v2/payments", {"{"}{"\n"}    method: "POST",{"\n"}    headers: {"{"} Authorization: `Bearer ${"{"} apiKey {"}"}` {"}"},{"\n"}    body: JSON.stringify({"{"} amount: order.total {"}"}){"\n"}  {"}"});{"\n"}{"\n"}  // Return a response{"\n"}  return new Response(JSON.stringify({"{"} status: "ok" {"}"}), {"{"}{"\n"}    status: 200,{"\n"}    headers: {"{"} "Content-Type": "application/json" {"}"},{"\n"}  {"}"});{"\n"}{"}"};</pre>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Context API</h3>
				<div class="overflow-x-auto">
					<table class="w-full text-sm border border-gray-200 rounded-lg">
						<thead class="bg-gray-50">
							<tr>
								<th class="px-4 py-2 text-left font-medium text-gray-700 border-b">Property</th>
								<th class="px-4 py-2 text-left font-medium text-gray-700 border-b">Description</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-gray-100">
							<tr><td class="px-4 py-2 font-mono text-xs">ctx.db.sql(query, params)</td><td class="px-4 py-2 text-gray-600">Execute SQL scoped to your project schema</td></tr>
							<tr><td class="px-4 py-2 font-mono text-xs">ctx.vault.get(name)</td><td class="px-4 py-2 text-gray-600">Read an encrypted secret from Vault</td></tr>
							<tr><td class="px-4 py-2 font-mono text-xs">ctx.env</td><td class="px-4 py-2 text-gray-600">Per-function environment variables</td></tr>
							<tr><td class="px-4 py-2 font-mono text-xs">ctx.user.id / ctx.user.email</td><td class="px-4 py-2 text-gray-600">Authenticated user (if JWT required)</td></tr>
							<tr><td class="px-4 py-2 font-mono text-xs">ctx.log.info(msg) / .warn / .error</td><td class="px-4 py-2 text-gray-600">Structured logging (visible in Logs)</td></tr>
						</tbody>
					</table>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Invoking Functions</h3>
				<p class="text-sm text-gray-700">Functions are invoked via HTTP using your API key:</p>
				<pre class="rounded-lg bg-gray-900 px-4 py-3 text-sm text-green-400 font-mono overflow-x-auto">POST https://your-project.eurobase.app/v1/functions/process-order{"\n"}Authorization: Bearer &lt;user-jwt&gt;{"\n"}apikey: eb_pk_...{"\n"}{"\n"}{"{"}"orderId": "abc-123"{"}"}</pre>

				<p class="text-sm text-gray-700 mt-2">Or via the SDK:</p>
				<pre class="rounded-lg bg-gray-900 px-4 py-3 text-sm text-green-400 font-mono overflow-x-auto">const {"{"} data, error {"}"} = await eurobase.functions.invoke('process-order', {"{"}{"\n"}  body: {"{"} orderId: 'abc-123' {"}"},{"\n"}{"}"});</pre>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">CLI Commands</h3>
				<pre class="rounded-lg bg-gray-900 px-4 py-3 text-sm text-green-400 font-mono overflow-x-auto"># List edge functions{"\n"}eurobase edge-functions list{"\n"}{"\n"}# Deploy from local file{"\n"}eurobase edge-functions deploy process-order -f functions/process-order.ts{"\n"}{"\n"}# View execution logs{"\n"}eurobase edge-functions logs process-order{"\n"}{"\n"}# Delete a function{"\n"}eurobase edge-functions delete process-order</pre>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Plan Limits</h3>
				<div class="overflow-x-auto">
					<table class="w-full text-sm border border-gray-200 rounded-lg">
						<thead class="bg-gray-50">
							<tr>
								<th class="px-4 py-2 text-left font-medium text-gray-700 border-b">Limit</th>
								<th class="px-4 py-2 text-left font-medium text-gray-700 border-b">Free</th>
								<th class="px-4 py-2 text-left font-medium text-gray-700 border-b">Pro</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-gray-100">
							<tr><td class="px-4 py-2 text-gray-700">Functions per project</td><td class="px-4 py-2">3</td><td class="px-4 py-2">25</td></tr>
							<tr><td class="px-4 py-2 text-gray-700">Execution timeout</td><td class="px-4 py-2">10 seconds</td><td class="px-4 py-2">60 seconds</td></tr>
							<tr><td class="px-4 py-2 text-gray-700">Memory per execution</td><td class="px-4 py-2">64 MB</td><td class="px-4 py-2">256 MB</td></tr>
						</tbody>
					</table>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Use Cases</h3>
				<ul class="list-disc pl-5 text-sm text-gray-700 space-y-1">
					<li><strong>Payment webhooks</strong> — Process Mollie callbacks, update order status</li>
					<li><strong>External integrations</strong> — Sync data to/from other EU SaaS</li>
					<li><strong>Custom auth logic</strong> — Post-signup hooks, role assignment</li>
					<li><strong>Data transformation</strong> — Parse CSVs, enrich records, generate reports</li>
					<li><strong>Notifications</strong> — Send emails, push notifications on events</li>
				</ul>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('logs')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Monitoring with Logs &rarr;
				</button>
			</div>
		</section>
