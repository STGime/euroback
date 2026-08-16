<script lang="ts">
	// Dedicated marketing / SEO landing page for the Legal Team tier.
	// Purpose: rank on German-language legal-tech backend queries
	// ("DSGVO Backend Deutschland", "Kanzlei-Software Hosting",
	// "§257 HGB WORM Retention", "Rechtsanwalt SaaS EU-hosted", etc.)
	// so buyers evaluating a compliance-heavy backend stack find us
	// before they've committed to the US-hosted incumbents that
	// won't survive an auditor's first question.
	//
	// Not a technical docs page — that lives at /docs#legal-tech and
	// covers what's built. This page is the pitch to a legal-tech
	// founder who's searching for a solution to a specific statutory
	// retention problem.

	import { onMount } from 'svelte';
	import { api } from '$lib/api.js';

	let signedIn = $state(false);

	onMount(() => {
		signedIn = !!api.getToken();
	});

	// Structured-data payload rendered as JSON-LD in <svelte:head>.
	// Two schema.org types: Product for the service itself,
	// FAQPage so the "who is this for / what does WORM mean"
	// questions can surface as rich results.
	const jsonLd = {
		'@context': 'https://schema.org',
		'@graph': [
			{
				'@type': 'Product',
				name: 'Eurobase Legal Team',
				description: 'GDPR-compliant Backend-as-a-Service for German legal-tech startups. WORM-enforced retention for §50 BRAO (6y lawyer files), §257 HGB (10y books/invoices), §147 AO (10y tax records). Hosted in France on Scaleway. No US CLOUD Act exposure.',
				brand: { '@type': 'Brand', name: 'Eurobase' },
				category: 'Backend-as-a-Service',
				offers: {
					'@type': 'Offer',
					availability: 'https://schema.org/PreOrder',
					priceCurrency: 'EUR',
					url: 'https://eurobase.app/legal',
				},
			},
			{
				'@type': 'FAQPage',
				mainEntity: [
					{
						'@type': 'Question',
						name: 'What is WORM retention and why does a legal-tech startup need it?',
						acceptedAnswer: {
							'@type': 'Answer',
							text: 'WORM (Write Once, Read Many) means once an object is stored, it cannot be modified or deleted until its retention period expires — even by an admin, even by a compromised key, even by a database-level DELETE. For a firm subject to §257 HGB (10y invoice retention) or §50 BRAO (6y lawyer-file retention), that guarantee is what a German auditor will ask for. Soft delete + audit log is a nice-to-have; WORM at the storage layer is what the regulator wants to see.',
						},
					},
					{
						'@type': 'Question',
						name: 'Which German statutes does the Legal Team tier target?',
						acceptedAnswer: {
							'@type': 'Answer',
							text: '§50 BRAO (Federal Lawyers Act) — client files retained 6 years from case end. §257 HGB (Commercial Code) — books, inventories, opening balance sheets, annual accounts, and invoices retained 10 years; commercial letters retained 6 years. §147 AO (Fiscal Code) — same split for tax-relevant records. The Retention tab lets you configure per-prefix policies matching each statutory class.',
						},
					},
					{
						'@type': 'Question',
						name: 'How is this different from just backing up my data?',
						acceptedAnswer: {
							'@type': 'Answer',
							text: 'Backups are recoverable — good for accident recovery, not good enough for statutory retention. A backup can be deleted, modified, or restored to a state where the data is gone. WORM Object Lock refuses the DELETE at the storage boundary. It is the difference between "we can probably restore this" and "the storage service is contractually incapable of losing this within the retention window".',
						},
					},
					{
						'@type': 'Question',
						name: 'What happens when a user requests DSAR erasure on data that is under a legal retention hold?',
						acceptedAnswer: {
							'@type': 'Answer',
							text: 'The erasure API refuses each held item with a specific message the requester sees in their export: "retained under §257 HGB, purgeable after 2036-03-14". No silent no-op. No ambiguous "we removed everything we could". The exporter enumerates every held item plus its basis plus the earliest purge date so the user can plan a follow-up request. This is the honest interpretation of Article 17(3)(b) GDPR (legal obligation to retain).',
						},
					},
					{
						'@type': 'Question',
						name: 'Where is the data hosted?',
						acceptedAnswer: {
							'@type': 'Answer',
							text: 'Scaleway data centres in Paris, France (fr-par region). No US infrastructure at any point in the stack. Sub-processor list published per project as part of the Article 30 DPA report the console auto-generates.',
						},
					},
				],
			},
		],
	};
