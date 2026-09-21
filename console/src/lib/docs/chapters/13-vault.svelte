<script lang="ts">
	import { goto } from '$app/navigation';
	// In the single-page docs this scrolled; each chapter is now its own
	// URL, so cross-references navigate instead.
	function scrollTo(id: string) {
		goto(`/docs/${id}`);
	}
</script>

		<section id="vault" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">13. Vault (Encrypted Secrets)</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex needs to store API keys for Mollie payments and Twilio SMS securely.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					Vault is Eurobase's built-in encrypted secrets storage. Store API keys, credentials, and sensitive configuration securely &mdash; encrypted with AES-256-GCM, accessible via the console, API, and SDK. All secrets stay in EU infrastructure.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Storing secrets</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Go to the <strong>Vault</strong> tab in your project. Click "New Secret", enter a name (e.g. <code class="bg-gray-100 rounded px-1">stripe_api_key</code>), the secret value, and an optional description. The value is encrypted before storage.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Accessing secrets from the SDK</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Secrets are only accessible with the <strong>secret API key</strong> (<code class="bg-gray-100 rounded px-1">eb_sk_</code>). The public key cannot read secrets &mdash; this prevents client-side exposure.
				</p>

				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto mt-2">
					<pre>// Server-side only (Node.js, backend)
const eb = createClient({'{'} url: '...', apiKey: 'eb_sk_...' {'}'})

// Read a secret
const {'{'} data: apiKey {'}'} = await eb.vault.get('stripe_api_key')
console.log(apiKey) // 'sk_live_...'

// Store a secret
await eb.vault.set('twilio_token', 'ACxxxxxxx', 'Twilio auth token')

// List all secret names (values not included)
const {'{'} data: secrets {'}'} = await eb.vault.list()
// [{'{'}name: 'stripe_api_key', description: '...'{'}'}]

// Delete a secret
await eb.vault.delete('old_key')</pre>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Common use cases</h3>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>Payment provider keys</strong> &mdash; Mollie, Stripe API keys for processing payments</li>
					<li><strong>Email/SMS credentials</strong> &mdash; SendGrid, Twilio tokens for notifications</li>
					<li><strong>External API keys</strong> &mdash; OpenAI, Google Maps, any third-party service</li>
					<li><strong>Database connection strings</strong> &mdash; credentials for external databases</li>
					<li><strong>Webhook signing secrets</strong> &mdash; verify incoming webhooks from external services</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">REST API</h3>
				<div class="rounded-lg border border-gray-200 overflow-hidden">
					<table class="w-full text-xs">
						<thead class="bg-gray-50">
							<tr>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Method</th>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Endpoint</th>
								<th class="px-3 py-2 text-left text-gray-600 font-semibold">Description</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-gray-100">
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">GET</td><td class="px-3 py-1.5 font-mono text-gray-600">/v1/vault</td><td class="px-3 py-1.5 text-gray-500">List secret names (no values)</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">GET</td><td class="px-3 py-1.5 font-mono text-gray-600">/v1/vault/:name</td><td class="px-3 py-1.5 text-gray-500">Get decrypted value</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">POST</td><td class="px-3 py-1.5 font-mono text-gray-600">/v1/vault</td><td class="px-3 py-1.5 text-gray-500">Create secret</td></tr>
							<tr><td class="px-3 py-1.5 text-gray-700 font-mono">DELETE</td><td class="px-3 py-1.5 font-mono text-gray-600">/v1/vault/:name</td><td class="px-3 py-1.5 text-gray-500">Delete secret</td></tr>
						</tbody>
					</table>
				</div>
				<p class="text-xs text-gray-400 mt-1">All vault endpoints require the secret API key (<code class="bg-gray-100 rounded px-1">eb_sk_</code>). Public key access returns 403.</p>

				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3 mt-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<div class="text-sm text-eurobase-800">
						<p><strong>EU sovereignty:</strong> Secrets are encrypted with AES-256-GCM and stored in Scaleway PostgreSQL (France). The encryption key lives in your server environment &mdash; not in a US-based secrets manager. No Google Secret Manager, no AWS KMS, no HashiCorp Vault (US). Your credentials never leave the EU.</p>
					</div>
				</div>

				<div class="rounded-lg border border-gray-200 bg-gray-50 px-4 py-3 mt-3">
					<p class="text-xs font-semibold text-gray-700 mb-1">Plan limits</p>
					<p class="text-xs text-gray-600">Free: 5 secrets &middot; Pro: 100 secrets</p>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('cron')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Scheduled Jobs &rarr;
				</button>
			</div>
		</section>
