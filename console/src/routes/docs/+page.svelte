<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { chapterById, pageChapters } from '$lib/docs/registry';
	import Welcome from '$lib/docs/chapters/00-welcome.svelte';

	const ORIGIN = 'https://console.eurobase.app';
	const title = 'Eurobase Documentation';
	const description =
		'Step-by-step guide to building on Eurobase, the EU-sovereign backend: projects, Postgres, storage, auth, row-level security, edge functions, cron, webhooks, migrations, compliance, organizations and SSO.';

	// The docs used to be a single page addressed by #hash. Keep every old
	// deep link working by forwarding /docs#chapter to /docs/chapter.
	onMount(() => {
		const h = location.hash.replace(/^#/, '');
		if (h && h !== 'welcome' && chapterById[h]) {
			goto(`/docs/${h}`, { replaceState: true });
		}
	});

	const jsonLd = JSON.stringify({
		'@context': 'https://schema.org',
		'@type': 'WebSite',
		name: title,
		url: `${ORIGIN}/docs`,
		description,
		publisher: { '@type': 'Organization', name: 'Eurobase OÜ', url: 'https://eurobase.app' }
	});
</script>

<svelte:head>
	<title>{title}</title>
	<meta name="description" content={description} />
	<link rel="canonical" href="{ORIGIN}/docs" />
	<meta property="og:type" content="website" />
	<meta property="og:title" content={title} />
	<meta property="og:description" content={description} />
	<meta property="og:url" content="{ORIGIN}/docs" />
	{@html `<script type="application/ld+json">${jsonLd}</script>`}
</svelte:head>

<div class="space-y-16">
	<Welcome />

	<section aria-labelledby="all-chapters">
		<h2 id="all-chapters" class="text-2xl font-bold text-gray-900 mb-4">All chapters</h2>
		<ol class="grid grid-cols-1 sm:grid-cols-2 gap-3">
			{#each pageChapters as ch}
				<li>
					<a href="/docs/{ch.id}" class="block h-full rounded-lg border border-gray-200 bg-white px-4 py-3 hover:border-eurobase-300 transition-colors">
						<span class="block text-sm font-medium text-gray-900">{ch.label}</span>
						<span class="mt-1 block text-xs text-gray-500 line-clamp-2">{ch.description}</span>
					</a>
				</li>
			{/each}
		</ol>
	</section>
</div>
