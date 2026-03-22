<script lang="ts">
	import { goto } from '$app/navigation';
	import { user, logout } from '$lib/stores.js';

	function handleSignOut() {
		logout();
		goto('/login');
	}
</script>

<div class="mx-auto max-w-xl space-y-6">
	<div>
		<h2 class="text-lg font-semibold text-gray-900">Account</h2>
		<p class="text-sm text-gray-500">Manage your Eurobase account.</p>
	</div>

	<!-- Email -->
	<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
		<div class="px-5 py-3 border-b border-gray-100">
			<h3 class="text-sm font-semibold text-gray-900">Email</h3>
		</div>
		<div class="px-5 py-4">
			<p class="text-sm font-mono text-gray-700">{$user?.email ?? '—'}</p>
		</div>
	</div>

	<!-- Sign out -->
	<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
		<div class="px-5 py-3 border-b border-gray-100">
			<h3 class="text-sm font-semibold text-gray-900">Session</h3>
		</div>
		<div class="px-5 py-4">
			<button
				type="button"
				class="cursor-pointer inline-flex items-center gap-2 rounded-lg bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700 transition-colors"
				onclick={handleSignOut}
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" d="M15.75 9V5.25A2.25 2.25 0 0 0 13.5 3h-6a2.25 2.25 0 0 0-2.25 2.25v13.5A2.25 2.25 0 0 0 7.5 21h6a2.25 2.25 0 0 0 2.25-2.25V15m3 0 3-3m0 0-3-3m3 3H9" />
				</svg>
				Sign Out
			</button>
		</div>
	</div>

	<!-- Future sections -->
	<div class="rounded-xl border border-dashed border-gray-200 bg-gray-50 overflow-hidden">
		<div class="px-5 py-4 text-center">
			<p class="text-xs text-gray-400">Change password and delete account coming soon.</p>
		</div>
	</div>
</div>
