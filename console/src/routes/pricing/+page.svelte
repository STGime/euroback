<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type PlanLimits } from '$lib/api.js';

	let limits: PlanLimits[] = $state([]);
	let loading = $state(true);
	let signedIn = $state(false);

	onMount(async () => {
		// Only fetch live limits when the visitor is already signed in.
		// /platform/config/plans is auth-gated; calling it anonymously
		// would 401 and the api wrapper's 401 handler force-redirects
		// to /login — fatal on a public marketing page. Anonymous
		// visitors see the static defaults inline below.
		signedIn = !!api.getToken();
		if (!signedIn) {
			loading = false;
			return;
		}
		try {
			limits = await api.getPlans();
		} catch {
			// Falls back to the hard-coded defaults below.
		} finally {
			loading = false;
		}
	});

	let freePlan = $derived(limits.find(p => p.plan === 'free'));
	let proPlan = $derived(limits.find(p => p.plan === 'pro'));

	function fmt(mb: number | undefined, fallback: string): string {
		if (mb === undefined) return fallback;
		if (mb >= 1024) return (mb / 1024).toFixed(0) + ' GB';
		return mb + ' MB';
	}

	function kmau(n: number | undefined, fallback: string): string {
		if (n === undefined) return fallback;
		if (n >= 1000) return (n / 1000).toFixed(0) + 'k';
		return String(n);
	}

	// Rows in the comparison table. `free` and `pro` are strings rendered
	// verbatim so we can mix data-driven values (DB size, MAU, …) with
	// fixed feature blurbs ("Custom email templates", "EU-sovereign").
	// `category` groups consecutive rows under a sub-header.
	let rows = $derived([
		{ category: 'Database & storage' },
		{ label: 'Database size', free: fmt(freePlan?.db_size_mb, '500 MB'), pro: fmt(proPlan?.db_size_mb, '5 GB') },
		{ label: 'File storage', free: fmt(freePlan?.storage_mb, '1 GB'), pro: fmt(proPlan?.storage_mb, '50 GB') },
		{ label: 'Egress bandwidth', free: fmt(freePlan?.bandwidth_mb, '5 GB') + '/mo', pro: fmt(proPlan?.bandwidth_mb, '100 GB') + '/mo' },
		{ label: 'Upload size', free: (freePlan?.upload_size_mb ?? 10) + ' MB', pro: (proPlan?.upload_size_mb ?? 50) + ' MB' },

		{ category: 'Auth & API' },
		{ label: 'Monthly active users', free: kmau(freePlan?.mau_limit, '10k'), pro: kmau(proPlan?.mau_limit, '100k') },
		{ label: 'API rate limit', free: (freePlan?.rate_limit_rps ?? 100) + ' rps', pro: (proPlan?.rate_limit_rps ?? 1000) + ' rps' },
		{ label: 'Realtime concurrent connections', free: String(freePlan?.ws_connections ?? 100), pro: kmau(proPlan?.ws_connections, '10k') },

		{ category: 'Automation & integrations' },
		{ label: 'Edge functions', free: String(freePlan?.edge_function_limit ?? 3), pro: String(proPlan?.edge_function_limit ?? 25) },
		{ label: 'Scheduled jobs (cron)', free: '2', pro: 'Unlimited' },
		{ label: 'Webhooks', free: String(freePlan?.webhook_limit ?? 3), pro: 'Unlimited' },
		{ label: 'Custom email templates', free: false, pro: true },

		{ category: 'Operations' },
		{ label: 'Log retention', free: (freePlan?.log_retention_days ?? 1) + ' day', pro: (proPlan?.log_retention_days ?? 30) + ' days' },
		{ label: 'Projects per organisation', free: String(freePlan?.project_limit ?? 2), pro: String(proPlan?.project_limit ?? 10) },

		{ category: 'Sovereignty & compliance' },
		{ label: 'EU-hosted infrastructure (Scaleway, France)', free: true, pro: true },
		{ label: 'GDPR by design', free: true, pro: true },
		{ label: 'DPA report (Article 30)', free: true, pro: true },
		{ label: 'Audit log', free: true, pro: true },
		// DSAR is the differentiator: the API is open to everyone (legal-
		// obligation respect — a free-tier tenant on a statutory 30-day
		// deadline must still be able to comply by scripting their own
		// export). The one-click console flow is Pro: that's what saves
		// the customer a day per request and is the actual upsell story.
		{ label: 'DSAR API (Article 15 + 20 export endpoints)', free: 'API', pro: 'API' },
		{ label: 'DSAR console — one-click export', free: false, pro: true },
	]);
</script>

<svelte:head>
	<title>Pricing — Eurobase</title>
	<meta name="description" content="Eurobase pricing — Free for prototypes, Pro for production. EU-sovereign Backend-as-a-Service, made in Berlin." />