</script>

<svelte:head>
	<title>Legal-tech backend for German startups — WORM retention, §257 HGB, §50 BRAO, §147 AO | Eurobase</title>
	<meta
		name="description"
		content="GDPR-compliant Backend-as-a-Service for German legal-tech startups. WORM-enforced retention for §50 BRAO (6y), §257 HGB (10y invoices), §147 AO (10y tax records). Hosted in France on Scaleway — no US CLOUD Act exposure. Postgres, storage, auth, edge functions, all with an audit-defensible retention story out of the box."
	/>
	<meta
		name="keywords"
		content="Legal-tech Backend Deutschland, DSGVO Backend, GDPR compliant BaaS, §257 HGB Retention Software, §50 BRAO Aktenaufbewahrung, §147 AO Speicherfrist, WORM Object Lock EU, Kanzlei-Software Hosting Deutschland, Rechtsanwalt SaaS DSGVO, EU-hosted Postgres for law firms, S3 Object Lock France, DSAR Erasure Legal Hold, Article 17 GDPR compliance, MaRisk AT 7.2 storage, Buchführungspflicht Retention"
	/>
	<meta property="og:title" content="Legal-tech backend for German startups — Eurobase" />
	<meta
		property="og:description"
		content="WORM-enforced retention for §50 BRAO / §257 HGB / §147 AO. EU-hosted (France, Scaleway). Made in Berlin. Beta invite via contact@eurobase.app."
	/>
	<meta property="og:type" content="website" />
	<meta property="og:url" content="https://eurobase.app/legal" />
	<meta name="twitter:card" content="summary_large_image" />
	<link rel="canonical" href="https://eurobase.app/legal" />
	{@html `<script type="application/ld+json">${JSON.stringify(jsonLd)}<\/script>`}
</svelte:head>

