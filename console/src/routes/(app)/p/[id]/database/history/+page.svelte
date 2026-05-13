<script lang="ts">
	import { page } from '$app/stores';
	import { onMount } from 'svelte';
	import { api, type SchemaChange } from '$lib/api.js';

	let projectId = $derived($page.params.id);
	let changes: SchemaChange[] = $state([]);
	let loading = $state(true);
	let error: string | null = $state(null);

	onMount(async () => {
		try {
			changes = await api.getSchemaChanges(projectId);
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load history';
		}
		loading = false;
	});

	function actionLabel(action: string): string {
		switch (action) {
			case 'create_table': return 'Created table';
			case 'drop_table': return 'Dropped table';
			case 'add_column': return 'Added column';
			case 'drop_column': return 'Dropped column';
			default: return action;
		}
	}

	function actionColor(action: string): string {
		switch (action) {
			case 'create_table': return 'bg-green-100 text-green-700';
			case 'drop_table': return 'bg-red-100 text-red-700';
			case 'add_column': return 'bg-blue-100 text-blue-700';
			case 'drop_column': return 'bg-amber-100 text-amber-700';
			default: return 'bg-gray-100 text-gray-700';
		}
	}

	function actionIcon(action: string): string {
		switch (action) {
			case 'create_table': return '+';
			case 'drop_table': return '×';
			case 'add_column': return '+';
			case 'drop_column': return '−';
			default: return '•';
		}
	}

	function formatDetail(change: SchemaChange): string {
		if (change.action === 'add_column' && change.detail) {
			const d = change.detail;
			let s = `${change.column_name} ${d.type ?? ''}`;
			if (d.nullable === false) s += ' NOT NULL';
			if (d.default) s += ` DEFAULT ${d.default}`;
			return s.trim();
		}
		if (change.action === 'create_table' && change.detail?.columns) {
			const cols = change.detail.columns as { name: string; type: string }[];
			return cols.map(c => c.name).join(', ');
		}
		if (change.action === 'drop_column' && change.column_name) {
			return change.column_name;
		}
		return '';
	}
</script>

<div class="mx-auto max-w-4xl">
	{#if loading}
		<div class="space-y-3">
			{#each Array(5) as _}
				<div class="h-16 animate-pulse rounded-lg bg-gray-100"></div>
			{/each}
		</div>
	{:else if error}
		<div class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">{error}</div>
	{:else if changes.length === 0}
		<div class="rounded-xl border border-gray-200 bg-white p-12 text-center">
			<svg class="mx-auto h-12 w-12 text-gray-300" fill="none" viewBox="0 0 24 24" stroke-width="1" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" d="M12 6v6h4.5m4.5 0a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" />
			</svg>
			<h3 class="mt-3 text-sm font-semibold text-gray-700">No schema changes yet</h3>
			<p class="mt-1 text-sm text-gray-400">Changes will appear here when you create tables, add columns, or modify the schema.</p>
		</div>
	{:else}
		<div class="relative">
			<!-- Timeline line -->
			<div class="absolute left-5 top-0 bottom-0 w-px bg-gray-200"></div>

			<div class="space-y-4">
				{#each changes as change}
					<div class="relative flex gap-4 pl-12">
						<!-- Timeline dot -->
						<div class="absolute left-3.5 top-3 flex h-3 w-3 items-center justify-center rounded-full ring-4 ring-white
							{change.action.includes('drop') ? 'bg-red-400' :
							 change.action.includes('create') ? 'bg-green-400' : 'bg-blue-400'}">
						</div>

						<div class="flex-1 rounded-lg border border-gray-200 bg-white px-4 py-3">
							<div class="flex items-center gap-2">
								<span class="inline-flex rounded px-1.5 py-0.5 text-[10px] font-bold {actionColor(change.action)}">
									{actionIcon(change.action)}
								</span>
								<span class="text-sm font-medium text-gray-900">
									{actionLabel(change.action)}
								</span>
								<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">{change.table_name}</code>
								{#if change.column_name && change.action.includes('column')}
									<span class="text-xs text-gray-400">.</span>
									<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-700">{change.column_name}</code>
								{/if}
								<span class="ml-auto text-[11px] text-gray-400">
									{new Date(change.created_at).toLocaleString('en-GB', { month: 'short', day: 'numeric', year: 'numeric', hour: '2-digit', minute: '2-digit' })}
								</span>
							</div>
							{#if formatDetail(change)}
								<p class="mt-1.5 text-xs font-mono text-gray-500">{formatDetail(change)}</p>
							{/if}
						</div>
					</div>
				{/each}
			</div>
		</div>
	{/if}
</div>
