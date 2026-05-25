<script lang="ts">
	import { page } from '$app/stores';
	import { api, type SubscriptionView, type Invoice } from '$lib/api.js';
	import { onMount } from 'svelte';

	let projectId = $derived($page.params.id);

	let view: SubscriptionView | null = $state(null);
	let invoices: Invoice[] = $state([]);
	let loading = $state(true);
	let error: string | null = $state(null);
	let busy = $state(false);

	let showCancelModal = $state(false);

	onMount(async () => {
		await load();
		// Honour ?autostart=1 — coming from the pricing page or the
		// project-creation flow (#70) where the user already declared
		// intent. One-click into the Mollie checkout, no extra
		// confirmation needed.
		const params = new URLSearchParams(window.location.search);
		if (params.get('autostart') === '1' && view?.plan === 'free') {
			await startCheckout();
		}
	});

	async function load() {
		loading = true;
		error = null;
		try {
			view = await api.getSubscription(projectId);
			const inv = await api.listInvoices(projectId);
			invoices = inv.invoices;
		} catch (e: any) {
			error = e?.message ?? 'Failed to load billing state';
		} finally {
			loading = false;
		}
	}

	async function startCheckout() {
		busy = true;
		error = null;
		try {
			const { checkout_url } = await api.startCheckout(projectId, 'pro');
			window.location.href = checkout_url;
		} catch (e: any) {
			error = e?.message ?? 'Failed to start checkout';
		} finally {
			busy = false;
		}
	}

	async function confirmCancel() {
		busy = true;
		error = null;
		try {
			await api.cancelSubscription(projectId);
			showCancelModal = false;
			await load();
		} catch (e: any) {
			error = e?.message ?? 'Cancel failed';
		} finally {
			busy = false;
		}
	}

	function formatCents(cents: number): string {
		return `€${(cents / 100).toFixed(2)}`;
	}

	function formatDate(s: string | undefined): string {
		if (!s) return '—';
		return new Date(s).toLocaleDateString('en-GB', { year: 'numeric', month: 'short', day: 'numeric' });
	}

	function statusLabel(status: SubscriptionView['status']): string {
		switch (status) {
			case 'active': return 'Active';
			case 'pending_payment': return 'Pending payment';
			case 'grace': return 'Payment failed — grace period';
			case 'pro_until_period_end': return 'Cancelled — Pro until period end';
			case 'cancelled': return 'Cancelled';
			default: return 'Free';
		}
	}

	function statusBadgeClass(status: SubscriptionView['status']): string {
		switch (status) {
			case 'active': return 'bg-green-100 text-green-800';
			case 'pending_payment': return 'bg-yellow-100 text-yellow-800';
			case 'grace': return 'bg-red-100 text-red-800';
			case 'pro_until_period_end': return 'bg-blue-100 text-blue-800';
			case 'cancelled': return 'bg-gray-100 text-gray-800';
			default: return 'bg-gray-100 text-gray-700';
		}
	}
</script>

