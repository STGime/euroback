<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type PlanLimits } from '$lib/api.js';

	let limits: PlanLimits[] = $state([]);
	let loading = $state(true);
	let signedIn = $state(false);
	// Team-tier closed-beta gate (M2). When true, the Team column
	// swaps its "Coming soon" copy for a live "Create Team project"
	// CTA that deep-links into /projects?new=team.
	let hasTeamBeta = $state(false);

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
			// Load plans + profile in parallel so the Team CTA
			// resolves at the same time as the limits.
			const [plansRes, profile] = await Promise.all([
				api.getPlans(),
				api.getProfile().catch(() => null)
			]);
			limits = plansRes;
			if (profile) hasTeamBeta = !!profile.team_beta_access;
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

	// Rows in the comparison table. `free`/`pro`/`team` render verbatim
	// (strings) or as check/dash (booleans). Team column values are
	// mostly `'Coming soon'` — Team tier is planned per the monetization
	// proposal but not shipped yet, so the column signals intent without
	// falsely advertising availability. `category` groups consecutive
	// rows under a sub-header.
	//
	// Phase B tightening (migration 000075) halves four Free caps —
	// MAU / storage / bandwidth / realtime cxns. The fallback strings
	// below reflect the NEW (post-migration) values so anonymous
	// visitors see the tight numbers even before the live-fetch runs.
	let rows = $derived([
		{ category: 'Database & storage' },
		{ label: 'Database size', free: fmt(freePlan?.db_size_mb, '500 MB'), pro: fmt(proPlan?.db_size_mb, '5 GB'), team: 'Coming soon', legal: 'Coming soon' },
		{ label: 'File storage', free: fmt(freePlan?.storage_mb, '500 MB'), pro: fmt(proPlan?.storage_mb, '50 GB'), team: 'Coming soon', legal: 'Coming soon' },
		{ label: 'Egress bandwidth', free: fmt(freePlan?.bandwidth_mb, '2 GB') + '/mo', pro: fmt(proPlan?.bandwidth_mb, '100 GB') + '/mo', team: 'Coming soon', legal: 'Coming soon' },
		{ label: 'Upload size', free: (freePlan?.upload_size_mb ?? 10) + ' MB', pro: (proPlan?.upload_size_mb ?? 50) + ' MB', team: 'Coming soon', legal: 'Coming soon' },
		{ label: 'Dedicated Postgres instance', free: false, pro: false, team: 'Coming soon', legal: 'Coming soon' },
		{ label: 'Daily backups + point-in-time recovery', free: false, pro: false, team: 'Coming soon', legal: 'Coming soon' },

		{ category: 'Auth & API' },
		{ label: 'Monthly active users', free: kmau(freePlan?.mau_limit, '5k'), pro: kmau(proPlan?.mau_limit, '100k'), team: 'Coming soon', legal: 'Coming soon' },
		{ label: 'API rate limit', free: (freePlan?.rate_limit_rps ?? 100) + ' rps', pro: (proPlan?.rate_limit_rps ?? 1000) + ' rps', team: 'Coming soon', legal: 'Coming soon' },
		{ label: 'Realtime concurrent connections', free: String(freePlan?.ws_connections ?? 50), pro: kmau(proPlan?.ws_connections, '10k'), team: 'Coming soon', legal: 'Coming soon' },
		{ label: 'SSO (SAML) for console sign-in', free: false, pro: false, team: 'Coming soon', legal: 'Coming soon' },
		{ label: 'RBAC (Owner / Admin / Developer / Read-only)', free: false, pro: false, team: 'Coming soon', legal: 'Coming soon' },

		{ category: 'Automation & integrations' },
		{ label: 'Edge functions', free: String(freePlan?.edge_function_limit ?? 3), pro: String(proPlan?.edge_function_limit ?? 25), team: 'Coming soon', legal: 'Coming soon' },
		{ label: 'Scheduled jobs (cron)', free: '2', pro: 'Unlimited', team: 'Coming soon', legal: 'Coming soon' },
		{ label: 'Webhooks', free: String(freePlan?.webhook_limit ?? 3), pro: 'Unlimited', team: 'Coming soon', legal: 'Coming soon' },
		{ label: 'Custom email templates', free: false, pro: true, team: 'Coming soon', legal: 'Coming soon' },
		{ label: 'Custom domain (CNAME your own domain)', free: false, pro: 'Coming soon', team: 'Coming soon', legal: 'Coming soon' },
		{ label: 'Bring-your-own SMTP for auth mail', free: false, pro: true, team: 'Coming soon', legal: 'Coming soon' },
		{ label: 'Slack / webhook quota alerts', free: false, pro: true, team: 'Coming soon', legal: 'Coming soon' },

		{ category: 'Lifecycle' },
		{ label: 'Idle-project pause after 30 days', free: 'Auto', pro: 'Never', team: 'Never', legal: 'Never' },

		{ category: 'Operations' },
		{ label: 'Log retention', free: (freePlan?.log_retention_days ?? 1) + ' day', pro: (proPlan?.log_retention_days ?? 30) + ' days', team: 'Coming soon', legal: 'Coming soon' },
		{ label: 'Projects per organisation', free: String(freePlan?.project_limit ?? 2), pro: String(proPlan?.project_limit ?? 10), team: 'Coming soon', legal: 'Coming soon' },
		{ label: 'Priority email support (24 h SLA)', free: false, pro: false, team: 'Coming soon', legal: 'Coming soon' },
		{ label: 'Uptime SLA (99.9 %)', free: false, pro: false, team: 'Coming soon', legal: 'Coming soon' },

		{ category: 'Sovereignty & compliance' },
		{ label: 'EU-hosted infrastructure (Scaleway, France)', free: true, pro: true, team: true, legal: true },
		{ label: 'GDPR by design', free: true, pro: true, team: true, legal: true },
		{ label: 'DPA report (Article 30)', free: true, pro: true, team: true, legal: true },
		{ label: 'Audit log', free: true, pro: true, team: true, legal: true },
		{ label: 'DSAR API (Article 15 + 20 export endpoints)', free: 'API', pro: 'API', team: 'API', legal: 'API' },
		{ label: 'DSAR console — one-click export', free: false, pro: true, team: true, legal: true },
		{ label: 'SOC 2 Type II attestation', free: false, pro: false, team: 'Coming soon', legal: 'Coming soon' },

		{ category: 'German legal-tech retention' },
		// Legal Team ships these today for beta users; GA pricing
		// TBD. Row cells show "Beta" so a buyer cross-checking the
		// /legal page (which describes them present-tense) doesn't
		// see a "Coming soon" that contradicts the pitch. See #417
		// review 🟡.
		{ label: 'WORM per-prefix retention (S3 Object Lock)', free: false, pro: false, team: false, legal: 'Beta' },
		{ label: 'Ad-hoc retention holds (row / object)', free: false, pro: false, team: false, legal: 'Beta' },
		{ label: 'DSAR erasure respects legal holds', free: false, pro: false, team: false, legal: 'Beta' },
		{ label: 'Audit-log retention', free: '90 days', pro: '90 days', team: '90 days', legal: '10 years' },
		{ label: '§50 BRAO / §257 HGB / §147 AO ready', free: false, pro: false, team: false, legal: 'Beta' },
	]);
