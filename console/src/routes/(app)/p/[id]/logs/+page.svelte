<script lang="ts">
	import { onMount, getContext } from 'svelte';
	import { page } from '$app/stores';
	import { api, type RequestLog, type LogStats, type LogsResponse } from '$lib/api.js';

	const projectCtx = getContext<{ id: string; project: import('$lib/api.js').Project | null }>('projectId');
	let projectId = $derived($page.params.id);
	let plan = $derived(projectCtx.project?.plan ?? 'free');
	let isFreePlan = $derived(plan === 'free');

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

	// Row expansion: one open at a time, keyed by log id.
	let expandedId = $state<string | null>(null);
	let copiedId = $state<string | null>(null);
	let exportBusy = $state(false);
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

	function toggleRow(id: string) {
		expandedId = expandedId === id ? null : id;
	}

	async function copyLog(log: RequestLog) {
		const payload = {
			timestamp: log.created_at,
			method: log.method,
			path: log.path,
			status_code: log.status_code,
			latency_ms: log.latency_ms,
			ip_address: log.ip_address || null,
			user_agent: log.user_agent || null,
		};
		try {
			await navigator.clipboard.writeText(JSON.stringify(payload, null, 2));
			copiedId = log.id;
			setTimeout(() => {
				if (copiedId === log.id) copiedId = null;
			}, 1200);
		} catch {
			// Clipboard blocked (insecure context etc.) — silently ignore.
		}
	}

	/**
	 * Export all logs matching the current filter set as CSV. Paginates in
	 * chunks of 1000 to avoid timeouts; caps at 10k rows so a runaway filter
	 * doesn't OOM the browser.
	 */
	async function exportCSV() {
		if (exportBusy) return;
		exportBusy = true;
		try {
			const cap = 10000;
			const chunk = 1000;
			const rows: RequestLog[] = [];
			let offset = 0;
			while (rows.length < cap) {
				const params: Record<string, any> = { limit: chunk, offset };
				if (methodFilter) params.method = methodFilter;
				if (statusFilter === '2xx') { params.status_min = 200; params.status_max = 299; }
				else if (statusFilter === '4xx') { params.status_min = 400; params.status_max = 499; }
				else if (statusFilter === '5xx') { params.status_min = 500; params.status_max = 599; }
				if (pathFilter) params.path = pathFilter;
				const resp: LogsResponse = await api.getLogs(projectId, params);
				rows.push(...resp.logs);
				if (resp.logs.length < chunk) break;
				offset += chunk;
			}
			const csv = toCSV(rows);
			const blob = new Blob([csv], { type: 'text/csv;charset=utf-8' });
			const url = URL.createObjectURL(blob);
			const a = document.createElement('a');
			a.href = url;
			a.download = `eurobase-logs-${new Date().toISOString().slice(0, 19).replace(/:/g, '-')}.csv`;
			document.body.appendChild(a);
			a.click();
			a.remove();
			URL.revokeObjectURL(url);
		} catch (err) {
			error = err instanceof Error ? err.message : 'Export failed';
		} finally {
			exportBusy = false;
		}
	}

	function toCSV(rows: RequestLog[]): string {
		const header = ['timestamp', 'method', 'path', 'status_code', 'latency_ms', 'ip_address', 'user_agent'];
		const esc = (v: unknown) => {
			const s = v == null ? '' : String(v);
			return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
		};
		const lines = [header.join(',')];
		for (const r of rows) {
			lines.push([
				r.created_at,
				r.method,
				r.path,
				r.status_code,
				r.latency_ms,
				r.ip_address ?? '',
				r.user_agent ?? '',
			].map(esc).join(','));
		}
		return lines.join('\n');
	}
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

