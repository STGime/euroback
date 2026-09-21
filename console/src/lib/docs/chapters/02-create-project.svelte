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

		<section id="create-project" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">2. Creating Your First Project</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex is signed in and ready to set up LexVault's backend.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					After signing in for the first time, you'll land on the onboarding wizard. Returning users can create additional projects from the <strong>Projects</strong> page.
				</p>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">1</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Name your project</p>
						<p class="text-sm text-gray-600">Alex types <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">LexVault</code>. The slug <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">lexvault</code> is generated automatically.</p>
					</div>
				</div>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">2</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Pick the owner <span class="text-xs font-normal text-gray-500">(org admins only)</span></p>
						<p class="text-sm text-gray-600">
							If you're an <strong>admin</strong> of any organization, an <strong>Owner</strong> dropdown appears between Project name and Region:
							pick your org (default) or <strong>Personal (not attached to any org)</strong>. Org-attached projects are visible to every
							org member; personal projects are yours alone. Not an admin of an org? The picker is hidden — projects land personal by default,
							same as before. See chapter 27 for how orgs work. Works on every plan — Free, Pro, Team, Legal Team — the Pro payment flow now threads your Owner choice through the webhook.
						</p>
					</div>
				</div>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">3</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Choose a region</p>
						<p class="text-sm text-gray-600">All regions are within the EU. Alex selects <strong>Paris (fr-par)</strong>. Your database, object storage, and compute are provisioned there.</p>
					</div>
				</div>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">4</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Configure authentication</p>
						<p class="text-sm text-gray-600">Toggle email/password auth on (default). Set password requirements and session duration.</p>
					</div>
				</div>

				<div class="flex items-start gap-3">
					<span class="flex h-7 w-7 items-center justify-center rounded-full bg-eurobase-100 text-xs font-bold text-eurobase-700 shrink-0">5</span>
					<div>
						<p class="text-sm font-medium text-gray-900">Get your API keys</p>
						<p class="text-sm text-gray-600">On completion, you receive a <strong>public key</strong> (safe for client-side) and a <strong>secret key</strong> (server-side only).</p>
					</div>
				</div>

				<div class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-amber-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
					</svg>
					<p class="text-sm text-amber-800">
						<strong>Save your secret key now.</strong> It is shown only once. If you lose it, you'll need to regenerate it from Project Settings.
					</p>
				</div>

				<!-- SDK init snippet -->
				<div class="relative rounded-lg bg-gray-900 p-4 text-xs font-mono text-green-400 overflow-x-auto">
					<button
						onclick={() => copyCode("import { createClient } from '@eurobase/sdk'\n\nconst eb = createClient({\n  url: 'https://lexvault.eurobase.app',\n  apiKey: process.env.EUROBASE_PUBLIC_KEY\n})", 'sdk-init')}
						class="absolute top-2 right-2 rounded bg-gray-700 px-2 py-1 text-[10px] text-gray-300 hover:bg-gray-600 cursor-pointer"
					>
						{copiedId === 'sdk-init' ? 'Copied!' : 'Copy'}
					</button>
					<pre>import {'{'} createClient {'}'} from '@eurobase/sdk'

const eb = createClient({'{'}
  url: 'https://lexvault.eurobase.app',
  apiKey: process.env.EUROBASE_PUBLIC_KEY
{'}'})</pre>
				</div>
			</div>

			<div class="mt-6 text-right">
				<a href="/docs/dashboard" class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: The Project Dashboard &rarr;
				</a>
			</div>
		</section>
