<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api.js';

	// Public, server-rendered landing for "Eurobase sign up" searches.
	// The actual form lives at /login?signup=1 (client-only, noindex);
	// this page is the indexable front door that hands off to it.
	let signedIn = $state(false);

	onMount(() => {
		signedIn = !!api.getToken();
	});

	// Keep in step with the Free column on /pricing (static fallbacks there).
	const freeTier = [
		'2 projects, 500 MB Postgres, 500 MB file storage',
		'5k monthly active users with email, magic-link and social login',
		'Edge functions, cron, webhooks and realtime',
		'DSAR export API and audit log on every project'
	];
</script>

<svelte:head>
	<title>Sign up — Eurobase · EU-sovereign backend, free to start</title>
	<meta name="description" content="Create a free Eurobase account: Postgres, auth, storage, edge functions and realtime, hosted in the EU (Scaleway, France). GDPR by design, no US CLOUD Act exposure. No credit card required." />
	<meta property="og:title" content="Sign up for Eurobase — the EU-sovereign backend" />
	<meta property="og:description" content="Postgres, auth, storage and edge functions hosted in France. Free to start, no credit card." />
	<meta property="og:type" content="website" />
	<link rel="canonical" href="https://console.eurobase.app/signup" />
</svelte:head>

<div class="min-h-screen bg-gray-50">
	<header class="border-b border-gray-200 bg-white">
		<div class="mx-auto flex max-w-6xl items-center justify-between px-6 py-4">
			<a href="https://eurobase.app" class="text-lg font-bold text-gray-900">Eurobase</a>
			<div class="flex items-center gap-3 text-sm">
				<a href="/docs" class="text-gray-600 hover:text-gray-900">Docs</a>
				<a href="/pricing" class="text-gray-600 hover:text-gray-900">Pricing</a>
				{#if signedIn}
					<a href="/projects" class="rounded-lg bg-eurobase-600 px-4 py-2 font-semibold text-white shadow-sm hover:bg-eurobase-700 transition-colors">Back to dashboard</a>
				{:else}
					<a href="/login" class="text-gray-600 hover:text-gray-900">Sign in</a>
				{/if}
			</div>
		</div>
	</header>

	<main class="mx-auto grid max-w-6xl grid-cols-1 gap-10 px-6 py-16 lg:grid-cols-2 lg:items-center">
		<section>
			<h1 class="text-4xl font-bold tracking-tight text-gray-900 sm:text-5xl">Your backend, hosted in Europe.</h1>
			<p class="mt-4 text-lg text-gray-600">
				Postgres, auth, storage, edge functions and realtime — on EU infrastructure (Scaleway, France), made in Berlin.
				GDPR by design, no US CLOUD Act exposure.
			</p>
			<div class="mt-8 flex flex-wrap items-center gap-4">
				{#if signedIn}
					<a href="/projects" class="rounded-lg bg-eurobase-600 px-6 py-3 text-base font-semibold text-white shadow-sm hover:bg-eurobase-700 transition-colors">Go to your projects</a>
				{:else}
					<a href="/login?signup=1" class="rounded-lg bg-eurobase-600 px-6 py-3 text-base font-semibold text-white shadow-sm hover:bg-eurobase-700 transition-colors">Create your free account</a>
					<a href="/login" class="text-sm text-gray-600 hover:text-gray-900">Already have an account? <span class="underline">Sign in</span></a>
				{/if}
			</div>
			<p class="mt-3 text-sm text-gray-500">No credit card required.</p>
		</section>

		<section class="rounded-xl border border-gray-200 bg-white p-6 shadow-sm">
			<h2 class="text-lg font-semibold text-gray-900">Free tier includes</h2>
			<ul class="mt-4 space-y-3 text-sm text-gray-700">
				{#each freeTier as item (item)}
					<li class="flex gap-2"><span class="text-eurobase-500">✓</span><span>{item}</span></li>
				{/each}
			</ul>
			<p class="mt-5 text-xs text-gray-500">
				Free is for personal and development use. Going to production? <a href="/pricing" class="underline hover:no-underline">Pro is €25/mo</a>.
			</p>
		</section>
	</main>
</div>
