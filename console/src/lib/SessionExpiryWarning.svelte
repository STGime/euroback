<script lang="ts">
	// Warns before the console session (24 h) runs out, and once it has,
	// so the next click doesn't silently land on the sign-in page.
	import { user } from '$lib/stores.js';
	import { loginURLReturningHere } from '$lib/api';
	import { sessionExpiresAt } from '$lib/session';

	const WARN_BEFORE_MS = 15 * 60 * 1000;

	let now = $state(Date.now());
	let dismissedFor = $state<number | null>(null);
	let expiresAt = $derived(sessionExpiresAt($user?.token));
	let remaining = $derived(expiresAt === null ? null : expiresAt - now);
	let expired = $derived(remaining !== null && remaining <= 0);
	let minutes = $derived(remaining === null ? 0 : Math.max(1, Math.ceil(remaining / 60000)));
	let visible = $derived(
		remaining !== null && remaining <= WARN_BEFORE_MS && (expired || dismissedFor !== expiresAt)
	);

	$effect(() => {
		const tick = () => (now = Date.now());
		const t = setInterval(tick, 15000);
		// Timers are throttled in hidden tabs and stop during sleep.
		document.addEventListener('visibilitychange', tick);
		return () => {
			clearInterval(t);
			document.removeEventListener('visibilitychange', tick);
		};
	});

	function signInAgain() {
		window.location.href = loginURLReturningHere();
	}
</script>

{#if visible}
	<div
		role={expired ? 'alert' : 'status'}
		class="fixed bottom-4 right-4 left-4 sm:left-auto sm:max-w-md z-[100] flex flex-wrap items-center gap-3 rounded-lg border px-4 py-3 text-sm shadow-lg {expired
			? 'border-red-200 bg-red-50 text-red-800'
			: 'border-amber-200 bg-amber-50 text-amber-900'}"
	>
		<span class="flex-1 min-w-0" aria-live="off">
			{#if expired}
				Your session has expired. Sign in again to continue.
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
		{#if !expired}
			<button
				type="button"
				class="cursor-pointer rounded-lg p-1 hover:bg-amber-100"
				aria-label="Dismiss"
				onclick={() => (dismissedFor = expiresAt)}
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" /></svg>
			</button>
		{/if}
	</div>
{/if}
