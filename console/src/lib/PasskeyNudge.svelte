<script lang="ts">
	// One-time "protect your account with a passkey" banner (#621).
	// The login page sets sessionStorage.eb_passkey_nudge after a
	// password-only sign-in (which means the account has no passkey —
	// with one, the password alone mints no session). "Not now" hides
	// it for this session; "Don't ask again" persists in localStorage.
	import { onMount } from 'svelte';
	import { api } from '$lib/api.js';

	let visible = $state(false);

	onMount(async () => {
		let wanted = false;
		try {
			wanted =
				sessionStorage.getItem('eb_passkey_nudge') === '1' &&
				localStorage.getItem('eb_passkey_nudge_dismissed') !== '1';
		} catch {
			wanted = false;
		}
		if (!wanted) return;
		// Only nudge when the server actually has passkeys enabled (a 503
		// here means it doesn't) and the account still has none.
		try {
			const res = await api.listPasskeys();
			visible = res.passkeys.length === 0;
		} catch {
			visible = false;
		}
	});

	function hide(forever: boolean) {
		visible = false;
		try {
			sessionStorage.removeItem('eb_passkey_nudge');
			if (forever) localStorage.setItem('eb_passkey_nudge_dismissed', '1');
		} catch { /* storage blocked */ }
	}
</script>

{#if visible}
	<div class="mb-6 flex flex-wrap items-center gap-3 rounded-xl border border-eurobase-200 bg-eurobase-50 px-4 py-3 text-sm text-eurobase-900">
		<svg class="h-5 w-5 shrink-0 text-eurobase-600" fill="none" viewBox="0 0 24 24" stroke-width="1.75" stroke="currentColor">
			<path stroke-linecap="round" stroke-linejoin="round" d="M15.75 5.25a3 3 0 0 1 3 3m3 0a6 6 0 0 1-7.029 5.912c-.563-.097-1.159.026-1.563.43L10.5 17.25H8.25v2.25H6v2.25H2.25v-2.818c0-.597.237-1.17.659-1.591l6.499-6.499c.404-.404.527-1 .43-1.563A6 6 0 1 1 21.75 8.25Z" />
		</svg>
		<p class="flex-1 min-w-[12rem]"><span class="font-medium">Protect your account with a passkey.</span> Turn on multi-factor authentication in seconds — free on every plan.</p>
		<a href="/account#passkeys" onclick={() => hide(false)} class="rounded-lg bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700">Add a passkey</a>
		<button type="button" onclick={() => hide(false)} class="rounded-lg px-2 py-1.5 text-xs text-eurobase-700 hover:bg-eurobase-100 cursor-pointer">Not now</button>
		<button type="button" onclick={() => hide(true)} class="rounded-lg px-2 py-1.5 text-xs text-eurobase-700 hover:bg-eurobase-100 cursor-pointer">Don't ask again</button>
	</div>
{/if}
