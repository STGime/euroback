<script lang="ts">
	import { page } from '$app/stores';

	let { children } = $props();

	let projectId = $derived($page.params.id);
	let currentPath = $derived($page.url.pathname);

	let activeTab = $derived(
		currentPath.endsWith('/schema') ? 'schema' :
		currentPath.endsWith('/history') ? 'history' :
		currentPath.endsWith('/sql') ? 'sql' : 'tables'
	);
</script>

<div class="mb-4 flex gap-1 border-b border-gray-200">
	<a
		href="/p/{projectId}/database"
		class="relative px-4 py-2 text-sm font-medium transition-colors
			{activeTab === 'tables' ? 'text-eurobase-700' : 'text-gray-500 hover:text-gray-700'}"
	>
		Table Editor
		{#if activeTab === 'tables'}
			<span class="absolute bottom-0 left-0 right-0 h-0.5 bg-eurobase-600 rounded-full"></span>
		{/if}
	</a>
	<a
		href="/p/{projectId}/database/sql"
		class="relative px-4 py-2 text-sm font-medium transition-colors
			{activeTab === 'sql' ? 'text-eurobase-700' : 'text-gray-500 hover:text-gray-700'}"
	>
		SQL Editor
		{#if activeTab === 'sql'}
			<span class="absolute bottom-0 left-0 right-0 h-0.5 bg-eurobase-600 rounded-full"></span>
		{/if}
	</a>
	<a
		href="/p/{projectId}/database/schema"
		class="relative px-4 py-2 text-sm font-medium transition-colors
			{activeTab === 'schema' ? 'text-eurobase-700' : 'text-gray-500 hover:text-gray-700'}"
	>
		Schema Diagram
		{#if activeTab === 'schema'}
			<span class="absolute bottom-0 left-0 right-0 h-0.5 bg-eurobase-600 rounded-full"></span>
		{/if}
	</a>
	<a
		href="/p/{projectId}/database/history"
		class="relative px-4 py-2 text-sm font-medium transition-colors
			{activeTab === 'history' ? 'text-eurobase-700' : 'text-gray-500 hover:text-gray-700'}"
	>
		Migration History
		{#if activeTab === 'history'}
			<span class="absolute bottom-0 left-0 right-0 h-0.5 bg-eurobase-600 rounded-full"></span>
		{/if}
	</a>
</div>

{@render children()}
