<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { api, type RequestLog, type LogStats, type LogsResponse } from '$lib/api.js';

	let projectId = $derived($page.params.id);

	let logs: RequestLog[] = $state([]);
	let total = $state(0);
	let stats: LogStats = $state({ total_requests: 0, error_count: 0, avg_latency_ms: 0, p95_latency_ms: 0 });
	let loading = $state(true);
	let error: string | null = $state(null);

	// Filters
	let methodFilter = $state('');
	let statusFilter = $state('');
	let pathFilter = $state('');

	// Pagination
	let currentOffset = $state(0);
	const pageSize = 50;
	let pageStart = $derived(total > 0 ? currentOffset + 1 : 0);
	let pageEnd = $derived(Math.min(currentOffset + pageSize, total));
	let hasPrev = $derived(currentOffset > 0);
	let hasNext = $derived(currentOffset + pageSize < total);

	onMount(() => {
		loadLogs();
	});

	async function loadLogs() {
		loading = true;
		error = null;
		try {
			const params: Record<string, any> = {
				limit: pageSize,
				offset: currentOffset
			};
			if (methodFilter) params.method = methodFilter;
			if (statusFilter === '2xx') { params.status_min = 200; params.status_max = 299; }
			else if (statusFilter === '4xx') { params.status_min = 400; params.status_max = 499; }
			else if (statusFilter === '5xx') { params.status_min = 500; params.status_max = 599; }
			if (pathFilter) params.path = pathFilter;

			const resp: LogsResponse = await api.getLogs(projectId, params);
			logs = resp.logs;
			total = resp.total;
			stats = resp.stats;
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load logs';
		} finally {
			loading = false;
		}
	}

	function applyFilters() {
		currentOffset = 0;
		loadLogs();
	}

	function clearFilters() {
		methodFilter = '';
		statusFilter = '';
		pathFilter = '';
		currentOffset = 0;
		loadLogs();
	}

	function prevPage() {
		if (hasPrev) { currentOffset = Math.max(0, currentOffset - pageSize); loadLogs(); }
	}
	function nextPage() {
		if (hasNext) { currentOffset += pageSize; loadLogs(); }
	}

	function methodColor(method: string): string {
		switch (method) {
			case 'GET': return 'bg-blue-100 text-blue-700';
			case 'POST': return 'bg-green-100 text-green-700';
			case 'PATCH': case 'PUT': return 'bg-amber-100 text-amber-700';
			case 'DELETE': return 'bg-red-100 text-red-700';
			default: return 'bg-gray-100 text-gray-700';
		}
	}

	function statusColor(code: number): string {
		if (code < 300) return 'bg-green-100 text-green-700';
		if (code < 400) return 'bg-blue-100 text-blue-700';
		if (code < 500) return 'bg-amber-100 text-amber-700';
		return 'bg-red-100 text-red-700';
	}

	function formatTime(iso: string): string {
		return new Date(iso).toLocaleString(undefined, {
			month: 'short', day: 'numeric',
			hour: '2-digit', minute: '2-digit', second: '2-digit'
		});
	}

	let errorRate = $derived(stats.total_requests > 0 ? ((stats.error_count / stats.total_requests) * 100).toFixed(1) : '0');
</script>

<!-- Stats cards -->
<div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4 mb-6">
	<div class="rounded-xl border border-gray-200 bg-white p-5">
		<div class="text-xs font-medium uppercase tracking-wider text-gray-400 mb-1">Total Requests</div>
		<div class="text-2xl font-bold text-gray-900">{stats.total_requests.toLocaleString()}</div>
	</div>
	<div class="rounded-xl border border-gray-200 bg-white p-5">
		<div class="text-xs font-medium uppercase tracking-wider text-gray-400 mb-1">Error Rate</div>
		<div class="text-2xl font-bold {Number(errorRate) > 5 ? 'text-red-600' : 'text-gray-900'}">{errorRate}%</div>
	</div>
	<div class="rounded-xl border border-gray-200 bg-white p-5">
		<div class="text-xs font-medium uppercase tracking-wider text-gray-400 mb-1">Avg Latency</div>
		<div class="text-2xl font-bold text-gray-900">{Math.round(stats.avg_latency_ms)}ms</div>
	</div>
	<div class="rounded-xl border border-gray-200 bg-white p-5">
		<div class="text-xs font-medium uppercase tracking-wider text-gray-400 mb-1">P95 Latency</div>
		<div class="text-2xl font-bold text-gray-900">{Math.round(stats.p95_latency_ms)}ms</div>
	</div>