<div class="max-w-4xl mx-auto p-6 space-y-8">
	<header>
		<h1 class="text-2xl font-semibold text-gray-900">Billing</h1>
		<p class="text-sm text-gray-500 mt-1">
			Eurobase Pro — €19/month. Payments processed by Mollie (EU, Netherlands).
		</p>
	</header>

	{#if loading}
		<div class="text-gray-500">Loading…</div>
	{:else if error}
		<div class="rounded-md bg-red-50 border border-red-200 p-3 text-sm text-red-800">{error}</div>
	{:else if view}
		<!-- Subscription state card -->
		<section class="rounded-lg border border-gray-200 bg-white">
			<div class="p-5 border-b border-gray-100 flex items-center justify-between">
				<div>
					<div class="text-xs uppercase tracking-wide text-gray-500">Current plan</div>
					<div class="text-xl font-semibold text-gray-900 mt-1">
						{view.plan === 'pro' ? 'Pro' : 'Free'}
					</div>
				</div>
				<span class="text-xs px-2 py-1 rounded {statusBadgeClass(view.status)}">
					{statusLabel(view.status)}
				</span>
			</div>

			<div class="p-5 grid grid-cols-2 gap-4 text-sm">
				{#if view.amount_eur}
					<div>
						<div class="text-gray-500">Monthly amount</div>
						<div class="text-gray-900 font-medium">€{view.amount_eur}</div>
					</div>
				{/if}
				{#if view.current_period_end}
					<div>
						<div class="text-gray-500">
							{view.cancel_at_period_end ? 'Access until' : 'Next charge'}
						</div>
						<div class="text-gray-900 font-medium">{formatDate(view.current_period_end)}</div>
					</div>
				{/if}
				{#if view.grace_until}
					<div>
						<div class="text-gray-500">Grace period ends</div>
						<div class="text-red-700 font-medium">{formatDate(view.grace_until)}</div>
					</div>
				{/if}
			</div>

			<div class="p-5 border-t border-gray-100 flex flex-wrap gap-2">
				{#if view.plan === 'free' || view.status === 'cancelled'}
					<button
						type="button"
						class="rounded-md bg-blue-600 px-3 py-2 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
						disabled={busy}
						onclick={startCheckout}
					>
						{busy ? 'Starting checkout…' : 'Upgrade to Pro'}
					</button>
				{:else if view.status === 'grace' || view.status === 'pending_payment'}
					<button
						type="button"
						class="rounded-md bg-blue-600 px-3 py-2 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
						disabled={busy}
						onclick={startCheckout}
					>
						{busy ? 'Starting checkout…' : 'Update payment method'}
					</button>
				{/if}

				{#if (view.status === 'active' || view.status === 'grace') && !view.cancel_at_period_end}
					<button
						type="button"
						class="rounded-md border border-gray-300 px-3 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"
						onclick={() => (showCancelModal = true)}
					>
						Cancel subscription
					</button>
				{/if}
			</div>
		</section>

		<!-- Invoices -->
		<section class="rounded-lg border border-gray-200 bg-white">
			<div class="p-5 border-b border-gray-100">
				<h2 class="text-lg font-semibold text-gray-900">Invoices</h2>
				<p class="text-xs text-gray-500 mt-1">Latest 20 charges. PDFs are hosted by Mollie.</p>
			</div>
			{#if invoices.length === 0}
				<div class="p-5 text-sm text-gray-500">No invoices yet.</div>
			{:else}
				<table class="w-full text-sm">
					<thead class="bg-gray-50 text-xs uppercase text-gray-500">
						<tr>
							<th class="text-left px-4 py-2 font-medium">Date</th>
							<th class="text-left px-4 py-2 font-medium">Amount</th>
							<th class="text-left px-4 py-2 font-medium">Status</th>
							<th class="text-left px-4 py-2 font-medium">Mollie ID</th>
						</tr>
					</thead>
					<tbody>
						{#each invoices as inv}
							<tr class="border-t border-gray-100">
								<td class="px-4 py-2">{formatDate(inv.paid_at || inv.created_at)}</td>
								<td class="px-4 py-2">{formatCents(inv.amount_cents)} {inv.currency}</td>
								<td class="px-4 py-2">
									<span class="text-xs px-2 py-0.5 rounded {inv.status === 'paid' ? 'bg-green-100 text-green-800' : inv.status === 'failed' ? 'bg-red-100 text-red-800' : 'bg-gray-100 text-gray-700'}">
										{inv.status}
									</span>
								</td>
								<td class="px-4 py-2 font-mono text-xs text-gray-500">{inv.mollie_payment_id}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			{/if}
		</section>
	{/if}

	{#if showCancelModal}
		<div class="fixed inset-0 bg-black/30 flex items-center justify-center z-50">
			<div class="bg-white rounded-lg max-w-md w-full p-6 space-y-4">
				<h3 class="text-lg font-semibold text-gray-900">Cancel Pro subscription?</h3>
				<p class="text-sm text-gray-600">
					Pro features stay active until {formatDate(view?.current_period_end)}.
					Mollie will not charge you again. You can resubscribe at any time.
				</p>
				<div class="flex gap-2 justify-end">
					<button
						type="button"
						class="rounded-md border border-gray-300 px-3 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"
						onclick={() => (showCancelModal = false)}
					>Keep subscription</button>
					<button
						type="button"
						class="rounded-md bg-red-600 px-3 py-2 text-sm font-medium text-white hover:bg-red-700 disabled:opacity-50"
						disabled={busy}
						onclick={confirmCancel}
					>{busy ? 'Cancelling…' : 'Confirm cancel'}</button>
				</div>
			</div>
		</div>
	{/if}
</div>
