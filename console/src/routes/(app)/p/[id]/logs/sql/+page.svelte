<script lang="ts">
	import { page } from '$app/stores';
	import { api, type SqlLogEntry } from '$lib/api.js';
	import LogsTabs from '$lib/components/LogsTabs.svelte';

	let projectId = $derived($page.params.id!);

	let entries: SqlLogEntry[] = $state([]);
	let retentionDays = $state(30);
	let nextBefore: number | undefined = $state(undefined);
	let loading = $state(true);
	let loadingMore = $state(false);
	let error: string | null = $state(null);
	let outcome = $state('');
	let expandedId = $state<number | null>(null);
	let copiedId = $state<number | null>(null);

	const pageSize = 100;
	// Only the latest request may update the list (filter / project changes).
	let requestSeq = 0;

	const SOURCE_TEXT: Record<SqlLogEntry['source'], string> = {
		sql: 'SQL editor',
		sql_transaction: 'Transaction',
		function: 'Function',
		policy: 'Policy',
		migration: 'Migration'
	};

	async function load(reset: boolean) {
		const seq = ++requestSeq;
		if (reset) {
			loading = true;
			entries = [];
			nextBefore = undefined;
		} else {
			loadingMore = true;
		}
		error = null;
		try {
			const resp = await api.getSqlLog(projectId, { outcome, before: reset ? undefined : nextBefore, limit: pageSize });
			if (seq !== requestSeq) return;
			entries = reset ? resp.entries : [...entries, ...resp.entries];
			retentionDays = resp.retention_days;
			nextBefore = resp.entries.length === pageSize ? resp.next_before : undefined;
		} catch (e) {
			if (seq !== requestSeq) return;
			error = e instanceof Error ? e.message : 'Failed to load the SQL history';
		} finally {
			if (seq === requestSeq) {
				loading = false;
				loadingMore = false;
			}
		}
	}

	$effect(() => {
		// Re-run on project switch and on filter change.
		void projectId;
		void outcome;
		load(true);
	});

	function toggle(id: number) {
		expandedId = expandedId === id ? null : id;
	}

	async function copy(e: SqlLogEntry) {
		try {
			await navigator.clipboard.writeText(e.statement);
			copiedId = e.id;
			setTimeout(() => { if (copiedId === e.id) copiedId = null; }, 1500);
		} catch { /* clipboard unavailable */ }
	}

	function firstLine(sql: string): string {
		const s = sql.replace(/\s+/g, ' ').trim();
		return s.length > 140 ? s.slice(0, 140) + '…' : s;
	}

	function when(iso: string): string {
		return new Date(iso).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' });
	}

	const outcomeClass: Record<SqlLogEntry['outcome'], string> = {
		ok: 'bg-green-50 text-green-700 ring-green-600/20',
		error: 'bg-red-50 text-red-700 ring-red-600/20',
		refused: 'bg-amber-50 text-amber-800 ring-amber-600/20',
		not_run: 'bg-gray-50 text-gray-600 ring-gray-500/20'
	};
	const outcomeText: Record<SqlLogEntry['outcome'], string> = { ok: 'Ran', error: 'Failed', refused: 'Refused', not_run: 'Not run' };
</script>

<LogsTabs {projectId} active="sql" />

<div class="mb-4 flex flex-wrap items-end justify-between gap-3">
	<p class="max-w-2xl text-sm text-gray-500">
		Every SQL statement sent to this project through the console, the MCP server or the CLI, plus function bodies, custom
		policies and migrations — who sent it, and whether it ran, failed or was refused. Kept {retentionDays} days. Visible to
		project admins, since statements can contain your data.
	</p>
	<label class="text-xs text-gray-600">
		Outcome
		<select bind:value={outcome} class="ml-2 rounded-md border border-gray-300 px-2 py-1.5 text-sm">
			<option value="">All</option>
			<option value="ok">Ran</option>
			<option value="error">Failed</option>
			<option value="refused">Refused</option>
			<option value="not_run">Not run</option>
		</select>
	</label>
</div>