<div class="min-h-screen bg-gray-50">
	<!-- Header -->
	<header class="border-b border-gray-200 bg-white">
		<div class="mx-auto flex max-w-6xl items-center justify-between px-6 py-4">
			<a href="/" class="text-lg font-bold text-gray-900">Eurobase</a>
			<div class="flex items-center gap-3 text-sm">
				<a href="/pricing" class="text-gray-600 hover:text-gray-900">Pricing</a>
				<a href="/docs#legal-tech" class="text-gray-600 hover:text-gray-900">Docs</a>
				{#if signedIn}
					<a href="/projects" class="rounded-lg bg-eurobase-600 px-4 py-2 font-semibold text-white shadow-sm hover:bg-eurobase-700 transition-colors">Back to dashboard</a>
				{:else}
					<a href="/login" class="text-gray-600 hover:text-gray-900">Sign in</a>
					<a href="mailto:contact@eurobase.app?subject=Legal%20Team%20beta%20access" class="rounded-lg bg-eurobase-600 px-4 py-2 font-semibold text-white shadow-sm hover:bg-eurobase-700 transition-colors">Request beta access</a>
				{/if}
			</div>
		</div>
	</header>

	<!-- Hero -->
	<section class="mx-auto max-w-4xl px-6 pt-16 pb-8 text-center">
		<span class="inline-block rounded-full bg-amber-100 px-3 py-1 text-xs font-semibold uppercase tracking-wide text-amber-800">Legal Team · closed beta</span>
		<h1 class="mt-4 text-4xl font-bold tracking-tight text-gray-900 sm:text-5xl">
			GDPR-compliant backend for German legal-tech startups.
		</h1>
		<p class="mt-4 text-lg text-gray-600">
			WORM-enforced retention for <strong>§50 BRAO</strong>, <strong>§257 HGB</strong>, and <strong>§147 AO</strong>. Hosted in France on Scaleway — no US CLOUD Act exposure. Postgres, storage, auth, and edge functions with an audit-defensible retention story out of the box.
		</p>
		<div class="mt-8 flex flex-col items-center gap-3 sm:flex-row sm:justify-center">
			<a href="mailto:contact@eurobase.app?subject=Legal%20Team%20beta%20access" class="rounded-lg bg-eurobase-600 px-6 py-3 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 transition-colors">
				Request beta access
			</a>
			<a href="/docs#legal-tech" class="rounded-lg border border-gray-300 bg-white px-6 py-3 text-sm font-semibold text-gray-700 shadow-sm hover:bg-gray-50 transition-colors">
				Read the technical docs
			</a>
		</div>
	</section>

	<!-- Problem framing -->
	<section class="mx-auto max-w-4xl px-6 pb-10">
		<div class="rounded-2xl border border-gray-200 bg-white p-8">
			<h2 class="text-xl font-semibold text-gray-900">The problem</h2>
			<p class="mt-3 text-sm text-gray-700 leading-relaxed">
				If you build software for lawyers, tax advisors, or Buchhaltungspflichtige (bookkeeping-obligated) SMBs in Germany, your customers are on the hook for statutory data retention that a generic backend can't defend. When a Kanzlei is audited, "we back everything up nightly" isn't the right answer — the auditor wants to see that the specific record class the statute mentions <em>cannot</em> be deleted within the retention window, not that it <em>hasn't been</em>.
			</p>
			<p class="mt-3 text-sm text-gray-700 leading-relaxed">
				Building that on top of Firebase, Supabase, or a raw AWS S3 bucket means writing your own Object Lock configuration, your own retention-hold API, your own DSAR-erasure-that-respects-legal-holds workflow, and — most importantly — your own audit-log retention that survives the same statutory window as the data it describes. That's months of work, most of which will be reviewed by a lawyer who bills in 6-minute increments.
			</p>
			<p class="mt-3 text-sm text-gray-700 leading-relaxed">
				Eurobase Legal Team is a pre-built version of that stack. WORM by default on the record classes that need it, ad-hoc holds for the ones that don't, and an honest DSAR-erasure API that returns "retained under §257 HGB, purgeable after 2036-03-14" instead of a silent no-op that will hold up in a Landesdatenschutzbeauftragte's inbox for three months.
			</p>
		</div>
	</section>

	<!-- What you get -->
	<section class="mx-auto max-w-4xl px-6 pb-10">
		<h2 class="text-2xl font-semibold text-gray-900">What Legal Team gives you</h2>
		<div class="mt-6 grid grid-cols-1 gap-4 sm:grid-cols-2">
			<div class="rounded-xl border border-gray-200 bg-white p-6">
				<h3 class="text-base font-semibold text-gray-900">Per-prefix WORM policies</h3>
				<p class="mt-2 text-sm text-gray-600">Every object under a configured prefix (e.g. <code class="rounded bg-gray-100 px-1 text-[11px]">/invoices/*</code>) is retention-locked under S3 Object Lock. Even a compromised admin key cannot delete before the retention date. Governance-mode vs compliance-mode is a per-prefix choice.</p>
			</div>
			<div class="rounded-xl border border-gray-200 bg-white p-6">
				<h3 class="text-base font-semibold text-gray-900">Ad-hoc retention holds</h3>
				<p class="mt-2 text-sm text-gray-600">When a customer cites a legal basis mid-lifetime ("this row is subject to litigation hold"), the console's Retention tab pins the specific row, object, or table beyond its default policy. Every hold is audited with actor + basis + expected release date.</p>
			</div>
			<div class="rounded-xl border border-gray-200 bg-white p-6">
				<h3 class="text-base font-semibold text-gray-900">Honest DSAR erasure</h3>
				<p class="mt-2 text-sm text-gray-600">When a user asks to be forgotten, held items are <em>refused</em> with a specific message the requester sees in their export: <em>"retained under §257 HGB, purgeable after 2036-03-14."</em> No silent no-op; no ambiguous "we removed everything we could."</p>
			</div>
			<div class="rounded-xl border border-gray-200 bg-white p-6">
				<h3 class="text-base font-semibold text-gray-900">10-year audit-log retention</h3>
				<p class="mt-2 text-sm text-gray-600">The audit trail survives the same statutory window as the data it describes. On standard tiers audit-log retention is 90 days; on Legal Team it matches §257 HGB / §147 AO. Hash-chained checkpoints for tamper-evidence.</p>
			</div>
		</div>
	</section>

	<!-- Statutes -->
	<section class="mx-auto max-w-4xl px-6 pb-10">
		<h2 class="text-2xl font-semibold text-gray-900">Statutes covered</h2>
		<div class="mt-6 space-y-4">
			<div class="rounded-xl border border-gray-200 bg-white p-6">
				<div class="flex items-center gap-3">
					<span class="rounded-md bg-eurobase-100 px-2 py-0.5 text-xs font-semibold text-eurobase-800">§50 BRAO</span>
					<h3 class="text-base font-semibold text-gray-900">Federal Lawyers' Act — client files</h3>
				</div>
				<p class="mt-2 text-sm text-gray-600">Client files (Handakten) retained <strong>6 years</strong> from case end. Configurable per-project retention policy on the <code class="rounded bg-gray-100 px-1 text-[11px]">/client-files/*</code> prefix.</p>
			</div>
			<div class="rounded-xl border border-gray-200 bg-white p-6">
				<div class="flex items-center gap-3">
					<span class="rounded-md bg-eurobase-100 px-2 py-0.5 text-xs font-semibold text-eurobase-800">§257 HGB</span>
					<h3 class="text-base font-semibold text-gray-900">Commercial Code — books, invoices, letters</h3>
				</div>
				<p class="mt-2 text-sm text-gray-600">Split by record class: books, inventories, opening balance sheets, annual accounts, and invoices (Buchungsbelege) retained <strong>10 years</strong>; received / sent commercial letters (Handelsbriefe) retained <strong>6 years</strong>. Both prefixes ship with the correct default policy.</p>
			</div>
			<div class="rounded-xl border border-gray-200 bg-white p-6">
				<div class="flex items-center gap-3">
					<span class="rounded-md bg-eurobase-100 px-2 py-0.5 text-xs font-semibold text-eurobase-800">§147 AO</span>
					<h3 class="text-base font-semibold text-gray-900">Fiscal Code — tax-relevant records</h3>
				</div>
				<p class="mt-2 text-sm text-gray-600">Mirrors §257 HGB: <strong>10 years</strong> for books and accounting records, <strong>6 years</strong> for other tax-relevant business correspondence. Tax-audit-ready retention on the <code class="rounded bg-gray-100 px-1 text-[11px]">/tax/*</code> prefix.</p>
			</div>
		</div>
		<p class="mt-4 text-xs text-gray-500">
			Statutory summaries are for orientation, not legal advice — your firm's counsel is the source of truth for the specific record classes and periods that apply to your workload. Eurobase provides the WORM enforcement mechanism; the retention decision itself is yours.
		</p>
	</section>

	<!-- Sovereignty -->
	<section class="mx-auto max-w-4xl px-6 pb-10">
		<div class="rounded-2xl border border-gray-200 bg-white p-8">
			<h2 class="text-xl font-semibold text-gray-900">Sovereignty is a first-class feature, not a certificate</h2>
			<p class="mt-3 text-sm text-gray-700 leading-relaxed">
				Every byte of Eurobase data lives in Scaleway data centres in Paris, France (<code class="rounded bg-gray-100 px-1 text-[11px]">fr-par</code>). No US-hosted services in the stack — no AWS, no GCP, no Cloudflare, no Vercel, no Stripe. That means no CLOUD Act reach, no Schrems II ambiguity, no "your data is technically encrypted at rest but the encryption keys sit in a US KMS" workaround.
			</p>
			<p class="mt-3 text-sm text-gray-700 leading-relaxed">
				Payment processing runs on Mollie (Dutch, EU-headquartered). SMS OTP runs on GatewayAPI (Danish). Every third-party processor is listed on the auto-generated Article 30 DPA report you can download per project. If a customer's Datenschutzbeauftragte(r) asks for it, you send them a PDF instead of a 40-email thread.
			</p>
		</div>
	</section>

	<!-- FAQ (visible; also indexed via JSON-LD above) -->
	<section class="mx-auto max-w-4xl px-6 pb-16">
		<h2 class="text-2xl font-semibold text-gray-900">Frequently asked</h2>
		<div class="mt-6 space-y-4">
			<details class="rounded-xl border border-gray-200 bg-white p-6 [&_summary]:cursor-pointer">
				<summary class="text-base font-semibold text-gray-900">What is WORM retention and why does a legal-tech startup need it?</summary>
				<p class="mt-3 text-sm text-gray-700 leading-relaxed">
					WORM (Write Once, Read Many) means once an object is stored, it cannot be modified or deleted until its retention period expires — even by an admin, even by a compromised key, even by a database-level DELETE. For a firm subject to §257 HGB (10y invoice retention) or §50 BRAO (6y lawyer-file retention), that guarantee is what a German auditor will ask for. Soft delete plus audit log is a nice-to-have; WORM at the storage layer is what the regulator wants to see.
				</p>
			</details>
			<details class="rounded-xl border border-gray-200 bg-white p-6 [&_summary]:cursor-pointer">
				<summary class="text-base font-semibold text-gray-900">How is this different from just backing up my data?</summary>
				<p class="mt-3 text-sm text-gray-700 leading-relaxed">
					Backups are recoverable — good for accident recovery, not good enough for statutory retention. A backup can be deleted, modified, or restored to a state where the data is gone. WORM Object Lock refuses the DELETE at the storage boundary. It is the difference between "we can probably restore this" and "the storage service is contractually incapable of losing this within the retention window."
				</p>
			</details>
			<details class="rounded-xl border border-gray-200 bg-white p-6 [&_summary]:cursor-pointer">
				<summary class="text-base font-semibold text-gray-900">What happens when a user requests DSAR erasure on data under a legal hold?</summary>
				<p class="mt-3 text-sm text-gray-700 leading-relaxed">
					The erasure API refuses each held item with a specific message the requester sees in their export: <em>"retained under §257 HGB, purgeable after 2036-03-14."</em> The exporter enumerates every held item plus its basis plus the earliest purge date so the user can plan a follow-up request. Honest interpretation of Article 17(3)(b) GDPR (legal obligation to retain), rather than a silent no-op that will hold up in a Landesdatenschutzbeauftragte's inbox for three months.
				</p>
			</details>
			<details class="rounded-xl border border-gray-200 bg-white p-6 [&_summary]:cursor-pointer">
				<summary class="text-base font-semibold text-gray-900">When can I sign up?</summary>
				<p class="mt-3 text-sm text-gray-700 leading-relaxed">
					Legal Team is currently in closed beta. Email <a href="mailto:contact@eurobase.app?subject=Legal%20Team%20beta%20access" class="text-eurobase-700 hover:text-eurobase-800 underline">contact@eurobase.app</a> with a one-line description of your workload and your retention basis (BRAO / HGB / AO / other). Grants are manual during the beta window; pricing is set per-workload until we exit beta.
				</p>
			</details>
			<details class="rounded-xl border border-gray-200 bg-white p-6 [&_summary]:cursor-pointer">
				<summary class="text-base font-semibold text-gray-900">Is my data really EU-only?</summary>
				<p class="mt-3 text-sm text-gray-700 leading-relaxed">
					Yes — Scaleway data centres in Paris, France. No US infrastructure at any point in the stack. Full sub-processor list published on the Article 30 DPA report the console auto-generates per project. If a component of the stack ever changes region, we file a versioned DPA update and notify affected projects.
				</p>
			</details>
		</div>
	</section>

	<!-- Final CTA -->
	<section class="border-t border-gray-200 bg-white">
		<div class="mx-auto max-w-4xl px-6 py-14 text-center">
			<h2 class="text-2xl font-semibold text-gray-900">Ready to talk?</h2>
			<p class="mt-3 text-sm text-gray-600 max-w-2xl mx-auto">
				One-line description of your workload plus your retention basis in an email, and we'll get back to you the same day about beta access and rough pricing.
			</p>
			<div class="mt-6">
				<a href="mailto:contact@eurobase.app?subject=Legal%20Team%20beta%20access" class="inline-block rounded-lg bg-eurobase-600 px-6 py-3 text-sm font-semibold text-white shadow-sm hover:bg-eurobase-700 transition-colors">
					Email contact@eurobase.app
				</a>
			</div>
			<p class="mt-6 text-xs text-gray-500">
				Prefer to see the technical details first? Read the <a href="/docs#legal-tech" class="text-eurobase-700 hover:text-eurobase-800 underline">Legal Team docs</a> or the <a href="/pricing" class="text-eurobase-700 hover:text-eurobase-800 underline">full pricing comparison</a>.
			</p>
		</div>
	</section>
</div>
