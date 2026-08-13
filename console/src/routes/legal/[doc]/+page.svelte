<script lang="ts">
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();
</script>

<svelte:head>
	<title>{data.title} — Eurobase</title>
	<meta name="description" content={`${data.title} for Eurobase — version ${data.version}.`} />
</svelte:head>

<div class="mx-auto max-w-3xl px-6 py-12">
	<div class="mb-8 flex items-baseline justify-between border-b border-gray-200 pb-4">
		<h1 class="text-3xl font-semibold text-gray-900">{data.title}</h1>
		<span class="text-sm text-gray-500">Version {data.version}</span>
	</div>

	<!-- Rendered markdown. `data.html` is produced by marked from an
	     .md source bundled at build time; no user input feeds this. -->
	<article class="prose prose-slate max-w-none prose-headings:text-gray-900 prose-a:text-eurobase-600 prose-a:no-underline hover:prose-a:underline">
		{@html data.html}
	</article>

	<p class="mt-12 border-t border-gray-200 pt-4 text-xs text-gray-500">
		Questions about this document? Contact <a href="mailto:dpo@eurobase.app" class="text-eurobase-600 hover:text-eurobase-700 underline">dpo@eurobase.app</a>.
	</p>
</div>
