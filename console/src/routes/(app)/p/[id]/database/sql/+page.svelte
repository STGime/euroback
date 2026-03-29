<script lang="ts">
	import { page } from '$app/stores';
	import { onMount } from 'svelte';
	import { api, type TableSchema } from '$lib/api.js';
	import SqlEditor from '$lib/components/SqlEditor.svelte';
	import ResultsTable from '$lib/components/ResultsTable.svelte';

	let projectId = $derived($page.params.id);

	// ---- Schema sidebar state ----
	let tables: TableSchema[] = $state([]);
	let schemaLoading = $state(true);
	let expandedTable: string | null = $state(null);

	// ---- Tab state ----
	interface QueryTab {
		id: string;
		name: string;
		sql: string;
		columns: string[];
		rows: Record<string, any>[];
		rowCount: number;
		executionTimeMs: number;
		error: string | null;
	}

	let tabs: QueryTab[] = $state([]);
	let activeTabId: string = $state('');
	let activeTab: QueryTab | undefined = $derived(tabs.find((t) => t.id === activeTabId));

	// ---- Execution state ----
	let executing = $state(false);

	// ---- History state ----
	let showHistory = $state(false);
	let history: { sql: string; timestamp: number }[] = $state([]);

	// ---- localStorage keys ----
	function storageKey(suffix: string) {
		return `eurobase_sql_${projectId}_${suffix}`;
	}

	onMount(() => {
		loadSchema();
		loadState();
		loadHistory();
		if (tabs.length === 0) {
			addTab();
		}
	});

	async function loadSchema() {
		schemaLoading = true;
		const hiddenTables = new Set(['users', 'refresh_tokens', 'storage_objects', 'email_tokens', 'vault_secrets']);
		try {
			tables = (await api.getSchema(projectId)).filter(t => !hiddenTables.has(t.name));
		} catch {
			// Schema load failure is non-critical for the SQL editor
		} finally {
			schemaLoading = false;
		}
	}

	function insertText(text: string) {
		if (!activeTab) return;
		const idx = tabs.findIndex((t) => t.id === activeTabId);
		if (idx >= 0) {
			const current = tabs[idx].sql;
			// Append with a space if there's existing content
			tabs[idx].sql = current ? current.trimEnd() + ' ' + text : text;
			saveState();
		}
	}

	function loadState() {
		try {
			const saved = localStorage.getItem(storageKey('tabs'));
			if (saved) {
				const parsed = JSON.parse(saved);
				if (Array.isArray(parsed) && parsed.length > 0) {
					tabs = parsed;
					const savedActive = localStorage.getItem(storageKey('active'));
					activeTabId = savedActive && tabs.find((t) => t.id === savedActive)
						? savedActive
						: tabs[0].id;
					return;
				}
			}
		} catch {}
	}

	function saveState() {
		try {
			localStorage.setItem(storageKey('tabs'), JSON.stringify(tabs));
			localStorage.setItem(storageKey('active'), activeTabId);
		} catch {}
	}

	function loadHistory() {
		try {
			const saved = localStorage.getItem(storageKey('history'));
			if (saved) history = JSON.parse(saved);
		} catch {}
	}

	function saveHistory() {
		try {
			localStorage.setItem(storageKey('history'), JSON.stringify(history.slice(0, 50)));
		} catch {}
	}

	function addTab() {
		const id = crypto.randomUUID();
		const num = tabs.length + 1;
		const tab: QueryTab = {
			id,
			name: `Query ${num}`,
			sql: '',
			columns: [],
			rows: [],
			rowCount: 0,
			executionTimeMs: 0,
			error: null
		};
		tabs = [...tabs, tab];
		activeTabId = id;
		saveState();
	}

	function closeTab(id: string) {
		if (tabs.length <= 1) return;
		const idx = tabs.findIndex((t) => t.id === id);
		tabs = tabs.filter((t) => t.id !== id);
		if (activeTabId === id) {
			activeTabId = tabs[Math.max(0, idx - 1)].id;
		}
		saveState();
	}

	function updateTabSQL(value: string) {
		if (!activeTab) return;
		const idx = tabs.findIndex((t) => t.id === activeTabId);
		if (idx >= 0) {
			tabs[idx].sql = value;
			saveState();
		}
	}

	function isDestructiveSQL(sql: string): boolean {
		const upper = sql.toUpperCase().trim();
		return /^(DROP|DELETE|TRUNCATE|ALTER\s+TABLE\s+\S+\s+DROP)/.test(upper);
	}

	let showDestructiveConfirm = $state(false);

	async function executeQuery() {
		if (!activeTab || !activeTab.sql.trim() || executing) return;
		const idx = tabs.findIndex((t) => t.id === activeTabId);
		if (idx < 0) return;

		if (isDestructiveSQL(activeTab.sql.trim())) {
			showDestructiveConfirm = true;
			return;
		}

		await runQuery();
	}

	async function runQuery() {
		if (!activeTab || !activeTab.sql.trim() || executing) return;
		const idx = tabs.findIndex((t) => t.id === activeTabId);
		if (idx < 0) return;

		showDestructiveConfirm = false;
		executing = true;
		tabs[idx].error = null;

		try {
			const result = await api.executeSQL(projectId, activeTab.sql.trim());
			tabs[idx].columns = result.columns;
			tabs[idx].rows = result.rows;
			tabs[idx].rowCount = result.row_count;
			tabs[idx].executionTimeMs = result.execution_time_ms;

			// Add to history.
			history = [
				{ sql: activeTab.sql.trim(), timestamp: Date.now() },
				...history.filter((h) => h.sql !== activeTab!.sql.trim())
			].slice(0, 50);
			saveHistory();

			// Refresh schema sidebar after DDL operations.
			const upper = activeTab.sql.trim().toUpperCase();
			if (/^(CREATE|DROP|ALTER|RENAME)/.test(upper)) {
				loadSchema();
			}
		} catch (err) {
			let msg = err instanceof Error ? err.message : 'Query failed';
			const jsonMatch = msg.match(/\{"error":"(.+?)"\}/);
			if (jsonMatch) msg = jsonMatch[1];

			// Enhance dependency errors with helpful context.
			if (msg.includes('other objects depend on it') || msg.includes('2BP01')) {
				const tableMatch = msg.match(/drop table (\w+)/i);
				const tableName = tableMatch ? tableMatch[1] : null;
				if (tableName) {
					// Look up which tables have FKs pointing to this table.
					const dependents = tables
						.filter(t => t.columns.some(c => c.foreign_key?.referenced_table === tableName))
						.map(t => {
							const fkCol = t.columns.find(c => c.foreign_key?.referenced_table === tableName);
							return `${t.name}.${fkCol?.name} → ${tableName}.${fkCol?.foreign_key?.referenced_column}`;
						});
					if (dependents.length > 0) {
						msg = `Cannot drop "${tableName}" because these tables reference it:\n\n` +
							dependents.map(d => `  • ${d}`).join('\n') +
							`\n\nDrop the dependent tables first, remove the foreign keys, or use DROP TABLE ${tableName} CASCADE to force.`;
					} else {
						msg = `Cannot drop "${tableName}" — other objects depend on it. Use DROP TABLE ${tableName} CASCADE to force (this will also drop dependent objects).`;
					}
				}
			}

			tabs[idx].error = msg;
			tabs[idx].columns = [];
			tabs[idx].rows = [];
			tabs[idx].rowCount = 0;
		} finally {
			executing = false;
			saveState();
		}
	}

	function selectHistoryItem(sql: string) {
		if (!activeTab) return;
		const idx = tabs.findIndex((t) => t.id === activeTabId);
		if (idx >= 0) {
			tabs[idx].sql = sql;
			saveState();
		}
		showHistory = false;
	}

	function handleKeydown(e: KeyboardEvent) {
		if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
			e.preventDefault();
			executeQuery();
		}
		if ((e.metaKey || e.ctrlKey) && e.key === 'n') {
			e.preventDefault();
			addTab();
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

<svelte:window onkeydown={handleKeydown} />

<div class="flex gap-4 h-[calc(100vh-16rem)] overflow-hidden">
	<!-- Schema sidebar -->
	<div class="w-52 shrink-0 flex flex-col rounded-xl border border-gray-200 bg-white overflow-hidden">
		<div class="border-b border-gray-200 px-4 py-3">
			<h3 class="text-xs font-semibold uppercase tracking-wider text-gray-500">Tables</h3>
		</div>
		<div class="flex-1 overflow-y-auto p-2 space-y-0.5">
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

	<!-- Main editor area -->
	<div class="flex-1 flex flex-col min-w-0 overflow-hidden">
		<!-- Query tabs bar -->
		<div class="flex items-center gap-1 mb-3">
			<div class="flex items-center gap-0.5 flex-1 overflow-x-auto">
				{#each tabs as tab}
					<button
						type="button"
						class="cursor-pointer group flex items-center gap-1.5 rounded-t-lg px-3 py-1.5 text-xs font-medium transition-colors whitespace-nowrap
							{activeTabId === tab.id
								? 'bg-gray-800 text-white'
								: 'bg-gray-100 text-gray-600 hover:bg-gray-200'}"
						onclick={() => { activeTabId = tab.id; saveState(); }}
					>
						{tab.name}
						{#if tabs.length > 1}
							<span
								class="ml-1 rounded-full p-0.5 opacity-0 group-hover:opacity-100 transition-opacity
									{activeTabId === tab.id ? 'hover:bg-gray-700' : 'hover:bg-gray-300'}"
								role="button"
								tabindex="-1"
								onclick={(e) => { e.stopPropagation(); closeTab(tab.id); }}
							>
								<svg class="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
									<path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" />
								</svg>
							</span>
						{/if}
					</button>
				{/each}
				<button
					type="button"
					class="cursor-pointer rounded-lg p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600 transition-colors"
					onclick={addTab}
					title="New query (Ctrl+N)"
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
					</svg>
				</button>
			</div>
			<button
				type="button"
				class="cursor-pointer inline-flex items-center gap-1.5 rounded-lg bg-eurobase-600 px-4 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
				onclick={executeQuery}
				disabled={executing || !activeTab?.sql.trim()}
				title="Run query (Ctrl+Enter)"
			>
				{#if executing}
					<svg class="h-3.5 w-3.5 animate-spin" viewBox="0 0 24 24" fill="none">
						<circle cx="12" cy="12" r="10" stroke="currentColor" stroke-width="3" class="opacity-25" />
						<path d="M4 12a8 8 0 018-8" stroke="currentColor" stroke-width="3" stroke-linecap="round" class="opacity-75" />
					</svg>
				{:else}
					<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M5.25 5.653c0-.856.917-1.398 1.667-.986l11.54 6.347a1.125 1.125 0 0 1 0 1.972l-11.54 6.347a1.125 1.125 0 0 1-1.667-.986V5.653Z" />
					</svg>
				{/if}
				Run
			</button>
		</div>

		<!-- Editor area -->
		{#if activeTab}
			<div class="h-48 shrink-0 mb-3">
				<SqlEditor
					value={activeTab.sql}
					onchange={updateTabSQL}
					onexecute={executeQuery}
				/>
			</div>

			<!-- Status bar -->
			<div class="flex items-center justify-between mb-3 text-xs text-gray-500">
				<div class="flex items-center gap-3">
					{#if activeTab.error}
						<!-- empty — error shown below -->
					{:else if activeTab.rowCount > 0 || activeTab.columns.length > 0}
						<span>
							{activeTab.rowCount} {activeTab.rowCount === 1 ? 'row' : 'rows'}
							&middot; {activeTab.executionTimeMs.toFixed(1)}ms
						</span>
					{/if}
				</div>
				<div class="relative">
					<button
						type="button"
						class="cursor-pointer inline-flex items-center gap-1 rounded-md border border-gray-300 bg-white px-2.5 py-1 text-xs text-gray-600 hover:bg-gray-50 transition-colors"
						onclick={() => (showHistory = !showHistory)}
					>
						<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M12 6v6h4.5m4.5 0a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" />
						</svg>
						History
					</button>
					{#if showHistory}
						<div class="absolute right-0 top-full mt-1 z-20 w-96 max-h-64 overflow-y-auto rounded-lg border border-gray-200 bg-white shadow-lg">
							{#if history.length === 0}
								<div class="px-4 py-6 text-center text-xs text-gray-400">No query history yet</div>
							{:else}
								{#each history as item}
									<button
										type="button"
										class="cursor-pointer w-full text-left px-3 py-2 text-xs hover:bg-gray-50 transition-colors border-b border-gray-100 last:border-0"
										onclick={() => selectHistoryItem(item.sql)}
									>
										<code class="block truncate font-mono text-gray-700">{item.sql}</code>
										<span class="text-[10px] text-gray-400">
											{new Date(item.timestamp).toLocaleString()}
										</span>
									</button>
								{/each}
							{/if}
						</div>
					{/if}
				</div>
			</div>

			<!-- Error display -->
			{#if activeTab.error}
				<div class="mb-3 flex items-start gap-3 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm">
					<svg class="h-5 w-5 shrink-0 text-red-500 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
					</svg>
					<pre class="text-red-700 whitespace-pre-wrap font-mono text-xs flex-1">{activeTab.error}</pre>
				</div>
			{/if}

			<!-- Results table -->
			<div class="flex-1 min-h-0 overflow-hidden">
				<ResultsTable
					columns={activeTab.columns}
					rows={activeTab.rows}
					loading={executing}
				/>
			</div>
		{/if}
	</div>
</div>

{#if showDestructiveConfirm}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button type="button" class="fixed inset-0 bg-black/50 cursor-default" onclick={() => showDestructiveConfirm = false} tabindex="-1" aria-label="Close"></button>
		<div class="relative z-10 w-full max-w-sm rounded-xl bg-white shadow-2xl p-6">
			<div class="flex items-center gap-3 mb-3">
				<div class="flex h-10 w-10 items-center justify-center rounded-full bg-amber-100">
					<svg class="h-5 w-5 text-amber-600" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
					</svg>
				</div>
				<div>
					<h3 class="text-sm font-semibold text-gray-900">Destructive Operation</h3>
					<p class="text-xs text-gray-500">This query will modify or delete data permanently.</p>
				</div>
			</div>
			<div class="rounded-lg bg-gray-900 px-3 py-2 mb-4">
				<code class="text-xs font-mono text-amber-400 break-all">{activeTab?.sql.trim().substring(0, 120)}{(activeTab?.sql.trim().length ?? 0) > 120 ? '...' : ''}</code>
			</div>
			<div class="flex justify-end gap-3">
				<button type="button" class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors" onclick={() => showDestructiveConfirm = false}>Cancel</button>
				<button type="button" class="cursor-pointer rounded-lg bg-amber-600 px-4 py-2 text-sm font-medium text-white hover:bg-amber-700 transition-colors" onclick={runQuery}>Execute</button>
			</div>
		</div>
	</div>
{/if}
