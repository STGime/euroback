<script lang="ts">
	import { api, type SqlLogEntry } from '$lib/api.js';

	// Superadmin: SQL statements across all projects. Defaults to refused
	// ones — statements the platform's checks stopped before they ran.
	let entries: SqlLogEntry[] = $state([]);
	let nextBefore: number | undefined = $state(undefined);
	let outcome = $state('refused');
	let loading = $state(true);
	let loadingMore = $state(false);
	let error: string | null = $state(null);
	let expandedId = $state<number | null>(null);

	const pageSize = 100;
	// Only the latest request may update the list (filter / project changes).
	let requestSeq = 0;

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
			const resp = await api.adminSqlLog({ outcome, before: reset ? undefined : nextBefore, limit: pageSize });
			if (seq !== requestSeq) return;
			entries = reset ? resp.entries : [...entries, ...resp.entries];
			nextBefore = resp.entries.length === pageSize ? resp.next_before : undefined;
		} catch (e) {
			if (seq !== requestSeq) return;
			error = e instanceof Error ? e.message : 'Failed to load the SQL log';
		} finally {
			if (seq === requestSeq) {
				loading = false;
				loadingMore = false;
			}
		}
	}

	$effect(() => {
		void outcome;
		load(true);
	});

	function oneLine(sql: string): string {
		const s = sql.replace(/\s+/g, ' ').trim();
		return s.length > 120 ? s.slice(0, 120) + '…' : s;
	}
</script>

<div class="mx-auto max-w-7xl space-y-6 p-6">
	<header class="flex flex-wrap items-end justify-between gap-3">
		<div>
			<a href="/admin" class="text-sm text-blue-600 hover:underline">← Admin</a>
			<h1 class="mt-1 text-2xl font-bold text-gray-900">SQL log</h1>
			<p class="text-sm text-gray-500">
				Statements sent through the console, MCP server and CLI across all projects (30 days). Refused = stopped by the
				platform's checks before reaching the database.
			</p>
		</div>
		<label class="text-xs text-gray-600">
			Outcome
			<select bind:value={outcome} class="ml-2 rounded-md border border-gray-300 px-2 py-1.5 text-sm">
				<option value="refused">Refused</option>
				<option value="error">Failed</option>
				<option value="ok">Ran</option>
				<option value="not_run">Not run</option>
				<option value="">All</option>
			</select>
		</label>
	</header>

	{#if loading}
		<p class="py-12 text-center text-sm text-gray-400">Loading…</p>
	{:else if error}
		<div class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">{error}</div>
	{:else if entries.length === 0}
		<p class="py-12 text-center text-sm text-gray-400">Nothing here.</p>
	{:else}
		<div class="overflow-x-auto rounded-xl border border-gray-200 bg-white">
			<table class="w-full text-sm">
				<thead class="border-b border-gray-100 bg-gray-50 text-left text-xs font-medium uppercase tracking-wider text-gray-500">
					<tr>
						<th class="px-4 py-3">Time</th>
						<th class="px-4 py-3">Project</th>
						<th class="px-4 py-3">By</th>
						<th class="px-4 py-3">Statement</th>
						<th class="px-4 py-3">Detail</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-gray-100">
					{#each entries as e (e.id)}
						<tr class="cursor-pointer align-top hover:bg-gray-50" onclick={() => (expandedId = expandedId === e.id ? null : e.id)}>
							<td class="whitespace-nowrap px-4 py-3 text-xs text-gray-500">{new Date(e.created_at).toLocaleString()}</td>
							<td class="px-4 py-3 font-mono text-xs text-gray-600">
								{e.project_id?.slice(0, 8)}
								{#if e.project_deleted_at}<div class="font-sans text-red-600">deleted</div>{/if}
							</td>
							<td class="px-4 py-3 text-xs">
								<div class="text-gray-900">{e.actor_email || '—'}</div>
								<div class="text-gray-400">{e.via}{e.ip ? ` · ${e.ip}` : ''}</div>
							</td>
							<td class="px-4 py-3 font-mono text-xs text-gray-800 break-all">
								{#if e.project_deleted_at}<span class="font-sans italic text-gray-400">text removed when the project was deleted</span>{:else}{oneLine(e.statement)}{/if}
							</td>
							<td class="px-4 py-3 text-xs text-gray-600">{e.outcome}{e.detail ? `: ${e.detail.slice(0, 100)}` : ''}</td>
						</tr>
						{#if expandedId === e.id}
							<tr class="bg-gray-50">
								<td colspan="5" class="px-4 py-4">
									<p class="mb-2 text-xs text-gray-500">
										Project <span class="font-mono">{e.project_id}</span> · {e.source} · {e.statement_len} characters · SHA-256
										<span class="font-mono">{e.sha256}</span>{#if e.pat_id} · token <span class="font-mono">{e.pat_id}</span>{/if}
									</p>
									{#if e.project_deleted_at}
										<p class="text-xs italic text-gray-500">Statement text, detail and IP were removed when the project was deleted ({new Date(e.project_deleted_at).toLocaleString()}).</p>
									{:else}
										<pre class="max-h-80 overflow-auto rounded-lg border border-gray-200 bg-white p-3 font-mono text-xs text-gray-800 whitespace-pre-wrap">{e.statement}</pre>
									{/if}
									{#if e.detail}<p class="mt-2 text-xs text-red-700">{e.detail}</p>{/if}
								</td>
							</tr>
						{/if}
					{/each}
				</tbody>
			</table>
		</div>
		{#if nextBefore}
			<div class="text-center">
				<button
					onclick={() => load(false)}
					disabled={loadingMore}
					class="rounded-md border border-gray-200 bg-white px-4 py-2 text-sm text-gray-700 hover:bg-gray-50 disabled:opacity-50 cursor-pointer"
				>{loadingMore ? 'Loading…' : 'Load older'}</button>
			</div>
		{/if}
	{/if}
</div>
