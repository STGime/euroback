<script lang="ts">
	// Yellow banner rendered on billing surfaces while the gateway
	// is running with MOLLIE_ENV=test. Signals to real users that
	// any subscription they start here is a rehearsal against
	// Mollie's test API — no card is charged.
	//
	// Fetches GET /platform/billing/config on mount rather than
	// taking props so it stays self-contained; every billing page
	// can just drop it in without threading state. Silent when
	// billing is off or when mode is anything other than "test"
	// (fail-safe: never claim test mode when we don't know).
	import { api } from '$lib/api.js';
	import { onMount } from 'svelte';

	let mode: 'test' | 'live' | '' = $state('');
	let ready = $state(false);

	onMount(async () => {
		try {
			const cfg = await api.getBillingConfig();
			mode = cfg.enabled ? cfg.mode : '';
		} catch {
			// Config probe failure shouldn't block the page. Leaving
			// mode="" hides the banner — better than a red error
			// bar on every billing page load if the endpoint is
			// briefly unavailable.
		} finally {
			ready = true;
		}
	});
</script>

{#if ready && mode === 'test'}
	<div class="mb-6 rounded-md border border-yellow-300 bg-yellow-50 p-4">
		<p class="text-sm font-medium text-yellow-900">
			⚠ Payments are in <strong>test mode</strong>. Any subscription started
			here is a rehearsal — no card is charged.
		</p>
		<p class="mt-1 text-xs text-yellow-800">
			We're validating the payment flow before opening real billing.
		</p>
	</div>
{/if}
