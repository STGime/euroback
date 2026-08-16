<script lang="ts">
	// Per-project billing: current plan, upgrade CTA, cancel
	// button, invoices scoped to this project. Deep-linked from
	// LegacyProModal.svelte via `?plan=pro` (grepped — no other
	// producer today).
	import { page } from '$app/stores';
	import { api, type BillingConfig, type Invoice, type Project, type ProjectSubscription } from '$lib/api.js';
	import { onMount } from 'svelte';
	import CancelSubscriptionModal from '$lib/CancelSubscriptionModal.svelte';
	import BillingTestModeBanner from '$lib/BillingTestModeBanner.svelte';

	let projectId = $derived($page.params.id as string);

	let project: Project | null = $state(null);
	let subscription: ProjectSubscription | null = $state(null);
	let invoices: Invoice[] = $state([]);
	let loading = $state(true);
	let error: string | null = $state(null);

	// Cancel modal state
	let cancelModalOpen = $state(false);
	let cancelSuccess: string | null = $state(null);

	// Checkout state — while a redirect is in flight the button
	// should be disabled so a double-click doesn't hit the API
	// twice (idempotency in the service catches this, but a
	// disabled button is a cleaner UX signal).
	let checkoutInFlight = $state(false);
	let checkoutError: string | null = $state(null);

	// Billing config probe. Two roles:
	//   1. `mode === 'test'` skips auto-start on ?plan=pro so the
	//      user has to click Upgrade *after* seeing the yellow
	//      BillingTestModeBanner. Without this, the LegacyProModal
	//      goto to `?plan=pro` would redirect to Mollie before the
	//      banner's own fetch completes (PR #398 review).
	//   2. `billingConfig === null` (probe unresolved OR failed)
	//      disables the Upgrade CTA — an unknown mode is exactly
	//      when we least want a one-click path to a payment page.
	// $state<T | null>(null) form, not `: T | null = $state(null)`:
	// the annotation-on-variable form makes Svelte's language
	// service infer `never` after `!== null` narrowing, silently
	// disabling type-check on the property access below.
	let billingConfig = $state<BillingConfig | null>(null);

	// Auto-open Pro checkout if arrived via ?plan=pro (deep
	// link from the legacy-Pro modal).
	let autoStart = $derived($page.url.searchParams.get('plan') === 'pro');
	let successBanner = $derived($page.url.searchParams.get('status') === 'success');

	// Gate the Upgrade CTA on a resolved probe. Failed probe →
	// button disabled; the user can retry by refreshing.
	let checkoutReady = $derived(billingConfig !== null && billingConfig.enabled);

	onMount(async () => {
		loading = true;
		try {
			// Fetch config in parallel with the rest — its result
			// gates auto-start below, so we must await it before
			// deciding whether to redirect.
			const [proj, list, sub, cfg] = await Promise.all([
				api.getProject(projectId),
				api.listInvoices(),
				api.getProjectSubscription(projectId),
				api.getBillingConfig().catch(() => null)
			]);
			project = proj;
			invoices = list.invoices.filter((i) => i.project_id === projectId);
			subscription = sub;
			billingConfig = cfg;
			// Auto-start only in live mode. In test mode (or when
			// the probe failed) the user must click through the
			// banner deliberately.
			if (
				autoStart &&
				project &&
				needsPayment(project) &&
				cfg?.enabled &&
				cfg?.mode === 'live'
			) {
				await startCheckout();
			}
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		} finally {
			loading = false;
		}
	});

	function onCancelComplete(_: { mode: string; refundedCents: number }): void {
		// Single-mode cancel post-2026-08-16 policy change (see
		// CancelSubscriptionModal): always end-of-period, no
		// refund. The `mode`/`refundedCents` args are kept on the
		// callback shape for a possible future re-enable of the
		// immediate-cancel branch.
		cancelModalOpen = false;
		cancelSuccess = 'Your subscription will end at the end of the current period. You keep Pro features until then.';
		// Reload state so the subscription's canceled_at + status
		// flip are visible immediately.
		void refresh();
	}

	async function refresh(): Promise<void> {
		try {
			project = await api.getProject(projectId);
			const [list, sub] = await Promise.all([
				api.listInvoices(),
				api.getProjectSubscription(projectId)
			]);
			invoices = list.invoices.filter((i) => i.project_id === projectId);
			subscription = sub;
		} catch {
			// Non-fatal; the cancelSuccess banner is the primary
			// user signal.
		}
	}

	function needsPayment(p: Project): boolean {
		// "Legacy-Pro pending payment" OR "still on Free wanting Pro"
		if (p.plan === 'pro' && p.legacy_pro_grace_until) return true;
		if (p.plan === 'free') return true;
		return false;
	}

	async function startCheckout(): Promise<void> {
		if (checkoutInFlight) return;
		checkoutInFlight = true;
		checkoutError = null;
		try {
			const res = await api.startBillingCheckout(projectId, 'pro');
			// Full-page redirect to Mollie — DO NOT open in a new
			// tab. Mollie's success/cancel handling relies on us
			// controlling the parent window's location.
			window.location.href = res.checkout_url;
		} catch (err) {
			checkoutError = err instanceof Error ? err.message : String(err);
			checkoutInFlight = false;
		}
	}

	function formatEUR(cents: number, currency: string): string {
		const prefix = currency === 'EUR' ? '€' : `${currency} `;
		const abs = Math.abs(cents);
		const whole = Math.floor(abs / 100);
		const frac = abs % 100;
		const sign = cents < 0 ? '-' : '';
		return `${sign}${prefix}${whole}.${String(frac).padStart(2, '0')}`;
	}

	function daysUntil(iso: string): number {
		const then = new Date(iso).getTime();
		const now = Date.now();
		return Math.max(0, Math.ceil((then - now) / (24 * 60 * 60 * 1000)));
	}

	let graceDaysLeft = $derived.by(() => {
		const grace = project?.legacy_pro_grace_until;
		return grace ? daysUntil(grace) : null;
	});

	// Two-hop invoice download — see api.getInvoicePDFURL comment
	// for why we can't just link directly, and the /billing
	// page's openInvoicePDF comment for the popup-blocker
	// rationale on grabbing the tab handle before the await.
	async function openInvoicePDF(invoiceId: string): Promise<void> {
		const w = window.open('', '_blank');
		try {
			const url = await api.getInvoicePDFURL(invoiceId);
			if (w) {
				w.opener = null;
				w.location.assign(url);
			} else {
				window.location.assign(url);
			}
		} catch (err) {
			if (w) w.close();
			const msg = err instanceof Error ? err.message : String(err);
			alert(`Couldn't open invoice: ${msg}`);
		}
	}
