<script lang="ts">
	// Org-wide billing overview. Lists every invoice for every
	// project the caller owns. Per-project actions (upgrade,
	// cancel) live on /p/[id]/billing — this page is the
	// accountant's view.
	import { api, type Invoice, type Project } from '$lib/api.js';
	import { onMount } from 'svelte';
	import BillingTestModeBanner from '$lib/BillingTestModeBanner.svelte';

	let invoices: Invoice[] = $state([]);
	let projects: Project[] = $state([]);
	let loading = $state(true);
	let error: string | null = $state(null);

	onMount(async () => {
		loading = true;
		try {
			const [i, p] = await Promise.all([api.listInvoices(), api.listProjects()]);
			invoices = i.invoices;
			projects = p;
		} catch (err) {
			// A 503 billing_disabled is expected in non-billing
			// environments and shouldn't spook the user — render
			// the empty state with an explanation instead.
			error = err instanceof Error ? err.message : String(err);
		} finally {
			loading = false;
		}
	});

	// Aggregate "at a glance" numbers — cheap to compute
	// client-side against the invoices list, and the accountant
	// audience expects to see totals prominently.
	let paidThisYear = $derived(
		invoices
			.filter(
				(i) =>
					i.status === 'paid' &&
					i.paid_at &&
					new Date(i.paid_at).getFullYear() === new Date().getFullYear()
			)
			.reduce((sum, i) => sum + i.amount_cents, 0)
	);
	let outstanding = $derived(
		invoices.filter((i) => i.status === 'pending' || i.status === 'failed').length
	);
	let proProjects = $derived(projects.filter((p) => p.plan === 'pro').length);

	function formatEUR(cents: number, currency: string): string {
		const prefix = currency === 'EUR' ? '€' : `${currency} `;
		const abs = Math.abs(cents);
		const whole = Math.floor(abs / 100);
		const frac = abs % 100;
		const sign = cents < 0 ? '-' : '';
		return `${sign}${prefix}${whole}.${String(frac).padStart(2, '0')}`;
	}

	function statusBadgeClass(status: string): string {
		switch (status) {
			case 'paid':
				return 'bg-green-100 text-green-800';
			case 'failed':
				return 'bg-red-100 text-red-800';
			case 'refunded':
				return 'bg-gray-100 text-gray-800';
			default:
				return 'bg-yellow-100 text-yellow-800';
		}
	}
</script>

<svelte:head>
	<title>Billing — Eurobase</title>
</svelte:head>

<div class="mx-auto max-w-5xl px-4 py-8 sm:px-6 lg:px-8">
	<div class="mb-8">
		<h1 class="text-2xl font-bold text-gray-900">Billing</h1>
		<p class="mt-2 text-sm text-gray-600">
			Every invoice across every project you own. Per-project
			billing actions (upgrade, cancel) live on the project's
			billing tab.
		</p>
	</div>

	<BillingTestModeBanner />

	<!-- At-a-glance stats -->
	<div class="mb-8 grid grid-cols-1 gap-4 sm:grid-cols-3">
		<div class="rounded-lg border border-gray-200 bg-white p-4">
			<p class="text-sm text-gray-500">Paid this year</p>
			<p class="mt-1 text-2xl font-semibold text-gray-900">
				{formatEUR(paidThisYear, 'EUR')}
			</p>
		</div>
		<div class="rounded-lg border border-gray-200 bg-white p-4">
			<p class="text-sm text-gray-500">Outstanding invoices</p>
			<p class="mt-1 text-2xl font-semibold text-gray-900">{outstanding}</p>
		</div>
		<div class="rounded-lg border border-gray-200 bg-white p-4">
			<p class="text-sm text-gray-500">Pro projects</p>
			<p class="mt-1 text-2xl font-semibold text-gray-900">{proProjects}</p>
		</div>
	</div>

	{#if loading}
		<p class="text-sm text-gray-500">Loading invoices…</p>
	{:else if error}
		<div class="rounded-md border border-yellow-200 bg-yellow-50 p-4">
			<p class="text-sm text-yellow-800">
				Billing is not enabled yet in this environment. Your Free
				projects continue to work; paid Pro is coming soon.
			</p>
			<p class="mt-2 text-xs text-yellow-700">Reason: {error}</p>
		</div>
	{:else if invoices.length === 0}
		<div class="rounded-md border border-gray-200 bg-white p-8 text-center">
			<p class="text-sm text-gray-600">
				No invoices yet. When you upgrade a project to Pro, invoices
				will land here.
			</p>
		</div>
	{:else}
		<div class="overflow-hidden rounded-lg border border-gray-200 bg-white">
			<table class="min-w-full divide-y divide-gray-200">
				<thead class="bg-gray-50">
					<tr>
						<th class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500"
							>Invoice</th
						>
						<th class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500"
							>Project</th
						>
						<th class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500"
							>Date</th
						>
						<th class="px-4 py-3 text-right text-xs font-medium uppercase tracking-wider text-gray-500"
							>Amount</th
						>
						<th class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500"
							>Status</th
						>
						<th class="px-4 py-3 text-right text-xs font-medium uppercase tracking-wider text-gray-500"
							>PDF</th
						>
					</tr>
				</thead>
				<tbody class="divide-y divide-gray-200 bg-white">
					{#each invoices as inv (inv.id)}
						<tr>
							<td class="whitespace-nowrap px-4 py-3 font-mono text-sm text-gray-900">
								{inv.number}
							</td>
							<td class="whitespace-nowrap px-4 py-3 text-sm text-gray-900">
								<a href="/p/{inv.project_id}/billing" class="hover:underline">
									{inv.project_name}
								</a>
							</td>
							<td class="whitespace-nowrap px-4 py-3 text-sm text-gray-500">
								{new Date(inv.created_at).toLocaleDateString('en-GB', {
									year: 'numeric',
									month: 'short',
									day: 'numeric'
								})}
							</td>
							<td class="whitespace-nowrap px-4 py-3 text-right text-sm text-gray-900">
								{formatEUR(inv.amount_cents, inv.currency)}
							</td>
							<td class="whitespace-nowrap px-4 py-3 text-sm">
								<span class="rounded-full px-2 py-0.5 text-xs font-medium {statusBadgeClass(inv.status)}">
									{inv.status}
								</span>
							</td>
							<td class="whitespace-nowrap px-4 py-3 text-right text-sm">
								{#if inv.status === 'paid'}
									<a
										href={api.invoicePDFUrl(inv.id)}
										target="_blank"
										rel="noopener"
										class="text-blue-600 hover:underline"
									>
										Download
									</a>
								{:else}
									<span class="text-gray-400">—</span>
								{/if}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</div>
