<script lang="ts">
	import { page } from '$app/stores';
	import { api, type EdgeFunction, type EdgeFunctionLog, type TableSchema } from '$lib/api.js';
	import CodeEditor from '$lib/components/CodeEditor.svelte';

	let projectId = $derived($page.params.id);

	let functions: EdgeFunction[] = $state([]);
	let loading = $state(true);
	let error: string | null = $state(null);

	// Schema browser state
	let tables: TableSchema[] = $state([]);
	let schemaLoading = $state(true);
	let expandedTable: string | null = $state(null);

	// Editor state
	let selectedFn: EdgeFunction | null = $state(null);
	let editorCode = $state('');
	let editorVerifyJWT = $state(true);
	let editorStatus = $state('active');
	let saving = $state(false);
	let logs: EdgeFunctionLog[] = $state([]);
	let showLogs = $state(false);

	// Code editor ref
	let codeEditor: CodeEditor | undefined = $state(undefined);

	// Create state
	let showCreate = $state(false);
	let newName = $state('');
	let creating = $state(false);

	// Delete state
	let showDeleteConfirm: string | null = $state(null);

	$effect(() => {
		loadFunctions();
		loadSchema();
	});

	async function loadSchema() {
		schemaLoading = true;
		const hiddenTables = new Set(['users', 'refresh_tokens', 'storage_objects', 'email_tokens', 'vault_secrets']);
		try {
			tables = (await api.getSchema(projectId)).filter(t => !hiddenTables.has(t.name));
		} catch {
			// Schema load failure is non-critical
		} finally {
			schemaLoading = false;
		}
	}

	function insertText(text: string) {
		if (codeEditor) {
			const pos = codeEditor.getCursorPosition();
			codeEditor.insertAt(pos, text);
			codeEditor.focus();
		} else {
			editorCode = editorCode + ' ' + text;
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

	async function loadFunctions() {
		loading = true;
		error = null;
		try {
			functions = await api.listEdgeFunctions(projectId);
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load functions';
		} finally {
			loading = false;
		}
	}

	async function selectFunction(fn: EdgeFunction) {
		try {
			const full = await api.getEdgeFunction(projectId, fn.name);
			selectedFn = full;
			editorCode = full.code || '';
			editorVerifyJWT = full.verify_jwt;
			editorStatus = full.status;
			showLogs = false;
			logs = [];
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load function';
		}
	}

	async function saveFunction() {
		if (!selectedFn) return;
		saving = true;
		try {
			const updated = await api.updateEdgeFunction(projectId, selectedFn.name, {
				code: editorCode,
				verify_jwt: editorVerifyJWT,
				status: editorStatus
			});
			selectedFn = { ...updated, code: editorCode };
			// Refresh list to update version
			await loadFunctions();
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to save function';
		} finally {
			saving = false;
		}
	}

	async function createFunction() {
		if (!newName.trim()) return;
		creating = true;
		try {
			const defaultCode = `export default async function handler(req: Request, ctx: Eurobase.FunctionContext) {
  const body = await req.json();

  // Access database (scoped to your project schema)
  // const [row] = await ctx.db.sql("SELECT * FROM your_table WHERE id = $1", [body.id]);

  // Access vault secrets
  // const apiKey = await ctx.vault.get("MY_API_KEY");

  // Access authenticated user (if verify_jwt is enabled)
  // const userId = ctx.user?.id;

  return new Response(JSON.stringify({ message: "Hello from Eurobase!" }), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}`;
			const fn = await api.createEdgeFunction(projectId, {
				name: newName.trim(),
				code: defaultCode
			});
			showCreate = false;
			newName = '';
			await loadFunctions();
			await selectFunction(fn);
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to create function';
		} finally {
			creating = false;
		}
	}

	async function deleteFunction(name: string) {
		try {
			await api.deleteEdgeFunction(projectId, name);
			if (selectedFn?.name === name) {
				selectedFn = null;
				editorCode = '';
			}
			showDeleteConfirm = null;
			await loadFunctions();
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to delete function';
		}
	}

	async function loadLogs() {
		if (!selectedFn) return;
		showLogs = true;
		try {
			logs = await api.getEdgeFunctionLogs(projectId, selectedFn.name);
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load logs';
		}
	}

	function statusColor(status: number): string {
		if (status < 300) return 'text-green-600';
		if (status < 400) return 'text-yellow-600';
		return 'text-red-600';
	}
</script>

<div class="space-y-6">
	<div class="flex items-center justify-between">
		<div>
			<h2 class="text-lg font-semibold text-gray-900">Edge Functions</h2>
			<p class="text-sm text-gray-500">Serverless TypeScript/JavaScript functions running on EU-sovereign infrastructure</p>
		</div>
		<button
			onclick={() => { showCreate = true; }}
			class="cursor-pointer rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors"
		>+ New Function</button>
	</div>

	{#if error}
		<div class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">
			{error}
			<button onclick={() => { error = null; }} class="ml-2 underline cursor-pointer">dismiss</button>
		</div>
	{/if}

	<!-- Create modal -->
	{#if showCreate}
		<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
			<div class="w-full max-w-md rounded-xl bg-white p-6 shadow-xl">
				<h3 class="text-lg font-semibold text-gray-900 mb-4">Create Edge Function</h3>
				<label class="block text-sm font-medium text-gray-700 mb-1">Function name</label>
				<input
					type="text"
					bind:value={newName}
					placeholder="process-order"
					class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm focus:border-eurobase-500 focus:ring-eurobase-500"
					onkeydown={(e) => e.key === 'Enter' && createFunction()}
				/>
				<p class="mt-1 text-xs text-gray-500">Lowercase letters, numbers, hyphens, and underscores. 1-63 characters.</p>
				<div class="mt-4 flex justify-end gap-3">
					<button onclick={() => { showCreate = false; newName = ''; }} class="cursor-pointer rounded-lg border border-gray-300 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50">Cancel</button>
					<button
						onclick={createFunction}
						disabled={creating || !newName.trim()}
						class="cursor-pointer rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 disabled:opacity-50"
					>{creating ? 'Creating...' : 'Create'}</button>
				</div>
			</div>
		</div>
	{/if}

	<!-- Delete confirm modal -->
	{#if showDeleteConfirm}
		<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
			<div class="w-full max-w-sm rounded-xl bg-white p-6 shadow-xl">
				<h3 class="text-lg font-semibold text-gray-900 mb-2">Delete Function</h3>
				<p class="text-sm text-gray-600">Are you sure you want to delete <span class="font-mono font-semibold">{showDeleteConfirm}</span>? This action cannot be undone.</p>
				<div class="mt-4 flex justify-end gap-3">
					<button onclick={() => { showDeleteConfirm = null; }} class="cursor-pointer rounded-lg border border-gray-300 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50">Cancel</button>
					<button
						onclick={() => showDeleteConfirm && deleteFunction(showDeleteConfirm)}
						class="cursor-pointer rounded-lg bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700"
					>Delete</button>
				</div>
			</div>
		</div>
	{/if}

	{#if loading}
		<div class="flex items-center gap-2 text-sm text-gray-500">
			<div class="h-4 w-4 animate-spin rounded-full border-2 border-gray-300 border-t-eurobase-600"></div>
			Loading functions...
		</div>
	{:else}
		<div class="grid grid-cols-12 gap-6">
			<!-- Function list sidebar -->
			<div class="col-span-3 space-y-2">
				{#if functions.length === 0}
					<div class="rounded-lg border border-dashed border-gray-300 p-8 text-center">
						<p class="text-sm text-gray-500">No edge functions yet</p>
						<p class="mt-1 text-xs text-gray-400">Create your first function to get started</p>
					</div>
				{/if}

				{#each functions as fn}
					<button
						onclick={() => selectFunction(fn)}
						class="cursor-pointer w-full text-left rounded-lg border px-4 py-3 transition-colors
							{selectedFn?.name === fn.name
								? 'border-eurobase-300 bg-eurobase-50'
								: 'border-gray-200 bg-white hover:border-gray-300 hover:bg-gray-50'}"
					>
						<div class="flex items-center justify-between">
							<span class="font-mono text-sm font-medium text-gray-900">{fn.name}</span>
							<div class="flex items-center gap-2">
								<span class="text-xs text-gray-400">v{fn.version}</span>
								<span class="inline-flex h-2 w-2 rounded-full {fn.status === 'active' ? 'bg-green-400' : 'bg-gray-400'}"></span>
							</div>
						</div>
						<div class="mt-1 flex items-center gap-3 text-xs text-gray-500">
							<span>{fn.verify_jwt ? 'JWT required' : 'Public'}</span>
						</div>
					</button>
				{/each}
			</div>

			<!-- Editor panel -->
			<div class="col-span-6 min-w-0">
				{#if selectedFn}
					<div class="rounded-lg border border-gray-200 bg-white">
						<!-- Header -->
						<div class="flex items-center justify-between border-b border-gray-200 px-4 py-3">
							<div>
								<h3 class="font-mono text-sm font-semibold text-gray-900">{selectedFn.name}</h3>
								<p class="text-xs text-gray-500">
									v{selectedFn.version} &middot; Invoke: <span class="font-mono">POST /v1/functions/{selectedFn.name}</span>
								</p>
							</div>
							<div class="flex items-center gap-2">
								<button
									onclick={loadLogs}
									class="cursor-pointer rounded-md border border-gray-300 px-3 py-1.5 text-xs font-medium text-gray-700 hover:bg-gray-50"
								>Logs</button>
								<button
									onclick={() => { showDeleteConfirm = selectedFn?.name ?? null; }}
									class="cursor-pointer rounded-md border border-red-200 px-3 py-1.5 text-xs font-medium text-red-600 hover:bg-red-50"
								>Delete</button>
								<button
									onclick={saveFunction}
									disabled={saving}
									class="cursor-pointer rounded-md bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 disabled:opacity-50"
								>{saving ? 'Saving...' : 'Save & Deploy'}</button>
							</div>
						</div>

						<!-- Settings bar -->
						<div class="flex items-center gap-4 border-b border-gray-100 px-4 py-2 bg-gray-50 text-xs">
							<label class="flex items-center gap-1.5 text-gray-700">
								<input type="checkbox" bind:checked={editorVerifyJWT} class="rounded border-gray-300" />
								Require JWT
							</label>
							<label class="flex items-center gap-1.5 text-gray-700">
								Status:
								<select bind:value={editorStatus} class="rounded border-gray-300 bg-white px-2 py-0.5 text-xs">
									<option value="active">Active</option>
									<option value="disabled">Disabled</option>
								</select>
							</label>
						</div>

						<!-- Triggers -->
						<div class="border-b border-gray-100 px-4 py-2.5 bg-gray-50/50">
							<p class="text-[11px] font-semibold uppercase tracking-wider text-gray-400 mb-1.5">Triggers</p>
							<div class="space-y-1.5">
								<div class="flex items-center gap-2 text-xs text-gray-600">
									<span class="inline-flex items-center rounded bg-green-100 px-1.5 py-0.5 text-[10px] font-semibold text-green-700">HTTP</span>
									<code class="font-mono text-gray-500">POST /v1/functions/{selectedFn.name}</code>
								</div>
								<div class="flex items-center gap-2 text-xs text-gray-400">
									<span class="inline-flex items-center rounded bg-gray-100 px-1.5 py-0.5 text-[10px] font-semibold text-gray-500">DB Event</span>
									<span>INSERT / UPDATE / DELETE on table</span>
									<span class="ml-auto inline-flex items-center rounded-full bg-amber-50 border border-amber-200 px-2 py-0.5 text-[10px] font-medium text-amber-600">Coming soon</span>
								</div>
							</div>
						</div>

						<!-- Code editor -->
						<div class="h-[400px]">
							<CodeEditor
								bind:this={codeEditor}
								value={editorCode}
								onchange={(v) => { editorCode = v; }}
								placeholder="// Write your function code here..."
							/>
						</div>

						<!-- Context reference -->
						<div class="border-t border-gray-200 px-4 py-3 bg-gray-50">
							<p class="text-xs font-medium text-gray-500 mb-1.5">Available context:</p>
							<div class="flex flex-wrap gap-2">
								{#each ['ctx.db.sql(query, params)', 'ctx.vault.get(name)', 'ctx.storage.upload(key, body)', 'ctx.user.id', 'ctx.log.info(msg)'] as ref}
									<code class="rounded bg-gray-200 px-1.5 py-0.5 text-xs text-gray-700">{ref}</code>
								{/each}
							</div>
						</div>

						<!-- Logs panel -->
						{#if showLogs}
							<div class="border-t border-gray-200">
								<div class="flex items-center justify-between px-4 py-2 bg-gray-50">
									<h4 class="text-xs font-semibold text-gray-700">Execution Logs</h4>
									<button onclick={() => { showLogs = false; }} class="cursor-pointer text-xs text-gray-500 hover:text-gray-700">Close</button>
								</div>
								{#if logs.length === 0}
									<p class="px-4 py-6 text-center text-xs text-gray-500">No invocations yet</p>
								{:else}
									<div class="max-h-60 overflow-y-auto">
										<table class="w-full text-xs">
											<thead class="bg-gray-50 text-gray-500">
												<tr>
													<th class="px-4 py-2 text-left font-medium">Method</th>
													<th class="px-4 py-2 text-left font-medium">Status</th>
													<th class="px-4 py-2 text-left font-medium">Duration</th>
													<th class="px-4 py-2 text-left font-medium">Error</th>
													<th class="px-4 py-2 text-left font-medium">Time</th>
												</tr>
											</thead>
											<tbody class="divide-y divide-gray-100">
												{#each logs as log}
													<tr>
														<td class="px-4 py-1.5 font-mono">{log.request_method}</td>
														<td class="px-4 py-1.5 font-mono {statusColor(log.status)}">{log.status}</td>
														<td class="px-4 py-1.5">{log.duration_ms}ms</td>
														<td class="px-4 py-1.5 text-red-600 max-w-[200px] truncate">{log.error || ''}</td>
														<td class="px-4 py-1.5 text-gray-500">{new Date(log.created_at).toLocaleString()}</td>
													</tr>
												{/each}
											</tbody>
										</table>
									</div>
								{/if}
							</div>
						{/if}
					</div>
				{:else}
					<div class="flex h-64 items-center justify-center rounded-lg border border-dashed border-gray-300">
						<p class="text-sm text-gray-500">Select a function to edit, or create a new one</p>
					</div>
				{/if}
			</div>

			<!-- Schema browser sidebar -->
			<div class="col-span-3">
				<div class="rounded-lg border border-gray-200 bg-white overflow-hidden">
					<div class="border-b border-gray-200 px-4 py-3">
						<h3 class="text-xs font-semibold uppercase tracking-wider text-gray-500">Tables</h3>
					</div>
					<div class="max-h-[500px] overflow-y-auto p-2 space-y-0.5">
						{#if schemaLoading}
							{#each Array(4) as _}
								<div class="h-7 animate-pulse rounded-lg bg-gray-100"></div>
							{/each}
						{:else if tables.length === 0}
							<div class="px-3 py-6 text-center text-xs text-gray-400">No tables</div>
						{:else}
							{#each tables as table}
								<div>
									<div class="flex items-center gap-0 rounded-lg transition-colors
										{expandedTable === table.name
											? 'bg-gray-100 text-gray-900'
											: 'text-gray-600 hover:bg-gray-50 hover:text-gray-900'}">
										<button
											type="button"
											class="cursor-pointer shrink-0 p-1.5 rounded-l-lg"
											onclick={() => {
												expandedTable = expandedTable === table.name ? null : table.name;
											}}
											title="Expand columns"
										>
											<svg class="h-3.5 w-3.5 text-gray-400 transition-transform {expandedTable === table.name ? 'rotate-90' : ''}" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
												<path stroke-linecap="round" stroke-linejoin="round" d="m8.25 4.5 7.5 7.5-7.5 7.5" />
											</svg>
										</button>
										<button
											type="button"
											class="cursor-pointer flex flex-1 items-center gap-2 py-1.5 pr-2.5 rounded-r-lg min-w-0"
											onclick={() => insertText(table.name)}
											title="Click to insert table name"
										>
											<svg class="h-3.5 w-3.5 shrink-0 text-gray-400" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
												<path stroke-linecap="round" stroke-linejoin="round" d="M3.375 19.5h17.25m-17.25 0a1.125 1.125 0 0 1-1.125-1.125M3.375 19.5h7.5c.621 0 1.125-.504 1.125-1.125m-9.75 0V5.625m0 12.75v-1.5c0-.621.504-1.125 1.125-1.125m18.375 2.625V5.625m0 12.75c0 .621-.504 1.125-1.125 1.125m1.125-1.125v-1.5c0-.621-.504-1.125-1.125-1.125m0 3.75h-7.5A1.125 1.125 0 0 1 12 18.375m9.75-12.75c0-.621-.504-1.125-1.125-1.125H3.375c-.621 0-1.125.504-1.125 1.125m19.5 0v1.5c0 .621-.504 1.125-1.125 1.125M2.25 5.625v1.5c0 .621.504 1.125 1.125 1.125m0 0h17.25M3.375 8.25h7.5c.621 0 1.125.504 1.125 1.125" />
											</svg>
											<span class="truncate text-xs font-medium">{table.name}</span>
										</button>
									</div>
									{#if expandedTable === table.name}
										<div class="ml-6 mt-0.5 mb-1 space-y-px">
											{#each table.columns as col}
												<button
													type="button"
													class="cursor-pointer flex w-full items-center gap-1.5 rounded px-2 py-1 text-[11px] text-gray-500 hover:bg-gray-50 hover:text-gray-700 transition-colors"
													onclick={() => insertText(col.name)}
													title="Click to insert column name"
												>
													<span class="font-mono truncate">{col.name}</span>
													<span class="ml-auto shrink-0 rounded px-1 py-0.5 text-[9px] font-medium {typeBadgeColor(shortType(col.data_type))}">
														{shortType(col.data_type)}
													</span>
												</button>
											{/each}
										</div>
									{/if}
								</div>
							{/each}
						{/if}
					</div>
				</div>
			</div>
		</div>
	{/if}
</div>
