<script lang="ts">
	let { data } = $props();

	const ORIGIN = 'https://console.eurobase.app';
	let url = $derived(`${ORIGIN}/docs/${data.chapter.id}`);
	let title = $derived(`${data.chapter.title} | Eurobase Docs`);

	// TechArticle + breadcrumbs: gives answer engines a typed, citable unit
	// per chapter instead of one undifferentiated page.
	let jsonLd = $derived(
		JSON.stringify([
			{
				'@context': 'https://schema.org',
				'@type': 'TechArticle',
				headline: data.chapter.title,
				description: data.chapter.description,
				url,
				inLanguage: 'en',
				isPartOf: { '@type': 'WebSite', name: 'Eurobase Documentation', url: `${ORIGIN}/docs` },
				publisher: { '@type': 'Organization', name: 'Eurobase OÜ', url: 'https://eurobase.app' }
			},
			{
				'@context': 'https://schema.org',
				'@type': 'BreadcrumbList',
				itemListElement: [
					{ '@type': 'ListItem', position: 1, name: 'Eurobase', item: 'https://eurobase.app' },
					{ '@type': 'ListItem', position: 2, name: 'Documentation', item: `${ORIGIN}/docs` },
					{ '@type': 'ListItem', position: 3, name: data.chapter.title, item: url }
				]
			}
		])
	);
</script>

<svelte:head>
	<title>{title}</title>
	<meta name="description" content={data.chapter.description} />
	<link rel="canonical" href={url} />
	<meta property="og:type" content="article" />
	<meta property="og:title" content={title} />
	<meta property="og:description" content={data.chapter.description} />
	<meta property="og:url" content={url} />
	{@html `<script type="application/ld+json">${jsonLd}</script>`}
</svelte:head>

<article>
	<data.Component />
</article>

<nav class="mt-12 flex items-center justify-between border-t border-gray-200 pt-6 text-sm" aria-label="Chapter navigation">
	{#if data.prev}
		<a href="/docs/{data.prev.id}" class="text-eurobase-700 hover:text-eurobase-800 font-medium">&larr; {data.prev.label}</a>
	{:else}
		<a href="/docs" class="text-eurobase-700 hover:text-eurobase-800 font-medium">&larr; Documentation home</a>
	{/if}
	{#if data.next}
		<a href="/docs/{data.next.id}" class="text-eurobase-700 hover:text-eurobase-800 font-medium">{data.next.label} &rarr;</a>
	{/if}
</nav>
