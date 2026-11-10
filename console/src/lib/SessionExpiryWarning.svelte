<script lang="ts">
	// Warns before the console session (24 h) runs out, and once it has,
	// so the next click doesn't silently land on the sign-in page.
	import { user } from '$lib/stores.js';
	import { loginURLReturningHere } from '$lib/api';
	import { sessionExpiresAt } from '$lib/session';

	const WARN_BEFORE_MS = 15 * 60 * 1000;

	let now = $state(Date.now());
	let expiresAt = $derived(sessionExpiresAt($user?.token));
	let remaining = $derived(expiresAt === null ? null : expiresAt - now);
	let minutes = $derived(remaining === null ? 0 : Math.max(1, Math.ceil(remaining / 60000)));

	$effect(() => {
		const t = setInterval(() => (now = Date.now()), 15000);
		return () => clearInterval(t);
	});

	function signInAgain() {
		window.location.href = loginURLReturningHere();
	}
</script>

{#if remaining !== null && remaining <= WARN_BEFORE_MS}
	<div
		role="status"
		class="fixed bottom-4 right-4 left-4 sm:left-auto sm:max-w-md z-[100] flex flex-wrap items-center gap-3 rounded-lg border px-4 py-3 text-sm shadow-lg {remaining <= 0
			? 'border-red-200 bg-red-50 text-red-800'
			: 'border-amber-200 bg-amber-50 text-amber-900'}"
	>
		<span class="flex-1 min-w-0">
			{#if remaining <= 0}
				Your session has expired. Sign in again to continue — forms you were filling in are kept where possible.
			{:else}
				Your session expires in {minutes} minute{minutes === 1 ? '' : 's'}. Sign in again now to avoid interrupting your work.
			{/if}
		</span>
		<button
			type="button"
			class="cursor-pointer rounded-lg bg-white px-3 py-1.5 text-sm font-medium border border-current/20 hover:bg-gray-50"
			onclick={signInAgain}
		>
			Sign in again
		</button>
	</div>
{/if}
