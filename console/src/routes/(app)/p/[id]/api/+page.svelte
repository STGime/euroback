<script lang="ts">
	import { getContext } from 'svelte';
	import type { Project } from '$lib/api.js';

	const projectCtx = getContext<{ id: string; project: Project | null }>('projectId');
</script>

<div class="mx-auto max-w-4xl">
	<div class="rounded-xl border border-gray-200 bg-white p-8 text-center">
		<div class="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-eurobase-100">
			<svg class="h-6 w-6 text-eurobase-600" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" d="M17.25 6.75 22.5 12l-5.25 5.25m-10.5 0L1.5 12l5.25-5.25m7.5-3-4.5 16.5" />
			</svg>
		</div>
		<h2 class="mt-4 text-lg font-semibold text-gray-900">API Explorer</h2>
		<p class="mt-2 text-sm text-gray-500">
			Interactive API documentation auto-generated from your schema. Try endpoints, view SDK code snippets, and manage API keys.
		</p>
		{#if projectCtx.project}
			<div class="mt-6 rounded-lg bg-gray-50 p-4 text-left">
				<p class="text-xs font-medium text-gray-500 uppercase">API Endpoint</p>
				<p class="mt-1 font-mono text-sm text-gray-900">https://{projectCtx.project.slug}.eurobase.app</p>
			</div>
		{/if}
		<p class="mt-6 text-xs text-gray-400">Coming in Phase 3</p>
	</div>
</div>
