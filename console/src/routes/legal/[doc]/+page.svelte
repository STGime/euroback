<script lang="ts">
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();
</script>

<svelte:head>
	<title>{data.title} — Eurobase</title>
	<meta name="description" content={`${data.title} for Eurobase — version ${data.version}.`} />
</svelte:head>

<div class="mx-auto max-w-3xl px-6 py-12">
	<!-- The documents cross-reference each other by path (the DPA cites
	     /legal/sub-processors in Section 7 and Annex 3); give readers the
	     same paths as real links. -->
	<nav class="mb-6 flex flex-wrap gap-x-4 gap-y-1 text-sm" aria-label="Legal documents">
		<a href="/legal/dpa" class="text-eurobase-600 hover:text-eurobase-700 underline">Data Processing Agreement</a>
		<a href="/legal/terms" class="text-eurobase-600 hover:text-eurobase-700 underline">Terms of Service</a>
		<a href="/legal/privacy" class="text-eurobase-600 hover:text-eurobase-700 underline">Privacy Policy</a>
		<a href="/legal/sub-processors" class="text-eurobase-600 hover:text-eurobase-700 underline">Sub-processors</a>
	</nav>

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