</svelte:head>

<div class="min-h-screen bg-gray-50">
	<!-- Top bar (minimal — no nav on this public page) -->
	<header class="border-b border-gray-200 bg-white">
		<div class="mx-auto flex max-w-6xl items-center justify-between px-6 py-4">
			<a href="/" class="text-lg font-bold text-gray-900">Eurobase</a>
			<div class="flex items-center gap-3 text-sm">
				{#if signedIn}
					<a href="/projects" class="rounded-lg bg-eurobase-600 px-4 py-2 font-semibold text-white shadow-sm hover:bg-eurobase-700 transition-colors">Back to dashboard</a>
				{:else}
					<a href="/login" class="text-gray-600 hover:text-gray-900">Sign in</a>
					<a href="/login" class="rounded-lg bg-eurobase-600 px-4 py-2 font-semibold text-white shadow-sm hover:bg-eurobase-700 transition-colors">Get started</a>
				{/if}
			</div>
		</div>
	</header>

	<!-- Hero -->
	<section class="mx-auto max-w-6xl px-6 pt-16 pb-8 text-center">
		<h1 class="text-4xl font-bold tracking-tight text-gray-900 sm:text-5xl">Simple, transparent pricing.</h1>
		<p class="mt-4 text-lg text-gray-600">The EU-sovereign Backend-as-a-Service, made in Berlin. Free to start; €19/mo when you go to production.</p>
	</section>

	<!-- Tier cards -->
	<section class="mx-auto max-w-5xl px-6 pb-12">
		<div class="grid grid-cols-1 gap-6 sm:grid-cols-2">
			<!-- Free -->
			<div class="rounded-2xl border border-gray-200 bg-white p-8 shadow-sm">
				<h2 class="text-xl font-semibold text-gray-900">Free</h2>
				<p class="mt-1 text-sm text-gray-500">For prototypes, side projects, and learning.</p>
				<div class="mt-6 flex items-baseline gap-1">
					<span class="text-4xl font-bold text-gray-900">€0</span>
					<span class="text-sm text-gray-500">/mo</span>
				</div>
				<a href="/login" class="mt-6 block rounded-lg border border-gray-300 bg-white px-4 py-2.5 text-center text-sm font-semibold text-gray-700 shadow-sm hover:bg-gray-50 transition-colors">Start free</a>
				<ul class="mt-6 space-y-2 text-sm text-gray-700">
					<li class="flex gap-2"><span class="text-gray-400">•</span><span>{fmt(freePlan?.db_size_mb, '500 MB')} database, {fmt(freePlan?.storage_mb, '1 GB')} file storage</span></li>
					<li class="flex gap-2"><span class="text-gray-400">•</span><span>{kmau(freePlan?.mau_limit, '10k')} monthly active users</span></li>
					<li class="flex gap-2"><span class="text-gray-400">•</span><span>{freePlan?.ws_connections ?? 100} realtime connections</span></li>
					<li class="flex gap-2"><span class="text-gray-400">•</span><span>{freePlan?.edge_function_limit ?? 3} edge functions, {freePlan?.webhook_limit ?? 3} webhooks</span></li>
					<li class="flex gap-2"><span class="text-gray-400">•</span><span>{freePlan?.project_limit ?? 2} projects per organisation</span></li>
					<li class="flex gap-2"><span class="text-gray-400">•</span><span>EU-hosted, GDPR-by-design</span></li>
				</ul>
			</div>

			<!-- Pro -->
			<div class="relative rounded-2xl border-2 border-eurobase-600 bg-white p-8 shadow-lg">
				<span class="absolute -top-3 right-6 rounded-full bg-eurobase-600 px-3 py-1 text-xs font-semibold text-white shadow">For production</span>
				<h2 class="text-xl font-semibold text-gray-900">Pro</h2>
				<p class="mt-1 text-sm text-gray-500">When your project ships to real users.</p>
				<div class="mt-6 flex items-baseline gap-1">
					<span class="text-4xl font-bold text-gray-900">€19</span>
					<span class="text-sm text-gray-500">/mo per project</span>
				</div>
				<a href="/login" class="mt-6 block rounded-lg bg-eurobase-600 px-4 py-2.5 text-center text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 transition-colors">Get Pro</a>
				<ul class="mt-6 space-y-2 text-sm text-gray-700">
					<li class="flex gap-2"><span class="text-eurobase-500">✓</span><span>{fmt(proPlan?.db_size_mb, '5 GB')} database, {fmt(proPlan?.storage_mb, '50 GB')} file storage</span></li>
					<li class="flex gap-2"><span class="text-eurobase-500">✓</span><span>{kmau(proPlan?.mau_limit, '100k')} MAU, {(proPlan?.rate_limit_rps ?? 1000)} rps</span></li>
					<li class="flex gap-2"><span class="text-eurobase-500">✓</span><span>{kmau(proPlan?.ws_connections, '10k')} realtime connections</span></li>
					<li class="flex gap-2"><span class="text-eurobase-500">✓</span><span>{proPlan?.edge_function_limit ?? 25} edge functions, unlimited cron &amp; webhooks</span></li>
					<li class="flex gap-2"><span class="text-eurobase-500">✓</span><span>{proPlan?.log_retention_days ?? 30}-day log retention, custom email templates</span></li>
					<li class="flex gap-2"><span class="text-eurobase-500">✓</span><span>EU-hosted, GDPR-by-design</span></li>
					<li class="flex gap-2"><span class="text-eurobase-500">✓</span><span><strong>One-click DSAR exports</strong> (Article 15 + 20) — audit-trailed, EU-only</span></li>
				</ul>
			</div>
		</div>
	</section>

	<!-- Comparison table -->
	<section class="mx-auto max-w-5xl px-6 pb-16">
		<h2 class="text-2xl font-semibold text-gray-900 mb-4">Full comparison</h2>
		<div class="overflow-hidden rounded-xl border border-gray-200 bg-white shadow-sm">
			<table class="w-full text-sm">
				<thead class="bg-gray-50">
					<tr>
						<th class="px-6 py-3 text-left font-medium text-gray-700"></th>
						<th class="px-6 py-3 text-center font-medium text-gray-700 w-32">Free</th>
						<th class="px-6 py-3 text-center font-medium text-gray-700 w-32">Pro</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-gray-200">
					{#each rows as r}
						{#if r.category}
							<tr class="bg-gray-50">
								<td colspan="3" class="px-6 py-2 text-xs font-semibold uppercase tracking-wide text-gray-500">{r.category}</td>
							</tr>
						{:else}
							<tr>
								<td class="px-6 py-3 text-gray-700">{r.label}</td>
								<td class="px-6 py-3 text-center text-gray-600">
									{#if typeof r.free === 'boolean'}
										{#if r.free}<span class="text-emerald-500">✓</span>{:else}<span class="text-gray-300">—</span>{/if}
									{:else}
										{r.free}
									{/if}
								</td>
								<td class="px-6 py-3 text-center text-gray-900 font-medium">
									{#if typeof r.pro === 'boolean'}
										{#if r.pro}<span class="text-eurobase-600">✓</span>{:else}<span class="text-gray-300">—</span>{/if}
									{:else}
										{r.pro}
									{/if}
								</td>
							</tr>
						{/if}
					{/each}
				</tbody>
			</table>
		</div>
		{#if loading}
			<p class="mt-3 text-xs text-gray-400">Loading live limits…</p>
		{/if}
	</section>

	<!-- Sovereignty footer -->
	<section class="border-t border-gray-200 bg-white">
		<div class="mx-auto max-w-5xl px-6 py-12 text-center">
			<h2 class="text-2xl font-semibold text-gray-900">Made in Berlin. Hosted in France.</h2>
			<p class="mt-3 text-sm text-gray-600 max-w-2xl mx-auto">All Eurobase data lives in EU jurisdiction (Scaleway, France). GDPR by design — DPA report (Article 30), sub-processor list, audit log, and DSAR exports (Article 15 + 20) are built in. <a href="/docs#compliance" class="text-eurobase-600 hover:text-eurobase-700 underline">Read the docs</a>.</p>
			<div class="mt-6 flex items-center justify-center gap-4 text-sm">
				<a href="https://bsky.app/profile/eurobasebaas.bsky.social" target="_blank" rel="noopener noreferrer" class="inline-flex items-center gap-2 text-gray-600 hover:text-eurobase-700 transition-colors">
					<svg class="h-4 w-4" viewBox="0 0 600 530" fill="currentColor" aria-hidden="true">
						<path d="M135.72 44.03C202.216 93.951 273.74 195.17 300 249.49c26.262-54.316 97.782-155.54 164.28-205.46C512.26 8.009 590-19.862 590 68.825c0 17.712-10.155 148.79-16.111 170.07-20.703 73.984-96.144 92.854-163.25 81.433 117.3 19.964 147.14 86.092 82.697 152.22-122.39 125.59-175.91-31.511-189.63-71.766-2.514-7.38-3.69-10.832-3.708-7.896-.017-2.936-1.193.516-3.707 7.896-13.714 40.255-67.233 197.36-189.63 71.766-64.444-66.128-34.605-132.26 82.697-152.22-67.108 11.421-142.55-7.45-163.25-81.433C20.156 217.613 10 86.535 10 68.825c0-88.687 77.742-60.816 125.72-24.795z"/>
					</svg>
					Follow on Bluesky
				</a>
			</div>
		</div>
	</section>
</div>
