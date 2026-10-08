		<section id="logs" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">16. Monitoring with Logs</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex notices slow responses and wants to investigate API traffic.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					The Logs page gives you real-time visibility into every API request hitting your project. Use it to debug issues, monitor traffic, and understand usage patterns.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Stats cards</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					At the top of the page, you'll see summary statistics: total requests, average response time, error rate, and requests by status code.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Request log table</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Below the stats, a detailed table lists every request with:
				</p>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li>Timestamp</li>
					<li>HTTP method and path</li>
					<li>Status code (color-coded: green for 2xx, yellow for 4xx, red for 5xx)</li>
					<li>Response time in milliseconds</li>
					<li>Client IP and user agent</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Filtering</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Use the filter controls to narrow results by HTTP method, status code range, path pattern, or time window. This makes it easy to isolate specific issues &mdash; for example, show only <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">5xx</code> errors on the <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">/api/v1/db/clients</code> endpoint.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">SQL history</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					<strong>Logs &rarr; SQL history</strong> lists every SQL statement sent to the project through the platform: the SQL editor, the multi-statement endpoint, the MCP server and the CLI (anything using a personal access token), plus function bodies, custom RLS policies and migrations. Each entry shows who sent it, whether it came from a console session or a token, the statement, and the result:
				</p>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>Ran</strong> &mdash; with the row count and duration.</li>
					<li><strong>Failed</strong> &mdash; the database rejected it; the error is shown. In a multi-statement request, the statements before it are listed as failed too (rolled back).</li>
					<li><strong>Refused</strong> &mdash; the platform's checks stopped it before it reached the database, with the reason.</li>
					<li><strong>Not run</strong> &mdash; in a multi-statement request, skipped because another statement was refused or failed.</li>
				</ul>
				<p class="text-sm text-gray-700 leading-relaxed">
					Click an entry for the full statement (the first 8&nbsp;KB are stored, with its length and SHA-256). Statements can contain your data, so SQL history is visible to <strong>project admins</strong> only and kept for <strong>30 days</strong>. SQL your app runs through the SDK isn't listed here.
				</p>
			</div>

			<div class="mt-6 text-right">
				<a href="/docs/compliance" class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Compliance, Audit Log &amp; DSAR &rarr;
				</a>
			</div>
		</section>