<!-- Plan retention banner -->
{#if isFreePlan}
	<div class="mb-4 flex items-start gap-3 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3">
		<svg class="h-5 w-5 shrink-0 text-amber-500 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
			<path stroke-linecap="round" stroke-linejoin="round" d="M12 6v6h4.5m4.5 0a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" />
		</svg>
		<div class="flex-1">
			<p class="text-sm font-medium text-amber-800">Free plan — 1 day log retention</p>
			<p class="mt-0.5 text-sm text-amber-700">
				Logs older than 24 hours are automatically deleted. Upgrade to Pro for 30-day retention, giving you full visibility into trends, debugging, and performance history.
			</p>
		</div>
		<a
			href="/p/{projectId}/settings"
			class="shrink-0 rounded-lg bg-amber-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-amber-700 transition-colors"
		>
			Upgrade
		</a>
	</div>
{:else}
	<div class="mb-4 flex items-center gap-2 text-xs text-gray-400">
		<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
			<path stroke-linecap="round" stroke-linejoin="round" d="M12 6v6h4.5m4.5 0a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" />
		</svg>
		<span>Pro plan — 30 day log retention</span>
	</div>
{/if}

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
	<button
		type="button"
		class="cursor-pointer rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
		onclick={exportCSV}
		disabled={exportBusy || total === 0}
		title="Download filtered logs as CSV (up to 10,000 rows)"
	>
		{exportBusy ? 'Exporting…' : 'Export CSV'}
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
					<th class="w-6 px-2 py-2.5"></th>
					<th class="px-4 py-2.5 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Timestamp</th>
					<th class="px-4 py-2.5 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Method</th>
					<th class="px-4 py-2.5 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Path</th>
					<th class="px-4 py-2.5 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Status</th>
					<th class="px-4 py-2.5 text-right text-xs font-medium uppercase tracking-wider text-gray-500">Latency</th>
					<th class="w-20 px-4 py-2.5"></th>
				</tr>
			</thead>
			<tbody>
				{#if loading}
					{#each Array(5) as _}
						<tr class="border-b border-gray-100">
							<td class="px-4 py-3" colspan="7"><div class="h-4 animate-pulse rounded bg-gray-100 w-full"></div></td>
						</tr>
					{/each}
				{:else if logs.length === 0}
					<tr>
						<td class="px-4 py-8 text-center text-gray-400" colspan="7">
							No logs yet. Logs appear after API requests are made.
						</td>
					</tr>
				{:else}
					{#each logs as log}
						<tr
							class="border-b border-gray-100 hover:bg-gray-50 transition-colors cursor-pointer"
							onclick={() => toggleRow(log.id)}
						>
							<td class="px-2 py-2.5 text-gray-400">
								<svg class="h-3.5 w-3.5 transition-transform {expandedId === log.id ? 'rotate-90' : ''}" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
									<path stroke-linecap="round" stroke-linejoin="round" d="m8.25 4.5 7.5 7.5-7.5 7.5" />
								</svg>
							</td>
							<td class="px-4 py-2.5 text-xs text-gray-500 font-mono whitespace-nowrap">{formatTime(log.created_at)}</td>
							<td class="px-4 py-2.5">
								<span class="inline-flex rounded-full px-2 py-0.5 text-xs font-semibold {methodColor(log.method)}">{log.method}</span>
							</td>
							<td class="px-4 py-2.5 text-xs font-mono text-gray-700 max-w-xs truncate" title={log.path}>{log.path}</td>
							<td class="px-4 py-2.5">
								<span class="inline-flex rounded-full px-2 py-0.5 text-xs font-semibold {statusColor(log.status_code)}">{log.status_code}</span>
							</td>
							<td class="px-4 py-2.5 text-right text-xs font-mono text-gray-600">{log.latency_ms}ms</td>
							<td class="px-4 py-2.5 text-right">
								<button
									type="button"
									class="cursor-pointer rounded border border-transparent px-2 py-0.5 text-xs text-gray-500 hover:border-gray-300 hover:bg-white hover:text-gray-800"
									onclick={(e) => { e.stopPropagation(); copyLog(log); }}
									title="Copy log entry as JSON"
								>
									{copiedId === log.id ? 'Copied' : 'Copy'}
								</button>
							</td>
						</tr>
						{#if expandedId === log.id}
							<tr class="border-b border-gray-100 bg-gray-50/60">
								<td></td>
								<td colspan="6" class="px-4 py-3">
									<dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-xs">
										<dt class="text-gray-500">Request ID</dt>
										<dd class="font-mono text-gray-700 break-all">{log.id}</dd>
										<dt class="text-gray-500">Full path</dt>
										<dd class="font-mono text-gray-700 break-all">{log.path}</dd>
										<dt class="text-gray-500">IP address</dt>
										<dd class="font-mono text-gray-700">{log.ip_address || '—'}</dd>
										<dt class="text-gray-500">User agent</dt>
										<dd class="font-mono text-gray-700 break-all">{log.user_agent || '—'}</dd>
										<dt class="text-gray-500">Timestamp (UTC)</dt>
										<dd class="font-mono text-gray-700">{log.created_at}</dd>
									</dl>
								</td>
							</tr>
						{/if}
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
