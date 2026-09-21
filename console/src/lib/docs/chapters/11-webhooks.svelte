<script lang="ts">
	import { goto } from '$app/navigation';
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
	// In the single-page docs this scrolled; each chapter is now its own
	// URL, so cross-references navigate instead.
	function scrollTo(id: string) {
		goto(`/docs/${id}`);
	}
</script>

		<section id="webhooks" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">11. Webhooks</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex wants LexVault to be notified whenever a new client record is created.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					Webhooks let your application receive real-time HTTP callbacks when events happen in your Eurobase project &mdash; database changes, user signups, file uploads, and more.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Setting up a webhook</h3>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">1</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Create a webhook</p>
						<p class="text-sm text-gray-600">Go to the Webhooks page and click "Create Webhook". Enter a name and your endpoint URL.</p>
					</div>
				</div>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">2</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Select events</p>
						<p class="text-sm text-gray-600">Choose which events trigger the webhook: <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">db.insert</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">db.update</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">db.delete</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth.signup</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">auth.signin</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">storage.upload</code>, and more. You can also filter by table name.</p>
					</div>
				</div>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">3</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Copy the signing secret</p>
						<p class="text-sm text-gray-600">Each webhook gets a signing secret. Use it to verify that incoming requests genuinely come from Eurobase.</p>
					</div>
				</div>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">4</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Monitor delivery history</p>
						<p class="text-sm text-gray-600">The webhook detail page shows every delivery attempt with status code, response time, and payload. Failed deliveries are retried automatically.</p>
					</div>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-6">Verifying signatures (Node.js)</h3>

				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto">
					<button
						onclick={() => copyCode("import crypto from 'crypto'\n\nfunction verifyWebhook(payload, signature, secret) {\n  const expected = crypto\n    .createHmac('sha256', secret)\n    .update(payload)\n    .digest('hex')\n  return crypto.timingSafeEqual(\n    Buffer.from(signature),\n    Buffer.from(expected)\n  )\n}\n\n// In your Express handler:\napp.post('/webhooks/eurobase', express.raw({ type: 'application/json' }), (req, res) => {\n  const sig = req.headers['x-eurobase-signature']\n  if (!verifyWebhook(req.body, sig, process.env.WEBHOOK_SECRET)) {\n    return res.status(401).send('Invalid signature')\n  }\n  const event = JSON.parse(req.body)\n  console.log('Received:', event.type, event.data)\n  res.sendStatus(200)\n})", 'webhook-verify')}
						class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer"
					>
						{copiedId === 'webhook-verify' ? 'Copied!' : 'Copy'}
					</button>
					<pre>import crypto from 'crypto'

function verifyWebhook(payload, signature, secret) {'{'}\
  const expected = crypto
    .createHmac('sha256', secret)
    .update(payload)
    .digest('hex')
  return crypto.timingSafeEqual(
    Buffer.from(signature),
    Buffer.from(expected)
  )
{'}'}

// In your Express handler:
app.post('/webhooks/eurobase', express.raw({'{'} type: 'application/json' {'}'}), (req, res) => {'{'}\
  const sig = req.headers['x-eurobase-signature']
  if (!verifyWebhook(req.body, sig, process.env.WEBHOOK_SECRET)) {'{'}\
    return res.status(401).send('Invalid signature')
  {'}'}
  const event = JSON.parse(req.body)
  console.log('Received:', event.type, event.data)
  res.sendStatus(200)
{'}'})</pre>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('rls')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Row-Level Security &rarr;
				</button>
			</div>
		</section>