{#if loading}
	<p class="py-12 text-center text-sm text-gray-400">Loading…</p>
{:else if error}
	<div class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">
		{#if /forbidden|permission|role/i.test(error)}
			Only project admins can see the SQL history.
		{:else}
			{error}
		{/if}
	</div>
{:else if entries.length === 0}
	<p class="py-12 text-center text-sm text-gray-400">No SQL statements in the last {retentionDays} days{outcome ? ' with this outcome' : ''}.</p>
{:else}
	<div class="overflow-x-auto rounded-xl border border-gray-200 bg-white">
		<table class="w-full text-sm">
			<thead class="border-b border-gray-100 bg-gray-50 text-left text-xs font-medium uppercase tracking-wider text-gray-500">
				<tr>
					<th class="px-4 py-3">Time</th>
					<th class="px-4 py-3">By</th>
					<th class="px-4 py-3">Source</th>
					<th class="px-4 py-3">Statement</th>
					<th class="px-4 py-3">Result</th>
				</tr>
			</thead>
			<tbody class="divide-y divide-gray-100">
				{#each entries as e (e.id)}
					<tr class="cursor-pointer align-top hover:bg-gray-50" onclick={() => toggle(e.id)}>
						<td class="whitespace-nowrap px-4 py-3 text-xs text-gray-500">{when(e.created_at)}</td>
						<td class="px-4 py-3 text-xs">
							<div class="text-gray-900">{e.actor_email || '—'}</div>
							<div class="text-gray-400">{e.via === 'token' ? 'token (MCP / CLI)' : 'console'}</div>
						</td>
						<td class="whitespace-nowrap px-4 py-3 text-xs text-gray-600">
							{SOURCE_TEXT[e.source]}{#if e.read_only}<span class="ml-1 text-gray-400">· read-only</span>{/if}
						</td>
						<td class="px-4 py-3 font-mono text-xs text-gray-800 break-all">{firstLine(e.statement)}</td>
						<td class="whitespace-nowrap px-4 py-3">
							<span class="inline-flex rounded-md px-2 py-0.5 text-xs font-medium ring-1 ring-inset {outcomeClass[e.outcome]}">{outcomeText[e.outcome]}</span>
							{#if e.outcome === 'ok' && e.row_count != null}<span class="ml-1 text-xs text-gray-400">{e.row_count} rows</span>{/if}
						</td>
					</tr>
					{#if expandedId === e.id}
						<tr class="bg-gray-50">
							<td colspan="5" class="px-4 py-4">
								<div class="mb-2 flex items-center justify-between">
									<span class="text-xs text-gray-500">
										{e.statement_len.toLocaleString()} characters{#if e.statement_len > e.statement.length} (first 8 KB shown){/if}
										{#if e.duration_ms != null} · {e.duration_ms} ms{/if}
										{#if e.ip} · {e.ip}{/if}
										· SHA-256 <span class="font-mono">{e.sha256.slice(0, 12)}…</span>
									</span>
									<button
										onclick={(ev) => { ev.stopPropagation(); copy(e); }}
										class="rounded-md border border-gray-200 bg-white px-2 py-1 text-xs text-gray-600 hover:bg-gray-50 cursor-pointer"
									>{copiedId === e.id ? 'Copied!' : 'Copy'}</button>
								</div>
								<pre class="max-h-80 overflow-auto rounded-lg border border-gray-200 bg-white p-3 font-mono text-xs text-gray-800 whitespace-pre-wrap">{e.statement}</pre>
								{#if e.detail}
									<p class="mt-2 text-xs {e.outcome === 'ok' ? 'text-gray-600' : 'text-red-700'}">{e.detail}</p>
								{/if}
							</td>
						</tr>
					{/if}
				{/each}
			</tbody>
		</table>
	</div>
	{#if nextBefore}
		<div class="mt-4 text-center">
			<button
				onclick={() => load(false)}
				disabled={loadingMore}
				class="rounded-md border border-gray-200 bg-white px-4 py-2 text-sm text-gray-700 hover:bg-gray-50 disabled:opacity-50 cursor-pointer"
			>{loadingMore ? 'Loading…' : 'Load older'}</button>
		</div>
	{/if}
{/if}
