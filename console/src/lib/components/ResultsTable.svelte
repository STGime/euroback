<script lang="ts">
	let {
		columns = [],
		rows = [],
		loading = false
	}: {
		columns: string[];
		rows: Record<string, any>[];
		loading: boolean;
	} = $props();

	let copiedText: string | null = $state(null);

	function isNull(value: any): boolean {
		return value === null || value === undefined;
	}

	function isUuid(value: any): boolean {
		return typeof value === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value);
	}

	function formatValue(value: any): string {
		if (value === null || value === undefined) return '';
		if (typeof value === 'object') return JSON.stringify(value);
		return String(value);
	}

	function displayValue(value: any): string {
		if (isNull(value)) return '';
		if (isUuid(value)) return String(value).substring(0, 8) + '...';
		if (typeof value === 'object') return JSON.stringify(value);
		const s = String(value);
		return s.length > 120 ? s.substring(0, 120) + '...' : s;
	}

	async function copyToClipboard(text: string) {
		try {
			await navigator.clipboard.writeText(text);
			copiedText = text;
			setTimeout(() => (copiedText = null), 1500);
		} catch {}
	}
</script>

<div class="overflow-auto rounded-lg border border-gray-200 bg-white h-full">
	<table class="min-w-full divide-y divide-gray-200">
		<thead class="bg-gray-50 sticky top-0 z-10">
			<tr>
				{#each columns as col}
					<th class="px-4 py-2.5 text-left text-xs font-semibold uppercase tracking-wider text-gray-500 whitespace-nowrap">
						{col}
					</th>
				{/each}
			</tr>
		</thead>
		<tbody class="divide-y divide-gray-100">
			{#if loading}
				<tr>
					<td colspan={columns.length} class="px-4 py-12 text-center">
						<div class="flex items-center justify-center gap-2 text-sm text-gray-400">
							<svg class="h-5 w-5 animate-spin" viewBox="0 0 24 24" fill="none">
								<circle cx="12" cy="12" r="10" stroke="currentColor" stroke-width="3" class="opacity-25" />
								<path d="M4 12a8 8 0 018-8" stroke="currentColor" stroke-width="3" stroke-linecap="round" class="opacity-75" />
							</svg>
							Executing query...
						</div>
					</td>
				</tr>
			{:else if rows.length === 0}
				<tr>
					<td colspan={columns.length || 1} class="px-4 py-12 text-center">
						<div class="text-sm text-gray-400">No results</div>
					</td>
				</tr>
			{:else}
				{#each rows as row}
					<tr class="hover:bg-gray-50 transition-colors">
						{#each columns as col}
							<td class="px-4 py-2 text-sm whitespace-nowrap max-w-xs truncate">
								{#if isNull(row[col])}
									<span class="text-xs italic text-gray-300">null</span>
								{:else if typeof row[col] === 'boolean'}
									{#if row[col]}
										<svg class="h-4 w-4 text-green-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
											<path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5" />
										</svg>
									{:else}
										<svg class="h-4 w-4 text-gray-300" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
											<path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" />
										</svg>
									{/if}
								{:else if isUuid(row[col])}
									<div class="flex items-center gap-1.5">
										<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono text-gray-600">
											{String(row[col]).substring(0, 8)}...
										</code>
										<button
											type="button"
											class="cursor-pointer text-gray-300 hover:text-gray-500 transition-colors"
											onclick={() => copyToClipboard(String(row[col]))}
											title="Copy full UUID"
										>
											{#if copiedText === String(row[col])}
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
								{:else if typeof row[col] === 'object'}
									<details class="group">
										<summary class="cursor-pointer rounded bg-orange-50 px-1.5 py-0.5 text-xs font-mono text-orange-600 hover:bg-orange-100">
											JSON
										</summary>
										<pre class="mt-1 max-w-xs overflow-auto rounded bg-gray-900 p-2 text-xs text-green-400">{JSON.stringify(row[col], null, 2)}</pre>
									</details>
								{:else}
									<span class="text-gray-900" title={formatValue(row[col])}>{displayValue(row[col])}</span>
								{/if}
							</td>
						{/each}
					</tr>
				{/each}
			{/if}
		</tbody>
	</table>
</div>
