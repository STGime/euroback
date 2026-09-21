<script lang="ts">
	import { goto } from '$app/navigation';
	// In the single-page docs this scrolled; each chapter is now its own
	// URL, so cross-references navigate instead.
	function scrollTo(id: string) {
		goto(`/docs/${id}`);
	}
</script>

		<section id="account" class="scroll-mt-20">
			<h2 class="text-2xl font-bold text-gray-900 mb-1">24. Your Account</h2>
			<p class="text-sm italic text-gray-500 mb-4">Alex wants to set a display name and update their password.</p>

			<div class="space-y-4">
				<p class="text-sm text-gray-700 leading-relaxed">
					The Account page (accessible from the main sidebar) manages your <strong>platform account</strong> &mdash; the one you use to sign into the Eurobase console itself.
				</p>

				<h3 class="text-lg font-semibold text-gray-900">Profile information</h3>
				<ul class="text-sm text-gray-700 space-y-1.5 ml-4 list-disc">
					<li><strong>Email</strong> &mdash; your login email (read-only)</li>
					<li><strong>Display name</strong> &mdash; set a friendly name that appears in the console header</li>
					<li><strong>Member since</strong> &mdash; your account creation date</li>
				</ul>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Change password</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Enter your current password, then your new password twice. The new password must be at least 8 characters.
				</p>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Personal Access Tokens</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					Long-lived bearer tokens for tooling that needs to act as you outside the browser &mdash; the <button onclick={() => scrollTo('mcp')} class="text-eurobase-600 hover:underline cursor-pointer">MCP server</button>, the CLI, CI pipelines, scripts. Mint and revoke them in the <strong>Personal Access Tokens</strong> card on the Account page.
				</p>
				<p class="text-sm text-gray-700 leading-relaxed">
					<strong>Creating a token:</strong> click <em>+ New token</em>, give it a memorable name (e.g. <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono">my laptop</code>, <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono">ci-prod</code>), optionally set an expiry date, and click <em>Create</em>. The plaintext token (e.g. <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono">eb_pat_205d71172180a3ca3ea0e26f07156429</code>) appears in an amber banner once. Copy it immediately into a password manager or your shell rc &mdash; <strong>it is not retrievable afterwards.</strong> The console only stores a SHA-256 hash.
				</p>
				<p class="text-sm text-gray-700 leading-relaxed">
					<strong>What a PAT can do</strong>: read and write any project you own or are a member of, via the SDK, the platform API, or the MCP server. It authenticates as you across every project, with the same RLS and role checks the rest of the platform enforces.
				</p>
				<p class="text-sm text-gray-700 leading-relaxed">
					<strong>What a PAT can't do</strong>:
				</p>
				<ul class="text-sm text-gray-700 space-y-1 ml-4 list-disc">
					<li>Reach the platform admin surface (allowlist management, cross-tenant project list). PATs never carry the <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono">is_superadmin</code> claim, even when minted by a superadmin.</li>
					<li>Mint other PATs &mdash; only an authenticated browser session can do that. Limits the blast radius of a leaked token.</li>
					<li>Change your password or delete your account.</li>
				</ul>
				<p class="text-sm text-gray-700 leading-relaxed">
					<strong>Revoking</strong>: click <em>Revoke</em> next to any token. Revocation is immediate &mdash; the next request using that token returns 401. There's no grace period.
				</p>
				<div class="rounded-lg border border-eurobase-200 bg-eurobase-50/50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-eurobase-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="m11.25 11.25.041-.02a.75.75 0 0 1 1.063.852l-.708 2.836a.75.75 0 0 0 1.063.853l.041-.021M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9-3.75h.008v.008H12V8.25Z" />
					</svg>
					<p class="text-sm text-eurobase-800">
						Treat PATs like passwords. If a token leaks (commit, screenshot, screen-share), revoke and rotate it. Tokens listed on this page show their last-used timestamp so you can spot inactive ones to clean up.
					</p>
				</div>

				<h3 class="text-lg font-semibold text-gray-900 mt-4">Delete account</h3>
				<p class="text-sm text-gray-700 leading-relaxed">
					At the bottom of the page, you can permanently delete your platform account. You'll need to type your email address to confirm. This also deletes all projects owned by the account.
				</p>

				<div class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 flex gap-3">
					<svg class="h-5 w-5 text-amber-600 shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
					</svg>
					<p class="text-sm text-amber-800">
						<strong>Account deletion is permanent.</strong> All projects, databases, files, and user data under this account will be irreversibly destroyed.
					</p>
				</div>
			</div>

			<div class="mt-6 text-right">
				<button onclick={() => scrollTo('connect-db')} class="text-sm text-eurobase-600 hover:text-eurobase-700 font-medium cursor-pointer">
					Next: Direct Postgres connection &rarr;
				</button>
			</div>
		</section>