</script>

<svelte:head>
	<title>Pricing — Eurobase · EU-sovereign backend, GDPR-compliant, legal-tech ready</title>
	<meta name="description" content="Eurobase pricing: Free for prototypes, €19/mo Pro for production, Team for SMBs, Legal Team for German legal-tech startups needing §257 HGB / §50 BRAO / §147 AO retention. EU-sovereign Backend-as-a-Service hosted in France, made in Berlin. GDPR by design, DSAR built-in, no US CLOUD Act exposure." />
	<meta name="keywords" content="EU sovereign BaaS, GDPR compliant backend, DSGVO Backend-as-a-Service, Backend Deutschland, legal-tech backend, Kanzlei-Software Hosting, §257 HGB retention, WORM Object Storage, Rechtsanwalt SaaS DSGVO, EU alternative Firebase, EU alternative Supabase" />
	<meta property="og:title" content="Eurobase — EU-sovereign backend for GDPR-conscious startups, including German legal-tech" />
	<meta property="og:description" content="Free to start, €19/mo Pro, dedicated Postgres on Team, WORM retention + §257 HGB / §50 BRAO / §147 AO compliance on Legal Team. Made in Berlin. Hosted in France. Zero DevOps." />
	<meta property="og:type" content="website" />
	<link rel="canonical" href="https://eurobase.app/pricing" />
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
	<section class="mx-auto max-w-6xl px-6 pt-16 pb-6 text-center">
		<h1 class="text-4xl font-bold tracking-tight text-gray-900 sm:text-5xl">Simple, transparent pricing.</h1>
		<p class="mt-4 text-lg text-gray-600">The EU-sovereign Backend-as-a-Service, made in Berlin. Free to start; €19/mo when you go to production; Team for SMBs; a dedicated Legal Team tier for German legal-tech startups is on the way.</p>
	</section>

	<!-- Why Eurobase — the differentiators, right under the hero -->
	<section class="mx-auto max-w-6xl px-6 pb-6">
		<div class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
			<div class="rounded-xl border border-gray-200 bg-white p-5">
				<div class="flex items-center gap-2 text-eurobase-700">
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M13 10V3L4 14h7v7l9-11h-7z"/></svg>
					<p class="text-sm font-semibold text-gray-900">Zero DevOps</p>
				</div>
				<p class="mt-2 text-sm text-gray-600">Postgres, storage, auth, edge functions, cron, webhooks — all wired up and monitored. You write app code; we run the boring parts.</p>
			</div>
			<div class="rounded-xl border border-gray-200 bg-white p-5">
				<div class="flex items-center gap-2 text-eurobase-700">
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M3 12l9-9 9 9M4 10v10a1 1 0 001 1h5v-6h4v6h5a1 1 0 001-1V10"/></svg>
					<p class="text-sm font-semibold text-gray-900">EU-sovereign</p>
				</div>
				<p class="mt-2 text-sm text-gray-600">All data on Scaleway (France). No US cloud, no CLOUD Act exposure. Sub-processor list published; DPA (Article 30) report generated per project.</p>
			</div>
			<div class="rounded-xl border border-gray-200 bg-white p-5">
				<div class="flex items-center gap-2 text-eurobase-700">
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016z"/></svg>
					<p class="text-sm font-semibold text-gray-900">GDPR by design</p>
				</div>
				<p class="mt-2 text-sm text-gray-600">DSAR export API (Articles 15 + 20) open on every tier — even Free. One-click console export from Pro up. Audit log on every project.</p>
			</div>
			<div class="rounded-xl border border-gray-200 bg-white p-5">
				<div class="flex items-center gap-2 text-eurobase-700">
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M12 6v6m0 0v6m0-6h6m-6 0H6"/></svg>
					<p class="text-sm font-semibold text-gray-900">Open export</p>
				</div>
				<p class="mt-2 text-sm text-gray-600">Your data is yours. <code class="rounded bg-gray-100 px-1 text-[11px]">eurobase db dump</code> gives you a standard <code class="rounded bg-gray-100 px-1 text-[11px]">pg_dump</code> you can migrate anywhere. No lock-in.</p>
			</div>
		</div>
	</section>

	<!-- NEW — recent shipments -->
	<section class="mx-auto max-w-6xl px-6 pb-6">
		<div class="rounded-xl border border-emerald-200 bg-emerald-50/60 p-5 sm:p-6">
			<div class="flex items-center gap-2">
				<span class="rounded-full bg-emerald-600 px-2 py-0.5 text-[10px] font-bold uppercase tracking-wider text-white">New</span>
				<h2 class="text-base font-semibold text-emerald-900">Recent shipments</h2>
			</div>
			<ul class="mt-3 grid grid-cols-1 gap-2 text-sm text-emerald-900 sm:grid-cols-2 lg:grid-cols-4">
				<li class="flex gap-2"><span class="text-emerald-600">✓</span><span><strong>Payment-first Pro checkout</strong> — pay via Mollie, project auto-provisions on webhook.</span></li>
				<li class="flex gap-2"><span class="text-emerald-600">✓</span><span><strong>Dedicated Postgres on Team</strong> — direct <code class="rounded bg-white/60 px-1 text-[11px]">DATABASE_URL</code> for Payload, Prisma, Drizzle.</span></li>
				<li class="flex gap-2"><span class="text-emerald-600">✓</span><span><strong>Test-mode billing rehearsal</strong> — real Mollie flow before public launch.</span></li>
				<li class="flex gap-2"><span class="text-emerald-600">✓</span><span><strong>Legal Team preview</strong> — WORM retention + §257 HGB / §50 BRAO / §147 AO holds. <a href="/legal" class="underline hover:no-underline">See the legal-tech page</a>.</span></li>
			</ul>
		</div>
	</section>

	<!-- Who it's for — one honest sentence -->
	<section class="mx-auto max-w-6xl px-6 pb-10 text-center">
		<p class="text-sm text-gray-600">
			<span class="font-semibold text-gray-800">Built for</span>
			indie developers shipping side projects,
			startups going to production without hiring a DevOps engineer,
			agencies handing off EU-hosted backends to their clients,
			and
			<a href="/legal" class="text-eurobase-700 hover:text-eurobase-800 underline decoration-eurobase-300 underline-offset-2 font-medium">legal-tech startups in Germany</a>
			that need §257 HGB / §50 BRAO / §147 AO-grade retention on top of a modern developer stack.
		</p>
	</section>

	<!-- Tier cards -->
	<section class="mx-auto max-w-6xl px-6 pb-12">
		<div class="grid grid-cols-1 gap-6 md:grid-cols-2 lg:grid-cols-4">
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
					<li class="flex gap-2"><span class="text-gray-400">•</span><span>{fmt(freePlan?.db_size_mb, '500 MB')} database, {fmt(freePlan?.storage_mb, '500 MB')} file storage</span></li>
					<li class="flex gap-2"><span class="text-gray-400">•</span><span>{kmau(freePlan?.mau_limit, '5k')} monthly active users</span></li>
					<li class="flex gap-2"><span class="text-gray-400">•</span><span>{freePlan?.ws_connections ?? 50} realtime connections</span></li>
					<li class="flex gap-2"><span class="text-gray-400">•</span><span>{freePlan?.edge_function_limit ?? 3} edge functions, {freePlan?.webhook_limit ?? 3} webhooks</span></li>
					<li class="flex gap-2"><span class="text-gray-400">•</span><span>{freePlan?.project_limit ?? 2} projects per organisation</span></li>
					<li class="flex gap-2"><span class="text-gray-400">•</span><span>EU-hosted, GDPR-by-design</span></li>
					<li class="flex gap-2"><span class="text-gray-400">•</span><span class="text-xs italic text-amber-600">Projects auto-pause after 30 days idle</span></li>
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
					<li class="flex gap-2"><span class="text-eurobase-500">✓</span><span>BYO SMTP for auth mail, Slack quota alerts</span></li>
					<li class="flex gap-2"><span class="text-eurobase-500">✓</span><span><strong>Never pauses</strong> — Free projects idle-pause after 30 days</span></li>
					<li class="flex gap-2"><span class="text-eurobase-500">✓</span><span><strong>One-click DSAR exports</strong> (Article 15 + 20) — audit-trailed, EU-only</span></li>
				</ul>
			</div>

			<!--
				Team card — closed-beta gate (Team-tier M2).
				  * hasTeamBeta = true  → live "Create Team project" CTA,
				                          "Free during closed beta" price.
				  * hasTeamBeta = false → "Coming soon" pill, muted price,
				                          no CTA (existing pre-M2 behaviour).
				The card structure stays the same either way so the grid
				doesn't visually reflow between granted and non-granted
				users.
			-->
			<div class="relative rounded-2xl border {hasTeamBeta ? 'border-2 border-emerald-500 shadow-lg' : 'border border-dashed border-gray-300 shadow-sm'} bg-white p-8">
				{#if hasTeamBeta}
					<span class="absolute -top-3 right-6 rounded-full bg-emerald-600 px-3 py-1 text-xs font-semibold text-white shadow">Closed beta</span>
				{:else}
					<span class="absolute -top-3 right-6 rounded-full bg-amber-500/20 text-amber-700 border border-amber-400/60 px-3 py-1 text-xs font-semibold shadow">Coming soon</span>
				{/if}
				<h2 class="text-xl font-semibold text-gray-900">Team</h2>
				<p class="mt-1 text-sm text-gray-500">For SMBs running production on Eurobase.</p>
				<div class="mt-6 flex items-baseline gap-1">
					{#if hasTeamBeta}
						<span class="text-4xl font-bold text-emerald-700">Free</span>
						<span class="text-sm text-gray-500">during closed beta</span>
					{:else}
						<span class="text-4xl font-bold text-gray-400">€—</span>
						<span class="text-sm text-gray-400">pricing TBD</span>
					{/if}
				</div>
				{#if hasTeamBeta}
					<a href="/projects?new=team" class="mt-6 block rounded-lg bg-emerald-600 px-4 py-2.5 text-center text-sm font-semibold text-white shadow-sm hover:bg-emerald-700 transition-colors">
						Create Team project
					</a>
				{:else}
					<div class="mt-6 block rounded-lg border border-gray-200 bg-gray-50 px-4 py-2.5 text-center text-sm text-gray-500">Waitlist opening later</div>
				{/if}
				<ul class="mt-6 space-y-2 text-sm text-gray-600">
					<li class="flex gap-2"><span class="text-gray-400">•</span><span>Everything in Pro, plus:</span></li>
					<li class="flex gap-2"><span class="text-gray-400">•</span><span><strong>Dedicated Postgres instance</strong> per project — direct <code class="rounded bg-gray-100 px-1 py-0.5 text-[11px] font-mono">DATABASE_URL</code> for Payload / Prisma / Drizzle</span></li>
					<li class="flex gap-2"><span class="text-gray-400">•</span><span>Daily backups + 7-day PITR</span></li>
					<li class="flex gap-2"><span class="text-gray-400">•</span><span>SSO (SAML) for console sign-in</span></li>
					<li class="flex gap-2"><span class="text-gray-400">•</span><span>RBAC — Owner / Admin / Developer / Read-only</span></li>
					<li class="flex gap-2"><span class="text-gray-400">•</span><span>Priority email support (24 h SLA)</span></li>
					<li class="flex gap-2"><span class="text-gray-400">•</span><span>SOC 2 Type II attestation</span></li>
				</ul>
			</div>

			<!--
				Legal Team card — separate SKU for German legal-tech
				startups needing WORM-enforced statutory retention on
				top of the Team stack. Closed-beta pricing TBD;
				"Contact us" CTA for now. Cross-links to the dedicated
				/legal marketing page for SEO landing traffic.
			-->
			<div class="relative rounded-2xl border border-dashed border-gray-300 bg-white p-8 shadow-sm">
				<span class="absolute -top-3 right-6 rounded-full bg-amber-500/20 text-amber-700 border border-amber-400/60 px-3 py-1 text-xs font-semibold shadow">Closed beta</span>
				<h2 class="text-xl font-semibold text-gray-900">Legal Team</h2>
				<p class="mt-1 text-sm text-gray-500">For German legal-tech startups needing statutory retention.</p>
				<div class="mt-6 flex items-baseline gap-1">
					<span class="text-4xl font-bold text-gray-400">Contact us</span>
				</div>
				<a href="/legal" class="mt-6 block rounded-lg border border-gray-300 bg-white px-4 py-2.5 text-center text-sm font-semibold text-gray-700 shadow-sm hover:bg-gray-50 transition-colors">
					Learn more &rarr;
				</a>
				<ul class="mt-6 space-y-2 text-sm text-gray-600">
					<li class="flex gap-2"><span class="text-gray-400">•</span><span>Everything in Team, plus:</span></li>
					<li class="flex gap-2"><span class="text-gray-400">•</span><span><strong>Per-prefix WORM retention</strong> via S3 Object Lock — audit-defensible, admin-key-proof</span></li>
					<li class="flex gap-2"><span class="text-gray-400">•</span><span><strong>Ad-hoc retention holds</strong> — pin rows or objects when a customer cites a legal basis mid-lifetime</span></li>
					<li class="flex gap-2"><span class="text-gray-400">•</span><span><strong>Honest DSAR erasure</strong> — held items refused with basis + purgeable-after date, not silent no-op</span></li>
					<li class="flex gap-2"><span class="text-gray-400">•</span><span><strong>10-year audit-log retention</strong> matching the statutory data window</span></li>
					<li class="flex gap-2"><span class="text-gray-400">•</span><span>Ready for §50 BRAO, §257 HGB, §147 AO</span></li>
				</ul>
			</div>
		</div>
	</section>

	<!-- Comparison table -->
	<section class="mx-auto max-w-6xl px-6 pb-16">
		<h2 class="text-2xl font-semibold text-gray-900 mb-4">Full comparison</h2>
		<div class="overflow-hidden rounded-xl border border-gray-200 bg-white shadow-sm">
			<table class="w-full text-sm">
				<thead class="bg-gray-50">
					<tr>
						<th class="px-6 py-3 text-left font-medium text-gray-700"></th>
						<th class="px-6 py-3 text-center font-medium text-gray-700 w-24">Free</th>
						<th class="px-6 py-3 text-center font-medium text-gray-700 w-24">Pro</th>
						<th class="px-6 py-3 text-center font-medium text-gray-500 w-24">Team <span class="ml-1 text-[10px] uppercase tracking-wide text-amber-600">Soon</span></th>
						<th class="px-6 py-3 text-center font-medium text-gray-500 w-32">Legal Team <span class="ml-1 text-[10px] uppercase tracking-wide text-amber-600">Soon</span></th>
					</tr>
				</thead>
				<tbody class="divide-y divide-gray-200">
					{#each rows as r}
						{#if r.category}
							<tr class="bg-gray-50">
								<td colspan="5" class="px-6 py-2 text-xs font-semibold uppercase tracking-wide text-gray-500">{r.category}</td>
							</tr>
						{:else}
							<tr>
								<td class="px-6 py-3 text-gray-700">{r.label}</td>
								<td class="px-6 py-3 text-center text-gray-600">
									{#if typeof r.free === 'boolean'}
										{#if r.free}<span class="text-emerald-500">✓</span>{:else}<span class="text-gray-300">—</span>{/if}
									{:else if r.free === 'Coming soon'}
										<span class="text-xs italic text-amber-600">{r.free}</span>
									{:else}
										{r.free}
									{/if}
								</td>
								<td class="px-6 py-3 text-center text-gray-900 font-medium">
									{#if typeof r.pro === 'boolean'}
										{#if r.pro}<span class="text-eurobase-600">✓</span>{:else}<span class="text-gray-300">—</span>{/if}
									{:else if r.pro === 'Coming soon'}
										<span class="text-xs italic text-amber-600">{r.pro}</span>
									{:else}
										{r.pro}
									{/if}
								</td>
								<td class="px-6 py-3 text-center text-gray-500">
									{#if typeof r.team === 'boolean'}
										{#if r.team}<span class="text-emerald-500">✓</span>{:else}<span class="text-gray-300">—</span>{/if}
									{:else if r.team === 'Coming soon'}
										<span class="text-xs italic text-amber-600">{r.team}</span>
									{:else}
										{r.team}
									{/if}
								</td>
								<td class="px-6 py-3 text-center text-gray-500">
									{#if typeof r.legal === 'boolean'}
										{#if r.legal}<span class="text-emerald-500">✓</span>{:else}<span class="text-gray-300">—</span>{/if}
									{:else if r.legal === 'Coming soon'}
										<span class="text-xs italic text-amber-600">{r.legal}</span>
									{:else if r.legal === 'Beta'}
										<span class="text-xs italic text-emerald-700">{r.legal}</span>
									{:else}
										{r.legal}
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
