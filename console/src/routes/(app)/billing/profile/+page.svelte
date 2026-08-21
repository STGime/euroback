<script lang="ts">
	// Standalone billing-profile editor. Reached in two ways:
	//   1. Nav link from /billing (once we add one — TODO).
	//   2. Auto-redirect from /p/[id]/billing when the user
	//      clicks Upgrade to Pro without a profile; the target
	//      page attaches ?next=/p/{id}/billing so we can bounce
	//      them straight back to the checkout button.
	//   3. Auto-redirect from /projects when the user tries to
	//      create a paid Pro project without a profile.
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { api, type BillingProfile } from '$lib/api.js';
	import { onMount } from 'svelte';
	import BillingProfileForm from '$lib/BillingProfileForm.svelte';

	let profile: BillingProfile | null = $state(null);
	let loading = $state(true);
	let error: string | null = $state(null);

	// `next` is the URL to send the user to after save. Only
	// same-origin paths starting with '/' are accepted so a
	// crafted ?next=https://evil.example can't turn the profile
	// form into an open redirect.
	let nextTarget = $derived.by(() => {
		const raw = $page.url.searchParams.get('next');
		if (!raw) return '/billing';
		if (!raw.startsWith('/') || raw.startsWith('//')) return '/billing';
		return raw;
	});

	onMount(async () => {
		loading = true;
		try {
			profile = await api.getBillingProfile();
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		} finally {
			loading = false;
		}
	});

	function onSaved(saved: BillingProfile): void {
		profile = saved;
		// Full-page navigation so the destination's onMount refetches
		// state (some pages cache getBillingProfile via _billingConfigCache-
		// style patterns — safer to reload than to trust every page
		// reads fresh).
		void goto(nextTarget, { invalidateAll: true });
	}
</script>

<svelte:head>
	<title>Billing details — Eurobase</title>
</svelte:head>

<div class="mx-auto max-w-2xl px-4 py-8 sm:px-6 lg:px-8">
	<div class="mb-6">
		<h1 class="text-2xl font-bold text-gray-900">Billing details</h1>
		<p class="mt-2 text-sm text-gray-600">
			Used on every invoice you receive. Required by Estonian VAT
			Act §37 — your accountant will thank you.
		</p>
	</div>

	{#if loading}
		<p class="text-sm text-gray-500">Loading…</p>
	{:else if error}
		<div class="rounded-md border border-red-200 bg-red-50 p-4">
			<p class="text-sm text-red-800">{error}</p>
		</div>
	{:else}
		<div class="rounded-lg border border-gray-200 bg-white p-6">
			<BillingProfileForm
				existing={profile}
				saveLabel={profile ? 'Save changes' : 'Save and continue'}
				{onSaved}
			/>
		</div>
	{/if}
</div>
