<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { api, type TableSchema } from '$lib/api.js';
	import SchemaDiagram from '$lib/components/SchemaDiagram.svelte';

	let projectId = $derived($page.params.id);
	let tables: TableSchema[] = $state([]);
	let loading = $state(true);
	let error: string | null = $state(null);

	onMount(async () => {
		try {
			const hiddenTables = new Set(['users', 'refresh_tokens', 'storage_objects']);
			tables = (await api.getSchema(projectId)).filter(t => !hiddenTables.has(t.name));
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load schema';
		} finally {
			loading = false;
		}
	});
</script>

<div class="h-[calc(100vh-13rem)] flex flex-col">
	{#if loading}
		<div class="flex-1 flex items-center justify-center text-sm text-gray-400">
			<svg class="h-5 w-5 animate-spin mr-2" viewBox="0 0 24 24" fill="none">
				<circle cx="12" cy="12" r="10" stroke="currentColor" stroke-width="3" class="opacity-25" />
				<path d="M4 12a8 8 0 018-8" stroke="currentColor" stroke-width="3" stroke-linecap="round" class="opacity-75" />
			</svg>
			Loading schema...
		</div>
	{:else if error}
		<div class="flex-1 flex items-center justify-center">
			<div class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-600">{error}</div>
		</div>
	{:else}
		<div class="flex-1 rounded-lg border border-gray-200 bg-white overflow-hidden">
			<SchemaDiagram {tables} />
		</div>
	{/if}
</div>
