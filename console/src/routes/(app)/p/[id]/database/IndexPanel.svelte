<script lang="ts">
	import { api, type ColumnInfo, type IndexInfo } from '$lib/api.js';

	let {
		projectId,
		tableName,
		columns = [],
		indexes = [],
		onChanged
	}: {
		projectId: string;
		tableName: string;
		columns: ColumnInfo[];
		indexes: IndexInfo[];
		onChanged: () => void;
	} = $props();

	let expanded = $state(false);
	let newColumn = $state('');
	let newUnique = $state(false);
	let creating = $state(false);
	let error: string | null = $state(null);

	async function handleCreate() {
		if (!newColumn) return;
		creating = true;
		error = null;
		try {
			await api.createIndex(projectId, tableName, newColumn, newUnique);
			newColumn = '';
			newUnique = false;
			onChanged();
		} catch (err) {
			const raw = err instanceof Error ? err.message : String(err);
			const jsonMatch = raw.match(/\{"error":"(.+?)"\}/);
			error = jsonMatch ? jsonMatch[1] : raw;
		} finally {
			creating = false;
		}
	}

	async function handleDrop(indexName: string) {
		error = null;
		try {
			await api.dropIndex(projectId, tableName, indexName);
			onChanged();
		} catch (err) {
			const raw = err instanceof Error ? err.message : String(err);
			const jsonMatch = raw.match(/\{"error":"(.+?)"\}/);
			error = jsonMatch ? jsonMatch[1] : raw;
		}
	}
</script>

<div class="mt-3 rounded-lg border border-gray-200 bg-white overflow-hidden">
	<button
		type="button"
		class="cursor-pointer w-full flex items-center justify-between px-4 py-2.5 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors"
		onclick={() => (expanded = !expanded)}
	>
		<div class="flex items-center gap-2">
			<svg class="h-4 w-4 text-gray-400 transition-transform {expanded ? 'rotate-90' : ''}" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" d="m8.25 4.5 7.5 7.5-7.5 7.5" />
			</svg>
			Indexes
			<span class="text-xs text-gray-400">({indexes.length})</span>
		</div>
	</button>

	{#if expanded}
		<div class="border-t border-gray-200 px-4 py-3 space-y-3">
			{#if error}
				<div class="flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-3 py-2">
					<svg class="h-4 w-4 mt-0.5 shrink-0 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
					</svg>
					<p class="text-sm text-red-700">{error}</p>
				</div>
			{/if}

			<!-- Index list -->
			{#if indexes.length === 0}
				<p class="text-xs text-gray-400">No indexes defined.</p>
			{:else}
				<div class="space-y-1">
					{#each indexes as idx}
						<div class="flex items-center justify-between rounded-lg bg-gray-50 px-3 py-2">
							<div class="flex items-center gap-2">
								<code class="text-xs font-mono text-gray-700">{idx.name}</code>
								<span class="text-xs text-gray-400">on <strong>{idx.column}</strong></span>
								{#if idx.is_unique}
									<span class="rounded px-1 py-0.5 text-[9px] font-semibold bg-teal-100 text-teal-700">UNIQUE</span>
								{/if}
							</div>
							<button
								type="button"
								class="cursor-pointer rounded p-1 text-gray-300 hover:bg-red-50 hover:text-red-500 transition-colors"
								onclick={() => handleDrop(idx.name)}
								title="Drop index"
							>
								<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
									<path stroke-linecap="round" stroke-linejoin="round" d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0" />
								</svg>
							</button>
						</div>
					{/each}
				</div>
			{/if}

			<!-- Create index form -->
			<div class="flex items-center gap-2 pt-1">
				<select
					bind:value={newColumn}
					class="flex-1 rounded-lg border border-gray-300 bg-white px-2 py-1.5 text-sm text-gray-700 focus:border-eurobase-500 focus:outline-none cursor-pointer"
				>
					<option value="">Column...</option>
					{#each columns as col}
						<option value={col.name}>{col.name}</option>
					{/each}
				</select>
				<label class="flex items-center gap-1.5 text-xs text-gray-600 cursor-pointer shrink-0">
					<input type="checkbox" bind:checked={newUnique} class="h-3.5 w-3.5 rounded border-gray-300 text-teal-600 cursor-pointer" />
					Unique
				</label>
				<button
					type="button"
					class="cursor-pointer shrink-0 rounded-lg bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors disabled:opacity-50"
					disabled={!newColumn || creating}
					onclick={handleCreate}
				>
					{creating ? 'Creating...' : 'Create Index'}
				</button>
			</div>
		</div>
	{/if}
</div>