</script>

<svelte:head>
	<title>Billing — {project?.name ?? 'Project'} — Eurobase</title>
</svelte:head>

<div class="mx-auto max-w-4xl px-4 py-8 sm:px-6 lg:px-8">
	<div class="mb-6">
		<h1 class="text-2xl font-bold text-gray-900">Billing</h1>
		<p class="mt-2 text-sm text-gray-600">
			{project?.name ?? 'Loading…'}
		</p>
	</div>

	<BillingTestModeBanner />

	{#if successBanner}
		<div class="mb-6 rounded-md border border-green-200 bg-green-50 p-4">
			<p class="text-sm font-medium text-green-800">
				Thanks — your payment went through. It may take a minute
				before Pro activates fully. Refresh this page if you
				don't see Pro status yet.
			</p>
		</div>
	{/if}

	{#if cancelSuccess}
		<div class="mb-6 rounded-md border border-blue-200 bg-blue-50 p-4">
			<p class="text-sm font-medium text-blue-800">{cancelSuccess}</p>
		</div>
	{/if}

	{#if loading}
		<p class="text-sm text-gray-500">Loading billing status…</p>
	{:else if error}
		<div class="rounded-md border border-red-200 bg-red-50 p-4">
			<p class="text-sm text-red-800">{error}</p>
		</div>
	{:else if project}
		<!-- ── Current plan card ─────────────────────────────── -->
		<div class="mb-6 rounded-lg border border-gray-200 bg-white p-6">
			<div class="flex items-start justify-between">
				<div>
					<p class="text-xs uppercase tracking-wider text-gray-500">Current plan</p>
					<p class="mt-1 text-xl font-semibold text-gray-900">
						{#if project.plan === 'pro'}
							Pro (€19/mo)
						{:else if project.plan === 'team'}
							Team (closed beta)
						{:else if project.plan === 'legal_team'}
							Legal Team (closed beta)
						{:else}
							Free
						{/if}
					</p>
					{#if project.plan === 'pro' && project.legacy_pro_grace_until}
						{@const days = graceDaysLeft ?? 0}
						<p
							class="mt-2 text-sm {days <= 3 ? 'font-medium text-red-700' : 'text-yellow-800'}"
						>
							{#if days === 0}
								Grace period expired — your project will be downgraded to Free on the next sweep.
							{:else if days === 1}
								1 day left to add a payment method before this project drops to Free.
							{:else}
								{days} days left to add a payment method before this project drops to Free.
							{/if}
						</p>
					{:else if project.plan === 'free'}
						<p class="mt-2 text-sm text-gray-600">
							5,000 MAU · 512 MB storage · 2 GB bandwidth · auto-pauses after 30 days idle.
						</p>
					{:else if project.plan === 'pro'}
						<p class="mt-2 text-sm text-gray-600">
							100k MAU · 100 GB storage · 250 GB bandwidth · never pauses.
						</p>
					{:else if project.plan === 'team' || project.plan === 'legal_team'}
						<p class="mt-2 text-sm text-gray-600">
							1M MAU · 100 GB dedicated database · 500 GB file storage · never pauses.
						</p>
					{/if}
				</div>
				<div>
					{#if project.plan === 'pro' && project.legacy_pro_grace_until}
						<!-- Legacy-Pro pending payment: primary CTA is add-a-card. -->
						<button
							type="button"
							onclick={startCheckout}
							disabled={checkoutInFlight || !checkoutReady}
							title={!checkoutReady ? 'Payments unavailable — please refresh' : undefined}
							class="inline-flex items-center rounded-md bg-blue-600 px-4 py-2 text-sm font-medium text-white shadow-sm hover:bg-blue-700 disabled:cursor-not-allowed disabled:opacity-50"
						>
							{checkoutInFlight ? 'Redirecting…' : 'Add payment (€19/mo)'}
						</button>
					{:else if project.plan === 'free'}
						<button
							type="button"
							onclick={startCheckout}
							disabled={checkoutInFlight || !checkoutReady}
							title={!checkoutReady ? 'Payments unavailable — please refresh' : undefined}
							class="inline-flex items-center rounded-md bg-blue-600 px-4 py-2 text-sm font-medium text-white shadow-sm hover:bg-blue-700 disabled:cursor-not-allowed disabled:opacity-50"
						>
							{checkoutInFlight ? 'Redirecting…' : 'Upgrade to Pro'}
						</button>
					{:else if project.plan === 'team' || project.plan === 'legal_team'}
						<!-- Team / Legal Team run on beta grants — no
						     self-serve upgrade or cancel; ops manages
						     these via the admin panel. Just show the
						     status badge. -->
						<span class="inline-flex rounded-full bg-eurobase-100 px-3 py-1 text-xs font-medium text-eurobase-800">
							Beta grant · active
						</span>
					{:else}
						<div class="flex items-center gap-3">
							<span class="inline-flex rounded-full bg-green-100 px-3 py-1 text-xs font-medium text-green-800"
								>Active</span
							>
							{#if subscription && subscription.status !== 'past_due'}
								<button
									type="button"
									onclick={() => (cancelModalOpen = true)}
									class="text-sm text-gray-600 hover:text-red-700"
								>
									Cancel Pro
								</button>
							{/if}
						</div>
					{/if}
				</div>
			</div>
			{#if checkoutError}
				<p class="mt-4 text-sm text-red-700">Couldn't start checkout: {checkoutError}</p>
			{/if}
		</div>

		<!-- ── Invoices scoped to this project ────────────────── -->
		<div class="rounded-lg border border-gray-200 bg-white">
			<div class="border-b border-gray-200 px-6 py-4">
				<h2 class="text-lg font-medium text-gray-900">Invoices</h2>
				<p class="mt-1 text-sm text-gray-500">
					PDFs are stored in EU-region Object Storage (Scaleway) and retained
					for 7 years per Estonian Accounting Act.
				</p>
			</div>
			{#if invoices.length === 0}
				<div class="px-6 py-8 text-center">
					<p class="text-sm text-gray-500">
						No invoices for this project yet. They'll appear here after
						your first paid billing cycle.
					</p>
				</div>
			{:else}
				<table class="min-w-full divide-y divide-gray-200">
					<thead class="bg-gray-50">
						<tr>
							<th class="px-6 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500"
								>Invoice</th
							>
							<th class="px-6 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500"
								>Date</th
							>
							<th class="px-6 py-3 text-right text-xs font-medium uppercase tracking-wider text-gray-500"
								>Amount</th
							>
							<th class="px-6 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500"
								>Status</th
							>
							<th class="px-6 py-3 text-right text-xs font-medium uppercase tracking-wider text-gray-500"
								>PDF</th
							>
						</tr>
					</thead>
					<tbody class="divide-y divide-gray-200 bg-white">
						{#each invoices as inv (inv.id)}
							<tr>
								<td class="whitespace-nowrap px-6 py-3 font-mono text-sm text-gray-900"
									>{inv.number}</td
								>
								<td class="whitespace-nowrap px-6 py-3 text-sm text-gray-500">
									{new Date(inv.created_at).toLocaleDateString('en-GB', {
										year: 'numeric',
										month: 'short',
										day: 'numeric'
									})}
								</td>
								<td class="whitespace-nowrap px-6 py-3 text-right text-sm text-gray-900">
									{formatEUR(inv.amount_cents, inv.currency)}
								</td>
								<td class="whitespace-nowrap px-6 py-3 text-sm">
									{inv.status}
								</td>
								<td class="whitespace-nowrap px-6 py-3 text-right text-sm">
									{#if inv.status === 'paid'}
										<button
											type="button"
											onclick={() => openInvoicePDF(inv.id)}
											class="text-blue-600 hover:underline"
										>
											Download
										</button>
									{:else}
										<span class="text-gray-400">—</span>
									{/if}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			{/if}
		</div>
	{/if}
</div>

{#if cancelModalOpen && subscription}
	<CancelSubscriptionModal
		{subscription}
		onclose={() => (cancelModalOpen = false)}
		oncomplete={onCancelComplete}
	/>
{/if}
