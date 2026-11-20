<script lang="ts">
	// "Latest: SDK x · CLI y" with links and the upgrade commands, so people
	// know which versions are current. Versions come from latestReleases().
	import { latestReleases } from '$lib/releases.svelte';

	let { compact = false }: { compact?: boolean } = $props();
	const rel = latestReleases();
</script>

{#if compact}
	<p class="text-xs text-gray-500">
		Latest:
		<a href={rel.sdkUrl} target="_blank" rel="noopener noreferrer" class="text-eurobase-700 hover:underline">SDK {rel.sdk}</a>
		·
		<a href={rel.cliUrl} target="_blank" rel="noopener noreferrer" class="text-eurobase-700 hover:underline">CLI {rel.cli}</a>
	</p>
{:else}
	<div class="rounded-lg border border-gray-200 bg-gray-50 px-3 py-2 text-xs text-gray-600 space-y-1">
		<p class="font-semibold text-gray-700">Latest releases</p>
		<p>
			<a href={rel.sdkUrl} target="_blank" rel="noopener noreferrer" class="text-eurobase-700 hover:underline">@eurobase/sdk {rel.sdk}</a>
			<br /><code class="font-mono text-[11px] text-gray-500">npm install @eurobase/sdk@latest</code>
		</p>
		<p>
			<a href={rel.cliUrl} target="_blank" rel="noopener noreferrer" class="text-eurobase-700 hover:underline">eurobase CLI {rel.cli}</a>
			<br /><code class="font-mono text-[11px] text-gray-500">brew upgrade stgime/tap/eurobase</code>
		</p>
	</div>
{/if}
