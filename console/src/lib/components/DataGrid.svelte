<script lang="ts">
	import type { ColumnInfo } from '$lib/api.js';

	let {
		columns = [],
		rows = [],
		loading = false,
		onEditRow,
		onDeleteRow
	}: {
		columns: ColumnInfo[];
		rows: any[];
		loading: boolean;
		onEditRow?: (row: any) => void;
		onDeleteRow?: (row: any) => void;
	} = $props();

	let copiedId: string | null = $state(null);

	function formatCell(value: any, type: string): string {
		if (value === null || value === undefined) return '';
		if (type === 'uuid') return String(value).substring(0, 8) + '...';
		if (type === 'boolean') return '';
		if (type.includes('timestamp') || type === 'timestamptz') {
			return formatRelativeTime(value);
		}
		if (type === 'jsonb' || type === 'json') {
			return typeof value === 'string' ? value : JSON.stringify(value);
		}
		return String(value);
	}

	function isTimestamp(type: string): boolean {
		return type.includes('timestamp') || type === 'timestamptz';
	}

	function formatRelativeTime(dateStr: string): string {
		try {
			const date = new Date(dateStr);
			if (isNaN(date.getTime())) return dateStr;
			const now = new Date();
			const diffMs = now.getTime() - date.getTime();

			if (diffMs < 0) {
				// Future date — just show the date
				return date.toLocaleDateString('en-GB', { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
			}

			const diffSec = Math.floor(diffMs / 1000);
			const diffMin = Math.floor(diffSec / 60);
			const diffHr = Math.floor(diffMin / 60);
			const diffDay = Math.floor(diffHr / 24);

			if (diffSec < 60) return `${diffSec}s ago`;
			if (diffMin < 60) return `${diffMin}m ago`;
			if (diffHr < 24) return `${diffHr}h ago`;
			if (diffDay < 30) return `${diffDay}d ago`;
			return date.toLocaleDateString('en-GB', { year: 'numeric', month: 'short', day: 'numeric' });
		} catch {
			return String(dateStr);
		}
	}

	function isNull(value: any): boolean {
		return value === null || value === undefined;
	}

	function isBool(type: string): boolean {
		return type === 'boolean';
	}

	function isUuid(type: string): boolean {
		return type === 'uuid';
	}

	function isJson(type: string): boolean {
		return type === 'jsonb' || type === 'json';
	}

	async function copyToClipboard(text: string) {
		try {
			await navigator.clipboard.writeText(text);
			copiedId = text;
			setTimeout(() => (copiedId = null), 1500);
		} catch {
			// clipboard not available
		}
	}

	function shortType(type: string): string {
		if (type === 'timestamp with time zone') return 'timestamptz';
		if (type === 'timestamp without time zone') return 'timestamp';
		if (type === 'character varying') return 'varchar';
		if (type === 'double precision') return 'float8';
		return type;
	}

	function typeBadgeColor(type: string): string {
		if (type === 'uuid') return 'bg-purple-100 text-purple-700';
		if (type.includes('int') || type === 'numeric' || type === 'real' || type === 'bigint')
			return 'bg-blue-100 text-blue-700';
		if (type === 'boolean') return 'bg-amber-100 text-amber-700';
		if (type.includes('timestamp') || type === 'timestamptz') return 'bg-green-100 text-green-700';
		if (type === 'jsonb' || type === 'json') return 'bg-orange-100 text-orange-700';
		if (type === 'text' || type.includes('char')) return 'bg-gray-100 text-gray-700';
		return 'bg-gray-100 text-gray-600';
	}
</script>

<div class="overflow-x-auto rounded-lg border border-gray-200 bg-white">
	<table class="min-w-full divide-y divide-gray-200">
		<thead class="bg-gray-50">
			<tr>
				{#each columns as col}
					<th
						class="px-4 py-3 text-left text-xs font-semibold uppercase tracking-wider text-gray-500"
					>
						<div class="flex items-center gap-2">
							<span>{col.name}</span>
							<span
								class="inline-flex rounded px-1.5 py-0.5 text-[10px] font-medium whitespace-nowrap {typeBadgeColor(shortType(col.data_type))}"
							>
								{shortType(col.data_type)}
							</span>
						</div>
					</th>
				{/each}
				{#if onEditRow || onDeleteRow}
					<th class="px-4 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500">
						Actions
					</th>
				{/if}
			</tr>
		</thead>
		<tbody class="divide-y divide-gray-100">
			{#if loading}
				<tr>
					<td colspan={columns.length + (onEditRow || onDeleteRow ? 1 : 0)} class="px-4 py-12 text-center">
						<div class="flex items-center justify-center gap-2 text-sm text-gray-400">
							<svg class="h-5 w-5 animate-spin" viewBox="0 0 24 24" fill="none">
								<circle cx="12" cy="12" r="10" stroke="currentColor" stroke-width="3" class="opacity-25" />
								<path d="M4 12a8 8 0 018-8" stroke="currentColor" stroke-width="3" stroke-linecap="round" class="opacity-75" />
							</svg>
							Loading rows...
						</div>
					</td>
				</tr>
			{:else if rows.length === 0}
				<tr>
					<td colspan={columns.length + (onEditRow || onDeleteRow ? 1 : 0)} class="px-4 py-12 text-center">
						<div class="text-sm text-gray-400">No rows found</div>
						<div class="mt-1 text-xs text-gray-300">This table is empty</div>
					</td>
				</tr>
			{:else}
				{#each rows as row}
					<tr class="hover:bg-gray-50 transition-colors">
						{#each columns as col}
							<td class="px-4 py-2.5 text-sm whitespace-nowrap">
								{#if isNull(row[col.name])}
									<span class="text-xs italic text-gray-300">null</span>
								{:else if isBool(col.data_type)}
									{#if row[col.name]}
										<svg class="h-4 w-4 text-green-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
											<path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" />
										</svg>
									{:else}
										<svg class="h-4 w-4 text-gray-300" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
											<path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" />
										</svg>
									{/if}
								{:else if isUuid(col.data_type)}
									<div class="flex items-center gap-1.5">
										<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-600">
											{String(row[col.name]).substring(0, 8)}...
										</code>
										<button
											type="button"
											class="cursor-pointer text-gray-300 hover:text-gray-500 transition-colors"
											onclick={() => copyToClipboard(String(row[col.name]))}
											title="Copy full UUID"
										>
											{#if copiedId === String(row[col.name])}
												<svg class="h-3.5 w-3.5 text-green-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
													<path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" />
												</svg>
											{:else}
												<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
													<path stroke-linecap="round" stroke-linejoin="round" d="M15.666 3.888A2.25 2.25 0 0 0 13.5 2.25h-3c-1.03 0-1.9.693-2.166 1.638m7.332 0c.055.194.084.4.084.612v0a.75.75 0 0 1-.75.75H9.75a.75.75 0 0 1-.75-.75v0c0-.212.03-.418.084-.612m7.332 0c.646.049 1.288.11 1.927.184 1.1.128 1.907 1.077 1.907 2.185V19.5a2.25 2.25 0 0 1-2.25 2.25H6.75A2.25 2.25 0 0 1 4.5 19.5V6.257c0-1.108.806-2.057 1.907-2.185a48.208 48.208 0 0 1 1.927-.184" />
												</svg>
											{/if}
										</button>
									</div>
								{:else if isJson(col.data_type)}
									<details class="group">
										<summary class="cursor-pointer rounded bg-orange-50 px-1.5 py-0.5 text-xs font-mono text-orange-600 hover:bg-orange-100">
											JSON
										</summary>
										<pre class="mt-1 max-w-xs overflow-auto rounded bg-gray-900 p-2 text-xs text-green-400">{typeof row[col.name] === 'string' ? row[col.name] : JSON.stringify(row[col.name], null, 2)}</pre>
									</details>
								{:else if isTimestamp(col.data_type)}
									<span class="text-gray-500 text-xs" title={String(row[col.name])}>
										{new Date(row[col.name]).toLocaleString('en-GB', { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' })}
									</span>
								{:else}
									<span class="text-gray-900">{formatCell(row[col.name], col.data_type)}</span>
								{/if}
							</td>
						{/each}
						{#if onEditRow || onDeleteRow}
							<td class="px-4 py-2.5 text-right">
								<div class="flex items-center justify-end gap-1">
									{#if onEditRow}
										<button
											type="button"
											class="cursor-pointer rounded p-1 text-gray-400 hover:bg-gray-100 hover:text-gray-600"
											onclick={() => onEditRow?.(row)}
											title="Edit row"
										>
											<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
												<path stroke-linecap="round" stroke-linejoin="round" d="m16.862 4.487 1.687-1.688a1.875 1.875 0 1 1 2.652 2.652L10.582 16.07a4.5 4.5 0 0 1-1.897 1.13L6 18l.8-2.685a4.5 4.5 0 0 1 1.13-1.897l8.932-8.931Zm0 0L19.5 7.125M18 14v4.75A2.25 2.25 0 0 1 15.75 21H5.25A2.25 2.25 0 0 1 3 18.75V8.25A2.25 2.25 0 0 1 5.25 6H10" />
											</svg>
										</button>
									{/if}
									{#if onDeleteRow}
										<button
											type="button"
											class="cursor-pointer rounded p-1 text-gray-400 hover:bg-red-50 hover:text-red-500"
											onclick={() => onDeleteRow?.(row)}
											title="Delete row"
										>
											<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
												<path stroke-linecap="round" stroke-linejoin="round" d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0" />
											</svg>
										</button>
									{/if}
								</div>
							</td>
						{/if}
					</tr>
				{/each}
			{/if}
		</tbody>
	</table>
</div>