</div>

<!-- Filter bar -->
<div class="flex items-center gap-2 mb-4 rounded-lg border border-gray-200 bg-gray-50 p-3">
	<select
		bind:value={methodFilter}
		class="rounded-lg border border-gray-300 bg-white px-2 py-1.5 text-sm text-gray-700 focus:border-eurobase-500 focus:outline-none cursor-pointer"
	>
		<option value="">All methods</option>
		<option value="GET">GET</option>
		<option value="POST">POST</option>
		<option value="PATCH">PATCH</option>
		<option value="PUT">PUT</option>
		<option value="DELETE">DELETE</option>
	</select>
	<select
		bind:value={statusFilter}
		class="rounded-lg border border-gray-300 bg-white px-2 py-1.5 text-sm text-gray-700 focus:border-eurobase-500 focus:outline-none cursor-pointer"
	>
		<option value="">All statuses</option>
		<option value="2xx">2xx Success</option>
		<option value="4xx">4xx Client Error</option>
		<option value="5xx">5xx Server Error</option>
	</select>
	<input
		type="text"
		bind:value={pathFilter}
		placeholder="Filter by path..."
		class="flex-1 rounded-lg border border-gray-300 px-2.5 py-1.5 text-sm text-gray-900 placeholder-gray-400 focus:border-eurobase-500 focus:outline-none"
	/>
	<button
		type="button"
		class="cursor-pointer rounded-lg bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors"
		onclick={applyFilters}
	>
		Apply
	</button>
	<button
		type="button"
		class="cursor-pointer rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors"
		onclick={clearFilters}
	>
		Clear
	</button>
</div>

{#if error}
	<div class="mb-4 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-600">
		{error}
	</div>
{/if}

<!-- Log table -->
<div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
	<div class="overflow-x-auto">
		<table class="w-full text-sm">
			<thead>
				<tr class="border-b border-gray-200 bg-gray-50">
					<th class="px-4 py-2.5 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Timestamp</th>
					<th class="px-4 py-2.5 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Method</th>
					<th class="px-4 py-2.5 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Path</th>
					<th class="px-4 py-2.5 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Status</th>
					<th class="px-4 py-2.5 text-right text-xs font-medium uppercase tracking-wider text-gray-500">Latency</th>
				</tr>
			</thead>
			<tbody>
				{#if loading}
					{#each Array(5) as _}
						<tr class="border-b border-gray-100">
							<td class="px-4 py-3" colspan="5"><div class="h-4 animate-pulse rounded bg-gray-100 w-full"></div></td>
						</tr>
					{/each}
				{:else if logs.length === 0}
					<tr>
						<td class="px-4 py-8 text-center text-gray-400" colspan="5">
							No logs yet. Logs appear after API requests are made.
						</td>
					</tr>
				{:else}
					{#each logs as log}
						<tr class="border-b border-gray-100 hover:bg-gray-50 transition-colors">
							<td class="px-4 py-2.5 text-xs text-gray-500 font-mono whitespace-nowrap">{formatTime(log.created_at)}</td>
							<td class="px-4 py-2.5">
								<span class="inline-flex rounded-full px-2 py-0.5 text-xs font-semibold {methodColor(log.method)}">{log.method}</span>
							</td>
							<td class="px-4 py-2.5 text-xs font-mono text-gray-700 max-w-xs truncate">{log.path}</td>
							<td class="px-4 py-2.5">
								<span class="inline-flex rounded-full px-2 py-0.5 text-xs font-semibold {statusColor(log.status_code)}">{log.status_code}</span>
							</td>
							<td class="px-4 py-2.5 text-right text-xs font-mono text-gray-600">{log.latency_ms}ms</td>
						</tr>
					{/each}
				{/if}
			</tbody>
		</table>
	</div>
</div>

<!-- Pagination -->
<div class="flex items-center justify-between mt-4">
	<div class="text-sm text-gray-500">
		{#if total > 0}
			Showing {pageStart}–{pageEnd} of {total.toLocaleString()}
		{:else}
			No logs
		{/if}
	</div>
	<div class="flex items-center gap-2">
		<button
			type="button"
			class="cursor-pointer rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
			disabled={!hasPrev}
			onclick={prevPage}
		>
			Previous
		</button>
		<button
			type="button"
			class="cursor-pointer rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
			disabled={!hasNext}
			onclick={nextPage}
		>
			Next
		</button>
	</div>
</div>
