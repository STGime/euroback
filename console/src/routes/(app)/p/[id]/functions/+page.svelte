<script lang="ts">
	import { page } from '$app/stores';
	import { api, type EdgeFunction, type EdgeFunctionLog, type TableSchema, type FunctionTrigger, type EdgeFunctionVersion, type FunctionMetrics } from '$lib/api.js';
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

	// Triggers state
	let triggers: FunctionTrigger[] = $state([]);
	let showTriggerForm = $state(false);
	let triggerTable = $state('');
	let triggerEvents = $state<Record<string, boolean>>({ INSERT: false, UPDATE: false, DELETE: false });
	let savingTrigger = $state(false);

	// Environment variables state
	let showEnvVars = $state(false);
	let envPairs = $state<Array<{ key: string; value: string }>>([]);
	let savingEnv = $state(false);
	let envMasked = $state(true);

	// Versioning state
	let showVersions = $state(false);
	let versions: EdgeFunctionVersion[] = $state([]);
	let loadingVersions = $state(false);

	// Metrics state
	let showMetrics = $state(false);
	let metrics: FunctionMetrics | null = $state(null);
	let metricsPeriod = $state('24h');
	let loadingMetrics = $state(false);

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
			showVersions = false;
			showMetrics = false;
			logs = [];
			triggers = [];
			versions = [];
			metrics = null;

			// Load env vars into pairs
			const ev = full.env_vars || {};
			envPairs = Object.entries(ev).map(([key, value]) => ({ key, value }));
			if (envPairs.length === 0) envPairs = [{ key: '', value: '' }];

			// Load triggers
			await loadTriggers(fn.name);
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
		showVersions = false;
		showMetrics = false;
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

	// ── Triggers ──

	async function loadTriggers(name: string) {
		try {
			triggers = await api.listFunctionTriggers(projectId, name);
		} catch {
			// Non-critical
		}
	}

	async function createTrigger() {
		if (!selectedFn || !triggerTable) return;
		const events = Object.entries(triggerEvents).filter(([, v]) => v).map(([k]) => k);
		if (events.length === 0) {
			error = 'Select at least one event';
			return;
		}
		savingTrigger = true;
		try {
			await api.createFunctionTrigger(projectId, selectedFn.name, {
				table_name: triggerTable,
				events
			});
			showTriggerForm = false;
			triggerTable = '';
			triggerEvents = { INSERT: false, UPDATE: false, DELETE: false };
			await loadTriggers(selectedFn.name);
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to create trigger';
		} finally {
			savingTrigger = false;
		}
	}

	async function deleteTrigger(triggerId: string) {
		if (!selectedFn) return;
		try {
			await api.deleteFunctionTrigger(projectId, selectedFn.name, triggerId);
			await loadTriggers(selectedFn.name);
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to delete trigger';
		}
	}

	// ── Environment Variables ──

	function addEnvPair() {
		envPairs = [...envPairs, { key: '', value: '' }];
	}

	function removeEnvPair(index: number) {
		envPairs = envPairs.filter((_, i) => i !== index);
		if (envPairs.length === 0) envPairs = [{ key: '', value: '' }];
	}

	async function saveEnvVars() {
		if (!selectedFn) return;
		savingEnv = true;
		try {
			const env_vars: Record<string, string> = {};
			for (const pair of envPairs) {
				if (pair.key.trim()) {
					env_vars[pair.key.trim()] = pair.value;
				}
			}
			await api.updateEdgeFunction(projectId, selectedFn.name, { env_vars });
			selectedFn = { ...selectedFn, env_vars };
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to save environment variables';
		} finally {
			savingEnv = false;
		}
	}

	// ── Versions ──

	async function loadVersions() {
		if (!selectedFn) return;
		showVersions = true;
		showLogs = false;
		showMetrics = false;
		loadingVersions = true;
		try {
			versions = await api.listFunctionVersions(projectId, selectedFn.name);
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load versions';
		} finally {
			loadingVersions = false;
		}
	}

	async function rollbackToVersion(version: number) {
		if (!selectedFn) return;
		try {
			const updated = await api.rollbackFunction(projectId, selectedFn.name, version);
			selectedFn = updated;
			editorCode = updated.code || '';
			await loadFunctions();
			await loadVersions();
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to rollback';
		}
	}

	// ── Metrics ──

	async function loadMetrics(period?: string) {
		if (!selectedFn) return;
		showMetrics = true;
		showLogs = false;
		showVersions = false;
		loadingMetrics = true;
		if (period) metricsPeriod = period;
		try {
			metrics = await api.getFunctionMetrics(projectId, selectedFn.name, metricsPeriod);
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load metrics';
		} finally {
			loadingMetrics = false;
		}
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
									onclick={() => loadMetrics()}
									class="cursor-pointer rounded-md border border-gray-300 px-3 py-1.5 text-xs font-medium text-gray-700 hover:bg-gray-50"
								>Metrics</button>
								<button
									onclick={loadVersions}
									class="cursor-pointer rounded-md border border-gray-300 px-3 py-1.5 text-xs font-medium text-gray-700 hover:bg-gray-50"
								>Versions</button>
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
							<div class="flex items-center justify-between mb-1.5">
								<p class="text-[11px] font-semibold uppercase tracking-wider text-gray-400">Triggers</p>
								<button
									onclick={() => { showTriggerForm = !showTriggerForm; }}
									class="cursor-pointer text-[11px] font-medium text-eurobase-600 hover:text-eurobase-700"
								>{showTriggerForm ? 'Cancel' : '+ Add Trigger'}</button>
							</div>
							<div class="space-y-1.5">
								<div class="flex items-center gap-2 text-xs text-gray-600">
									<span class="inline-flex items-center rounded bg-green-100 px-1.5 py-0.5 text-[10px] font-semibold text-green-700">HTTP</span>
									<code class="font-mono text-gray-500">POST /v1/functions/{selectedFn.name}</code>
								</div>

								{#each triggers as trigger}
									<div class="flex items-center gap-2 text-xs text-gray-600">
										<span class="inline-flex items-center rounded bg-blue-100 px-1.5 py-0.5 text-[10px] font-semibold text-blue-700">DB</span>
										<span class="font-mono text-gray-500">{trigger.events.join(', ')} on {trigger.table_name}</span>
										<button
											onclick={() => deleteTrigger(trigger.id)}
											class="cursor-pointer ml-auto text-[10px] text-red-500 hover:text-red-700"
										>remove</button>
									</div>
								{/each}

								{#if showTriggerForm}
									<div class="mt-2 rounded-lg border border-gray-200 bg-white p-3 space-y-2">
										<select
											bind:value={triggerTable}
											class="w-full rounded border-gray-300 text-xs px-2 py-1.5"
										>
											<option value="">Select a table...</option>
											{#each tables as table}
												<option value={table.name}>{table.name}</option>
											{/each}
										</select>
										<div class="flex items-center gap-3">
											{#each ['INSERT', 'UPDATE', 'DELETE'] as event}
												<label class="flex items-center gap-1 text-xs text-gray-700">
													<input type="checkbox" bind:checked={triggerEvents[event]} class="rounded border-gray-300" />
													{event}
												</label>
											{/each}
										</div>
										<button
											onclick={createTrigger}
											disabled={savingTrigger || !triggerTable}
											class="cursor-pointer rounded bg-eurobase-600 px-3 py-1 text-xs font-medium text-white hover:bg-eurobase-700 disabled:opacity-50"
										>{savingTrigger ? 'Saving...' : 'Add Trigger'}</button>
									</div>
								{/if}
							</div>
						</div>

						<!-- Environment Variables (collapsible) -->
						<div class="border-b border-gray-100 px-4 py-2.5 bg-gray-50/50">
							<button
								onclick={() => { showEnvVars = !showEnvVars; }}
								class="cursor-pointer flex items-center gap-1.5 w-full text-left"
							>
								<svg class="h-3.5 w-3.5 text-gray-400 transition-transform {showEnvVars ? 'rotate-90' : ''}" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
									<path stroke-linecap="round" stroke-linejoin="round" d="m8.25 4.5 7.5 7.5-7.5 7.5" />
								</svg>
								<span class="text-[11px] font-semibold uppercase tracking-wider text-gray-400">Environment Variables</span>
								<span class="text-[10px] text-gray-400 ml-1">({envPairs.filter(p => p.key.trim()).length})</span>
							</button>

							{#if showEnvVars}
								<div class="mt-2 space-y-1.5">
									{#each envPairs as pair, i}
										<div class="flex items-center gap-2">
											<input
												type="text"
												bind:value={pair.key}
												placeholder="KEY"
												class="w-1/3 rounded border-gray-300 text-xs px-2 py-1.5 font-mono"
											/>
											<input
												type={envMasked ? 'password' : 'text'}
												bind:value={pair.value}
												placeholder="value"
												class="flex-1 rounded border-gray-300 text-xs px-2 py-1.5 font-mono"
											/>
											<button
												onclick={() => removeEnvPair(i)}
												class="cursor-pointer text-xs text-red-500 hover:text-red-700 shrink-0"
											>x</button>
										</div>
									{/each}
									<div class="flex items-center gap-2">
										<button
											onclick={addEnvPair}
											class="cursor-pointer text-xs font-medium text-eurobase-600 hover:text-eurobase-700"
										>+ Add variable</button>
										<button
											onclick={() => { envMasked = !envMasked; }}
											class="cursor-pointer text-xs text-gray-500 hover:text-gray-700"
										>{envMasked ? 'Show values' : 'Hide values'}</button>
										<button
											onclick={saveEnvVars}
											disabled={savingEnv}
											class="cursor-pointer ml-auto rounded bg-eurobase-600 px-3 py-1 text-xs font-medium text-white hover:bg-eurobase-700 disabled:opacity-50"
										>{savingEnv ? 'Saving...' : 'Save'}</button>
									</div>
								</div>
							{/if}
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
								{#each ['ctx.db.sql(query, params)', 'ctx.vault.get(name)', 'ctx.storage.upload(key, body)', 'ctx.user.id', 'ctx.log.info(msg)', 'ctx.env.KEY'] as ref}
									<code class="rounded bg-gray-200 px-1.5 py-0.5 text-xs text-gray-700">{ref}</code>
								{/each}
							</div>
						</div>

						<!-- Metrics panel -->
						{#if showMetrics}
							<div class="border-t border-gray-200">
								<div class="flex items-center justify-between px-4 py-2 bg-gray-50">
									<h4 class="text-xs font-semibold text-gray-700">Metrics</h4>
									<div class="flex items-center gap-2">
										{#each ['24h', '7d', '30d'] as p}
											<button
												onclick={() => loadMetrics(p)}
												class="cursor-pointer text-xs px-2 py-0.5 rounded {metricsPeriod === p ? 'bg-eurobase-100 text-eurobase-700 font-medium' : 'text-gray-500 hover:text-gray-700'}"
											>{p}</button>
										{/each}
										<button onclick={() => { showMetrics = false; }} class="cursor-pointer text-xs text-gray-500 hover:text-gray-700 ml-2">Close</button>
									</div>
								</div>
								{#if loadingMetrics}
									<div class="px-4 py-6 text-center text-xs text-gray-500">Loading metrics...</div>
								{:else if metrics}
									<div class="grid grid-cols-4 gap-4 px-4 py-4">
										<div class="text-center">
											<p class="text-2xl font-semibold text-gray-900">{metrics.total_invocations}</p>
											<p class="text-[11px] text-gray-500">Invocations</p>
										</div>
										<div class="text-center">
											<p class="text-2xl font-semibold {metrics.error_rate > 5 ? 'text-red-600' : 'text-gray-900'}">{metrics.error_rate.toFixed(1)}%</p>
											<p class="text-[11px] text-gray-500">Error Rate</p>
										</div>
										<div class="text-center">
											<p class="text-2xl font-semibold text-gray-900">{metrics.avg_duration_ms.toFixed(0)}ms</p>
											<p class="text-[11px] text-gray-500">Avg Duration</p>
										</div>
										<div class="text-center">
											<p class="text-2xl font-semibold text-gray-900">{metrics.p95_duration_ms.toFixed(0)}ms</p>
											<p class="text-[11px] text-gray-500">p95 Duration</p>
										</div>
									</div>
								{:else}
									<p class="px-4 py-6 text-center text-xs text-gray-500">No metrics available</p>
								{/if}
							</div>
						{/if}

						<!-- Versions panel -->
						{#if showVersions}
							<div class="border-t border-gray-200">
								<div class="flex items-center justify-between px-4 py-2 bg-gray-50">
									<h4 class="text-xs font-semibold text-gray-700">Version History</h4>
									<button onclick={() => { showVersions = false; }} class="cursor-pointer text-xs text-gray-500 hover:text-gray-700">Close</button>
								</div>
								{#if loadingVersions}
									<div class="px-4 py-6 text-center text-xs text-gray-500">Loading versions...</div>
								{:else if versions.length === 0}
									<p class="px-4 py-6 text-center text-xs text-gray-500">No previous versions</p>
								{:else}
									<div class="max-h-60 overflow-y-auto">
										<table class="w-full text-xs">
											<thead class="bg-gray-50 text-gray-500">
												<tr>
													<th class="px-4 py-2 text-left font-medium">Version</th>
													<th class="px-4 py-2 text-left font-medium">Created</th>
													<th class="px-4 py-2 text-right font-medium">Action</th>
												</tr>
											</thead>
											<tbody class="divide-y divide-gray-100">
												{#each versions as v}
													<tr>
														<td class="px-4 py-1.5 font-mono">v{v.version}</td>
														<td class="px-4 py-1.5 text-gray-500">{new Date(v.created_at).toLocaleString()}</td>
														<td class="px-4 py-1.5 text-right">
															<button
																onclick={() => rollbackToVersion(v.version)}
																class="cursor-pointer text-eurobase-600 hover:text-eurobase-700 font-medium"
															>Restore</button>
														</td>
													</tr>
												{/each}
											</tbody>
										</table>
									</div>
								{/if}
							</div>
						{/if}

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
