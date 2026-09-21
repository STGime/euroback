<script lang="ts">
	let copiedId = $state('');
	async function copyCode(code: string, id: string) {
		try {
			await navigator.clipboard.writeText(code);
			copiedId = id;
			setTimeout(() => { if (copiedId === id) copiedId = ''; }, 1500);
		} catch {
			// clipboard unavailable — silently ignore
		}
	}
</script>

		<section id="api" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">10. Exploring the API</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex wants to see every API endpoint available for the LexVault tables.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					The API page auto-generates a REST endpoint reference for every table in your project. For each table you get endpoints for listing, creating, reading, updating, and deleting records.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">What you'll find</h3>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>Endpoint list</strong> &mdash; every table gets <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">GET</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">POST</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">PATCH</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">DELETE</code> endpoints</li>
					<li><strong>Try-it panel</strong> &mdash; test endpoints directly from the console with a built-in request builder</li>
					<li><strong>Query parameters</strong> &mdash; filter with <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">eq</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">neq</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">gt</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">lt</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">like</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">order</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">limit</code>, and <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">offset</code></li>
					<li><strong>Code snippets</strong> &mdash; auto-generated cURL and SDK examples for every endpoint</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">cURL examples</h3>

				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto">
					<button
						onclick={() => copyCode('# List all clients\ncurl https://lexvault.eurobase.app/api/v1/db/clients \\\n  -H "Authorization: Bearer $EUROBASE_SECRET_KEY"\n\n# Create a client\ncurl -X POST https://lexvault.eurobase.app/api/v1/db/clients \\\n  -H "Authorization: Bearer $EUROBASE_SECRET_KEY" \\\n  -H "Content-Type: application/json" \\\n  -d \'{"name": "Acme Legal", "email": "info@acmelegal.eu"}\'\n\n# Update a client\ncurl -X PATCH https://lexvault.eurobase.app/api/v1/db/clients?eq.email=info@acmelegal.eu \\\n  -H "Authorization: Bearer $EUROBASE_SECRET_KEY" \\\n  -H "Content-Type: application/json" \\\n  -d \'{"plan": "pro"}\'\n\n# Delete a client\ncurl -X DELETE https://lexvault.eurobase.app/api/v1/db/clients?eq.id=some-uuid \\\n  -H "Authorization: Bearer $EUROBASE_SECRET_KEY"', 'curl-crud')}
						class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer"
					>
						{copiedId === 'curl-crud' ? 'Copied!' : 'Copy'}
					</button>
					<pre># List all clients
curl https://lexvault.eurobase.app/api/v1/db/clients \
  -H "Authorization: Bearer $EUROBASE_SECRET_KEY"

# Create a client
curl -X POST https://lexvault.eurobase.app/api/v1/db/clients \
  -H "Authorization: Bearer $EUROBASE_SECRET_KEY" \
  -H "Content-Type: application/json" \
  -d '{"{"}"name": "Acme Legal", "email": "info@acmelegal.eu"{"}"}'

# Update a client
curl -X PATCH https://lexvault.eurobase.app/api/v1/db/clients?eq.email=info@acmelegal.eu \
  -H "Authorization: Bearer $EUROBASE_SECRET_KEY" \
  -H "Content-Type: application/json" \
  -d '{"{"}"plan": "pro"{"}"}'

# Delete a client
curl -X DELETE https://lexvault.eurobase.app/api/v1/db/clients?eq.id=some-uuid \
  -H "Authorization: Bearer $EUROBASE_SECRET_KEY"</pre>
				</div>
			</div>

			<div class="mt-6 text-right">
				<a href="/docs/webhooks" class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Webhooks &rarr;
				</a>
			</div>
		</section>
