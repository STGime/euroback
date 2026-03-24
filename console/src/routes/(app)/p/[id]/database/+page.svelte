<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { api, type TableSchema, type ColumnInfo } from '$lib/api.js';
	import DataGrid from '$lib/components/DataGrid.svelte';
	import IndexPanel from './IndexPanel.svelte';
	import NewTableModal from './NewTableModal.svelte';
	import RenameTableModal from './RenameTableModal.svelte';
	import ColumnEditModal from './ColumnEditModal.svelte';

	let projectId = $derived($page.params.id);

	// ---- Schema state ----
	let tables: TableSchema[] = $state([]);
	let schemaLoading = $state(true);
	let schemaError: string | null = $state(null);

	// ---- Selected table state ----
	let selectedTable: string | null = $state(null);
	let selectedSchema: TableSchema | null = $derived(
		tables.find((t) => t.name === selectedTable) ?? null
	);

	// ---- Data state ----
	let rows: any[] = $state([]);
	let totalCount = $state(0);
	let dataLoading = $state(false);
	let dataError: string | null = $state(null);
	let currentOffset = $state(0);
	const pageSize = 20;

	// ---- Selection state ----
	let selectedIds: Set<string> = $state(new Set());

	// ---- Filter state ----
	let showFilters = $state(false);
	let filterColumn = $state('');
	let filterOperator = $state('eq');
	let filterValue = $state('');
	let sortColumn = $state('');
	let sortDirection = $state<'asc' | 'desc'>('asc');

	// ---- Modal state ----
	let showNewTableModal = $state(false);
	let showInsertModal = $state(false);
	let insertFormData: Record<string, string> = $state({});
	let insertError: string | null = $state(null);
	let showDeleteConfirm: any = $state(null);
	let showDropTableConfirm: string | null = $state(null);
	let dropTableError: string | null = $state(null);

	// ---- Rename table modal ----
	let showRenameTableModal = $state(false);
	let renameTableTarget: string | null = $state(null);

	// ---- Column edit modal ----
	let showColumnEditModal = $state(false);
	let editColumnTarget: ColumnInfo | null = $state(null);

	// ---- Add column modal ----
	let showAddColumnModal = $state(false);
	let addColName = $state('');
	let addColType = $state('text');
	let addColNullable = $state(true);
	let addColUnique = $state(false);
	let addColDefault = $state('');
	let addColError: string | null = $state(null);
	let addColSaving = $state(false);

	// ---- Bulk delete ----
	let showBulkDeleteConfirm = $state(false);
	let bulkDeleteError: string | null = $state(null);

	// ---- Drop column confirm ----
	let showDropColumnConfirm: ColumnInfo | null = $state(null);
	let dropColumnError: string | null = $state(null);

	// ---- Pagination ----
	let pageStart = $derived(totalCount > 0 ? currentOffset + 1 : 0);
	let pageEnd = $derived(Math.min(currentOffset + pageSize, totalCount));
	let hasPrev = $derived(currentOffset > 0);
	let hasNext = $derived(currentOffset + pageSize < totalCount);

	// ---- Load schema on mount ----
	onMount(() => {
		loadSchema();
	});

	// ---- Reload data when table selection changes ----
	function selectTableAndLoad(name: string) {
		selectedTable = name;
		currentOffset = 0;
		selectedIds = new Set();
		showFilters = false;
		filterColumn = '';
		filterValue = '';
		sortColumn = '';
		loadTableData();
	}

	async function loadSchema() {
		schemaLoading = true;
		schemaError = null;
		try {
			const hiddenTables = new Set(['users', 'refresh_tokens']);
			tables = (await api.getSchema(projectId)).filter(t => !hiddenTables.has(t.name));
			if (tables.length > 0 && !selectedTable) {
				selectTableAndLoad(tables[0].name);
			}
		} catch (err) {
			schemaError = err instanceof Error ? err.message : 'Failed to load schema';
		} finally {
			schemaLoading = false;
		}
	}

	async function loadTableData() {
		if (!selectedTable) return;
		dataLoading = true;
		dataError = null;
		try {
			const filters: Record<string, string> = {};
			if (filterColumn && filterValue) {
				filters[`${filterColumn}`] = `${filterOperator}.${filterValue}`;
			}
			const order = sortColumn ? `${sortColumn}.${sortDirection}` : undefined;
			const result = await api.queryTable(projectId, selectedTable, {
				limit: pageSize,
				offset: currentOffset,
				order,
				filters: Object.keys(filters).length > 0 ? filters : undefined
			});
			rows = result.data;
			totalCount = result.count;
		} catch (err) {
			dataError = err instanceof Error ? err.message : 'Failed to load data';
		} finally {
			dataLoading = false;
		}
	}

	function selectTable(name: string) {
		selectTableAndLoad(name);
	}

	function prevPage() {
		if (hasPrev) {
			currentOffset = Math.max(0, currentOffset - pageSize);
			void loadTableData();
		}
	}

	function nextPage() {
		if (hasNext) {
			currentOffset += pageSize;
			void loadTableData();
		}
	}

	function refresh() {
		void loadTableData();
	}

	function applyFilter() {
		currentOffset = 0;
		void loadTableData();
	}

	function clearFilter() {
		filterColumn = '';
		filterOperator = 'eq';
		filterValue = '';
		currentOffset = 0;
		void loadTableData();
	}

	function applySort(column: string) {
		if (sortColumn === column) {
			sortDirection = sortDirection === 'asc' ? 'desc' : 'asc';
		} else {
			sortColumn = column;
			sortDirection = 'asc';
		}
		currentOffset = 0;
		void loadTableData();
	}

	// ---- System tables ----
	// Managed by the platform — rows should not be manually inserted/deleted
	// as they are kept in sync with external services (e.g. S3).
	const systemTables = new Set(['storage_objects']);
	let isSystemTable = $derived(selectedTable ? systemTables.has(selectedTable) : false);

	// ---- Insert row ----
	// Context-aware button label based on table name.
	let insertLabel = $derived(
		selectedTable === 'storage_objects' ? 'Add Object' :
		`Insert Row`
	);

	// Columns that are fully auto-generated and should not appear in the insert form.
	const autoGeneratedColumns = new Set(['id', 'created_at', 'updated_at']);

	function isEditableColumn(col: import('$lib/api.js').ColumnInfo): boolean {
		return !autoGeneratedColumns.has(col.name);
	}

	function openInsertModal() {
		if (!selectedSchema) return;
		insertFormData = {};
		insertError = null;
		for (const col of selectedSchema.columns) {
			if (!isEditableColumn(col)) continue;
			insertFormData[col.name] = '';
		}
		showInsertModal = true;
	}

	function formatInsertError(err: unknown): string {
		const raw = err instanceof Error ? err.message : String(err);
		// Extract the inner error from API response like 'API 400: {"error":"..."}'
		const jsonMatch = raw.match(/\{[^}]*"error"\s*:\s*"([^"]*)"/);
		const detail = jsonMatch ? jsonMatch[1] : raw;
		// Parse PostgreSQL type errors: 'invalid value for type bigint: "abc"'
		const typeMatch = detail.match(/invalid (?:input syntax|value) for type (\w+):\s*"?([^"]*)"?/i);
		if (typeMatch) {
			const [, pgType, value] = typeMatch;
			return `Invalid value${value ? ` "${value}"` : ''} — expected type ${pgType}`;
		}
		// Parse not-null violations
		if (/null value.*violates not-null/i.test(detail)) {
			const colMatch = detail.match(/column "(\w+)"/);
			return colMatch ? `"${colMatch[1]}" is required and cannot be empty` : 'A required field is empty';
		}
		// Parse unique constraint violations
		if (/duplicate key|unique constraint/i.test(detail)) {
			const colMatch = detail.match(/Key \((\w+)\)/);
			return colMatch ? `A row with this "${colMatch[1]}" already exists` : 'This row violates a unique constraint';
		}
		return detail || 'Failed to insert row';
	}

	async function handleInsertRow() {
		if (!selectedTable) return;
		insertError = null;
		try {
			const data: Record<string, any> = {};
			for (const [key, value] of Object.entries(insertFormData)) {
				if (value.trim() !== '') {
					data[key] = value;
				}
			}
			await api.insertRow(projectId, selectedTable, data);
			showInsertModal = false;
			void loadTableData();
		} catch (err) {
			insertError = formatInsertError(err);
		}
	}

	// ---- Delete row ----
	async function handleDeleteRow() {
		if (!selectedTable || !showDeleteConfirm) return;
		const id = showDeleteConfirm.id;
		try {
			await api.deleteRow(projectId, selectedTable, id);
			showDeleteConfirm = null;
			void loadTableData();
		} catch (err) {
			dataError = err instanceof Error ? err.message : 'Failed to delete row';
		}
	}

	async function handleTableCreated(tableName: string, columns: { name: string; type: string; nullable: boolean; defaultValue: string; isPrimaryKey: boolean; isUnique?: boolean; fkTable?: string; fkColumn?: string; fkOnDelete?: string }[]) {
		await api.createTable(
			projectId,
			tableName,
			columns.map((c) => ({
				name: c.name,
				type: c.type,
				nullable: c.nullable,
				default_value: c.defaultValue || undefined,
				is_primary_key: c.isPrimaryKey,
				is_unique: c.isUnique || false,
				foreign_key: c.fkTable && c.fkColumn ? {
					column: c.name,
					referenced_table: c.fkTable,
					referenced_column: c.fkColumn,
					on_delete: c.fkOnDelete || 'NO ACTION'
				} : undefined
			}))
		);
		showNewTableModal = false;
		await loadSchema();
		selectTableAndLoad(tableName);
	}

	async function handleDropTable() {
		if (!showDropTableConfirm) return;
		const tableName = showDropTableConfirm;
		dropTableError = null;
		try {
			await api.dropTable(projectId, tableName);
			showDropTableConfirm = null;
			if (selectedTable === tableName) {
				selectedTable = null;
				rows = [];
				totalCount = 0;
			}
			await loadSchema();
			if (tables.length > 0 && !selectedTable) {
				selectTableAndLoad(tables[0].name);
			}
		} catch (err) {
			let msg = err instanceof Error ? err.message : 'Failed to drop table';
			const jsonMatch = msg.match(/\{"error":"(.+?)"\}/);
			if (jsonMatch) msg = jsonMatch[1];
			dropTableError = msg;
		}
	}

	const pgTypes = [
		'text', 'integer', 'bigint', 'smallint', 'boolean', 'uuid',
		'timestamp', 'timestamptz', 'jsonb', 'json', 'real', 'numeric',
		'date', 'time', 'bytea', 'serial', 'bigserial',
		'double precision', 'character varying', 'varchar'
	];

	function openAddColumnModal() {
		addColName = '';
		addColType = 'text';
		addColNullable = true;
		addColUnique = false;
		addColDefault = '';
		addColError = null;
		showAddColumnModal = true;
	}

	async function handleAddColumn() {
		if (!selectedTable || !addColName.trim()) return;
		addColSaving = true;
		addColError = null;
		try {
			await api.addColumn(projectId, selectedTable, {
				name: addColName.trim(),
				type: addColType,
				nullable: addColNullable,
				default_value: addColDefault || undefined
			});
			if (addColUnique) {
				await api.addUniqueConstraint(projectId, selectedTable, addColName.trim());
			}
			showAddColumnModal = false;
			await loadSchema();
			if (selectedTable) loadTableData();
		} catch (err) {
			const raw = err instanceof Error ? err.message : String(err);
			const jsonMatch = raw.match(/\{"error":"(.+?)"\}/);
			addColError = jsonMatch ? jsonMatch[1] : raw;
		} finally {
			addColSaving = false;
		}
	}

	async function handleDropColumn() {
		if (!selectedTable || !showDropColumnConfirm) return;
		dropColumnError = null;
		try {
			await api.dropColumn(projectId, selectedTable, showDropColumnConfirm.name);
			showDropColumnConfirm = null;
			await loadSchema();
			if (selectedTable) loadTableData();
		} catch (err) {
			const raw = err instanceof Error ? err.message : String(err);
			const jsonMatch = raw.match(/\{"error":"(.+?)"\}/);
			dropColumnError = jsonMatch ? jsonMatch[1] : raw;
		}
	}

	async function handleBulkDelete() {
		if (!selectedTable || selectedIds.size === 0) return;
		bulkDeleteError = null;
		try {
			await api.bulkDeleteRows(projectId, selectedTable, Array.from(selectedIds));
			showBulkDeleteConfirm = false;
			selectedIds = new Set();
			void loadTableData();
		} catch (err) {
			const raw = err instanceof Error ? err.message : String(err);
			const jsonMatch = raw.match(/\{"error":"(.+?)"\}/);
			bulkDeleteError = jsonMatch ? jsonMatch[1] : raw;
		}
	}

	const operators = [
		{ value: 'eq', label: '=' },
		{ value: 'neq', label: '!=' },
		{ value: 'gt', label: '>' },
		{ value: 'gte', label: '>=' },
		{ value: 'lt', label: '<' },
		{ value: 'lte', label: '<=' },
		{ value: 'like', label: 'LIKE' },
		{ value: 'ilike', label: 'ILIKE' },
		{ value: 'is', label: 'IS' }
	];
</script>

<div class="flex gap-6 h-[calc(100vh-13rem)] overflow-hidden">
	<!-- Left sidebar: table list -->
	<div class="w-56 shrink-0 flex flex-col rounded-xl border border-gray-200 bg-white overflow-hidden">
		<div class="flex items-center justify-between border-b border-gray-200 px-4 py-3">
			<h3 class="text-sm font-semibold text-gray-700">Tables</h3>
			<button
				type="button"
				class="cursor-pointer inline-flex items-center gap-1 rounded-md bg-eurobase-600 px-2 py-1 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors"
				onclick={() => (showNewTableModal = true)}
			>
				<svg class="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke-width="2.5" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
				</svg>
				New
			</button>
		</div>

		<div class="flex-1 overflow-y-auto p-2 space-y-0.5">
			{#if schemaLoading}
				{#each Array(4) as _}
					<div class="h-8 animate-pulse rounded-lg bg-gray-100"></div>
				{/each}
			{:else if schemaError}
				<div class="px-3 py-2 text-xs text-red-500">{schemaError}</div>
			{:else if tables.length === 0}
				<div class="px-3 py-6 text-center text-xs text-gray-400">
					No tables yet
				</div>
			{:else}
				{#each tables as table}
					<div class="group flex items-center rounded-lg transition-colors
						{selectedTable === table.name
							? 'bg-eurobase-50'
							: 'hover:bg-gray-50'}">
						<button
							type="button"
							class="cursor-pointer flex flex-1 items-center gap-2.5 min-w-0 px-3 py-2 text-sm transition-colors
								{selectedTable === table.name
									? 'text-eurobase-700 font-medium'
									: 'text-gray-600 hover:text-gray-900'}"
							onclick={() => selectTable(table.name)}
						>
							<svg class="h-4 w-4 shrink-0 {selectedTable === table.name ? 'text-eurobase-500' : 'text-gray-400'}" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" d="M3.375 19.5h17.25m-17.25 0a1.125 1.125 0 0 1-1.125-1.125M3.375 19.5h7.5c.621 0 1.125-.504 1.125-1.125m-9.75 0V5.625m0 12.75v-1.5c0-.621.504-1.125 1.125-1.125m18.375 2.625V5.625m0 12.75c0 .621-.504 1.125-1.125 1.125m1.125-1.125v-1.5c0-.621-.504-1.125-1.125-1.125m0 3.75h-7.5A1.125 1.125 0 0 1 12 18.375m9.75-12.75c0-.621-.504-1.125-1.125-1.125H3.375c-.621 0-1.125.504-1.125 1.125m19.5 0v1.5c0 .621-.504 1.125-1.125 1.125M2.25 5.625v1.5c0 .621.504 1.125 1.125 1.125m0 0h17.25M3.375 8.25h7.5c.621 0 1.125.504 1.125 1.125" />
							</svg>
							<span class="truncate">{table.name}</span>
							{#if systemTables.has(table.name)}
								<span class="shrink-0 rounded px-1 py-0.5 text-[9px] font-medium bg-eurobase-100 text-eurobase-600">system</span>
							{/if}
						</button>
						{#if !systemTables.has(table.name)}
							<button
								type="button"
								class="cursor-pointer shrink-0 mr-1.5 rounded p-1 text-gray-300 opacity-0 group-hover:opacity-100 hover:bg-red-50 hover:text-red-500 transition-all"
								onclick={(e) => { e.stopPropagation(); showDropTableConfirm = table.name; dropTableError = null; }}
								title="Drop table"
							>
								<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
									<path stroke-linecap="round" stroke-linejoin="round" d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0" />
								</svg>
							</button>
						{:else}
							<span class="shrink-0 mr-2 text-[10px] text-gray-400">{table.row_count}</span>
						{/if}
					</div>
				{/each}
			{/if}
		</div>
	</div>

	<!-- Main content area -->
	<div class="flex-1 flex flex-col min-w-0 overflow-hidden">
		{#if selectedTable && selectedSchema}
			<!-- Top toolbar -->
			<div class="flex items-center justify-between mb-4">
				<div class="flex items-center gap-3">
					<h2 class="text-lg font-semibold text-gray-900">{selectedTable}</h2>
					{#if isSystemTable}
						<span class="inline-flex items-center gap-1 rounded-full bg-eurobase-50 px-2 py-0.5 text-[10px] font-medium text-eurobase-600">
							<svg class="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" d="M16.5 10.5V6.75a4.5 4.5 0 1 0-9 0v3.75m-.75 11.25h10.5a2.25 2.25 0 0 0 2.25-2.25v-6.75a2.25 2.25 0 0 0-2.25-2.25H6.75a2.25 2.25 0 0 0-2.25 2.25v6.75a2.25 2.25 0 0 0 2.25 2.25Z" />
							</svg>
							System table
						</span>
					{:else}
						<button
							type="button"
							class="cursor-pointer inline-flex items-center gap-1 rounded-md border border-gray-200 bg-white px-2 py-0.5 text-[11px] font-medium text-gray-500 hover:bg-gray-50 hover:text-gray-700 transition-colors"
							onclick={() => { renameTableTarget = selectedTable; showRenameTableModal = true; }}
							title="Rename table"
						>
							<svg class="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" d="m16.862 4.487 1.687-1.688a1.875 1.875 0 1 1 2.652 2.652L6.832 19.82a4.5 4.5 0 0 1-1.897 1.13l-2.685.8.8-2.685a4.5 4.5 0 0 1 1.13-1.897L16.863 4.487Zm0 0L19.5 7.125" />
							</svg>
							Rename
						</button>
					{/if}
					<span class="text-xs text-gray-400">
						{selectedSchema.columns.length} columns / {totalCount} rows
					</span>
				</div>
				<div class="flex items-center gap-2">
					<button
						type="button"
						class="cursor-pointer inline-flex items-center gap-1.5 rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors
							{showFilters ? 'border-eurobase-300 bg-eurobase-50 text-eurobase-700' : ''}"
						onclick={() => (showFilters = !showFilters)}
					>
						<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M12 3c2.755 0 5.455.232 8.083.678.533.09.917.556.917 1.096v1.044a2.25 2.25 0 0 1-.659 1.591l-5.432 5.432a2.25 2.25 0 0 0-.659 1.591v2.927a2.25 2.25 0 0 1-1.244 2.013L9.75 21v-6.568a2.25 2.25 0 0 0-.659-1.591L3.659 7.409A2.25 2.25 0 0 1 3 5.818V4.774c0-.54.384-1.006.917-1.096A48.32 48.32 0 0 1 12 3Z" />
						</svg>
						Filter
					</button>
					<button
						type="button"
						class="cursor-pointer inline-flex items-center gap-1.5 rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors"
						onclick={refresh}
					>
						<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M16.023 9.348h4.992v-.001M2.985 19.644v-4.992m0 0h4.992m-4.993 0 3.181 3.183a8.25 8.25 0 0 0 13.803-3.7M4.031 9.865a8.25 8.25 0 0 1 13.803-3.7l3.181 3.182" />
						</svg>
						Refresh
					</button>
					{#if !isSystemTable}
						<button
							type="button"
							class="cursor-pointer inline-flex items-center gap-1.5 rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors"
							onclick={openAddColumnModal}
						>
							<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
							</svg>
							Add Column
						</button>
					{/if}
					<button
						type="button"
						class="cursor-pointer inline-flex items-center gap-1.5 rounded-lg bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors"
						onclick={openInsertModal}
					>
						<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
						</svg>
						{insertLabel}
					</button>
				</div>
			</div>

			<!-- Filter bar -->
			{#if showFilters}
				<div class="flex items-center gap-2 mb-4 rounded-lg border border-gray-200 bg-gray-50 p-3">
					<select
						bind:value={filterColumn}
						class="rounded-lg border border-gray-300 bg-white px-2 py-1.5 text-sm text-gray-700 focus:border-eurobase-500 focus:outline-none cursor-pointer"
					>
						<option value="">Column...</option>
						{#each selectedSchema.columns as col}
							<option value={col.name}>{col.name}</option>
						{/each}
					</select>
					<select
						bind:value={filterOperator}
						class="rounded-lg border border-gray-300 bg-white px-2 py-1.5 text-sm text-gray-700 focus:border-eurobase-500 focus:outline-none cursor-pointer"
					>
						{#each operators as op}
							<option value={op.value}>{op.label}</option>
						{/each}
					</select>
					<input
						type="text"
						bind:value={filterValue}
						placeholder="Value..."
						class="flex-1 rounded-lg border border-gray-300 px-2.5 py-1.5 text-sm text-gray-900 placeholder-gray-400 focus:border-eurobase-500 focus:outline-none"
					/>
					<button
						type="button"
						class="cursor-pointer rounded-lg bg-eurobase-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-eurobase-700 transition-colors"
						onclick={applyFilter}
					>
						Apply
					</button>
					<button
						type="button"
						class="cursor-pointer rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors"
						onclick={clearFilter}
					>
						Clear
					</button>
				</div>
			{/if}

			<!-- Error -->
			{#if dataError}
				<div class="mb-4 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-600">
					{dataError}
				</div>
			{/if}

			<!-- Selection bar -->
			{#if selectedIds.size > 0}
				<div class="mb-3 flex items-center gap-3 rounded-lg border border-eurobase-200 bg-eurobase-50 px-4 py-2">
					<span class="text-sm font-medium text-eurobase-700">
						{selectedIds.size} {selectedIds.size === 1 ? 'row' : 'rows'} selected
					</span>
					{#if !isSystemTable}
						<button
							type="button"
							class="cursor-pointer rounded-md border border-red-300 bg-white px-2.5 py-1 text-xs font-medium text-red-600 hover:bg-red-50 transition-colors"
							onclick={() => { showBulkDeleteConfirm = true; bulkDeleteError = null; }}
						>
							Delete Selected
						</button>
					{/if}
					<button
						type="button"
						class="cursor-pointer rounded-md border border-gray-300 bg-white px-2.5 py-1 text-xs font-medium text-gray-600 hover:bg-gray-50 transition-colors"
						onclick={() => { selectedIds = new Set(); }}
					>
						Clear
					</button>
				</div>
			{/if}

			<!-- Data grid -->
			<div class="flex-1 overflow-auto">
				<DataGrid
					columns={selectedSchema.columns}
					{rows}
					loading={dataLoading}
					bind:selectedIds
					onUpdateCell={async (rowId, column, value) => {
						if (!selectedTable) return;
						await api.updateRow(projectId, selectedTable, rowId, { [column]: value });
						loadTableData();
					}}
					onDeleteRow={isSystemTable ? undefined : (row) => {
						showDeleteConfirm = row;
					}}
					onEditColumn={isSystemTable ? undefined : (col) => {
						editColumnTarget = col;
						showColumnEditModal = true;
					}}
					onDropColumn={isSystemTable ? undefined : (col) => {
						showDropColumnConfirm = col;
						dropColumnError = null;
					}}
				/>
			</div>

			<!-- Index Panel -->
			{#if !isSystemTable && selectedTable && selectedSchema}
				<IndexPanel
					{projectId}
					tableName={selectedTable}
					columns={selectedSchema.columns}
					indexes={selectedSchema.indexes ?? []}
					onChanged={() => loadSchema()}
				/>
			{/if}

			<!-- Pagination -->
			<div class="flex items-center justify-between mt-4 pt-3 border-t border-gray-200">
				<div class="text-sm text-gray-500">
					{#if totalCount > 0}
						Showing {pageStart}–{pageEnd} of {totalCount}
					{:else}
						No rows
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
		{:else if !schemaLoading}
			<!-- Empty state -->
			<div class="flex-1 flex items-center justify-center">
				<div class="text-center">
					<svg class="mx-auto h-12 w-12 text-gray-300" fill="none" viewBox="0 0 24 24" stroke-width="1" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M20.25 6.375c0 2.278-3.694 4.125-8.25 4.125S3.75 8.653 3.75 6.375m16.5 0c0-2.278-3.694-4.125-8.25-4.125S3.75 4.097 3.75 6.375m16.5 0v11.25c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125V6.375m16.5 0v3.75m-16.5-3.75v3.75m16.5 0v3.75C20.25 16.153 16.556 18 12 18s-8.25-1.847-8.25-4.125v-3.75m16.5 0c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125" />
					</svg>
					<h3 class="mt-3 text-sm font-semibold text-gray-700">No tables yet</h3>
					<p class="mt-1 text-sm text-gray-400">Create your first table to get started.</p>
					<button
						type="button"
						class="cursor-pointer mt-4 inline-flex items-center gap-1.5 rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors"
						onclick={() => (showNewTableModal = true)}
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
						</svg>
						New Table
					</button>
				</div>
			</div>
		{/if}
	</div>
</div>

<!-- New Table Modal -->
<NewTableModal
	open={showNewTableModal}
	onClose={() => (showNewTableModal = false)}
	onCreate={handleTableCreated}
	{tables}
/>

<!-- Rename Table Modal -->
{#if renameTableTarget}
	<RenameTableModal
		open={showRenameTableModal}
		tableName={renameTableTarget}
		{projectId}
		onClose={() => { showRenameTableModal = false; renameTableTarget = null; }}
		onRenamed={async (newName) => {
			await loadSchema();
			selectTableAndLoad(newName);
		}}
	/>
{/if}

<!-- Column Edit Modal -->
{#if editColumnTarget && selectedTable}
	<ColumnEditModal
		open={showColumnEditModal}
		column={editColumnTarget}
		tableName={selectedTable}
		{projectId}
		{tables}
		onClose={() => { showColumnEditModal = false; editColumnTarget = null; }}
		onSaved={async () => {
			await loadSchema();
			if (selectedTable) loadTableData();
		}}
	/>
{/if}

<!-- Add Column Modal -->
{#if showAddColumnModal && selectedTable}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button
			type="button"
			class="fixed inset-0 bg-black/50 cursor-default"
			onclick={() => (showAddColumnModal = false)}
			tabindex="-1"
			aria-label="Close modal"
		></button>
		<div class="relative z-10 w-full max-w-md rounded-xl bg-white shadow-2xl">
			<div class="flex items-center justify-between border-b border-gray-200 px-6 py-4">
				<h2 class="text-lg font-semibold text-gray-900">Add Column — <span class="font-mono text-eurobase-600">{selectedTable}</span></h2>
				<button
					type="button"
					class="cursor-pointer rounded-lg p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600"
					onclick={() => (showAddColumnModal = false)}
					aria-label="Close"
				>
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" />
					</svg>
				</button>
			</div>
			<div class="px-6 py-5 space-y-4">
				{#if addColError}
					<div class="flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-4 py-3">
						<svg class="h-4 w-4 mt-0.5 shrink-0 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
						</svg>
						<p class="text-sm text-red-700">{addColError}</p>
					</div>
				{/if}
				<div>
					<label class="block text-sm font-medium text-gray-700 mb-1" for="addcol-name">Name</label>
					<input
						id="addcol-name"
						type="text"
						bind:value={addColName}
						placeholder="column_name"
						class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono text-gray-900 placeholder-gray-300 focus:border-eurobase-500 focus:ring-1 focus:ring-eurobase-500/20 focus:outline-none"
					/>
				</div>
				<div>
					<label class="block text-sm font-medium text-gray-700 mb-1" for="addcol-type">Type</label>
					<select
						id="addcol-type"
						bind:value={addColType}
						class="w-full rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm text-gray-900 focus:border-eurobase-500 focus:outline-none cursor-pointer"
					>
						{#each pgTypes as t}
							<option value={t}>{t}</option>
						{/each}
					</select>
				</div>
				<div class="flex items-center gap-6">
					<div class="flex items-center gap-3">
						<input
							id="addcol-nullable"
							type="checkbox"
							bind:checked={addColNullable}
							class="h-4 w-4 rounded border-gray-300 text-eurobase-600 focus:ring-eurobase-500 cursor-pointer"
						/>
						<label for="addcol-nullable" class="text-sm text-gray-700 cursor-pointer">Nullable</label>
					</div>
					<div class="flex items-center gap-3">
						<input
							id="addcol-unique"
							type="checkbox"
							bind:checked={addColUnique}
							class="h-4 w-4 rounded border-gray-300 text-teal-600 focus:ring-teal-500 cursor-pointer"
						/>
						<label for="addcol-unique" class="text-sm text-gray-700 cursor-pointer">Unique</label>
					</div>
				</div>
				<div>
					<label class="block text-sm font-medium text-gray-700 mb-1" for="addcol-default">Default value</label>
					<input
						id="addcol-default"
						type="text"
						bind:value={addColDefault}
						placeholder="e.g. now(), 0, 'hello'"
						class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono text-gray-900 placeholder-gray-300 focus:border-eurobase-500 focus:ring-1 focus:ring-eurobase-500/20 focus:outline-none"
					/>
				</div>
			</div>
			<div class="flex items-center justify-end gap-3 border-t border-gray-200 px-6 py-4">
				<button
					type="button"
					class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors"
					onclick={() => (showAddColumnModal = false)}
				>
					Cancel
				</button>
				<button
					type="button"
					class="cursor-pointer rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
					disabled={!addColName.trim() || addColSaving}
					onclick={handleAddColumn}
				>
					{addColSaving ? 'Adding...' : 'Add Column'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Drop Column Confirmation -->
{#if showDropColumnConfirm}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button
			type="button"
			class="fixed inset-0 bg-black/50 cursor-default"
			onclick={() => (showDropColumnConfirm = null)}
			tabindex="-1"
			aria-label="Close dialog"
		></button>
		<div class="relative z-10 w-full max-w-sm rounded-xl bg-white shadow-2xl p-6">
			<div class="flex items-center gap-3 mb-4">
				<div class="flex h-10 w-10 items-center justify-center rounded-full bg-red-100">
					<svg class="h-5 w-5 text-red-600" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
					</svg>
				</div>
				<div>
					<h3 class="text-sm font-semibold text-gray-900">Drop Column</h3>
					<p class="text-xs text-gray-500">This action cannot be undone.</p>
				</div>
			</div>
			{#if dropColumnError}
				<div class="mb-4 flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-3 py-2">
					<svg class="h-4 w-4 mt-0.5 shrink-0 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
					</svg>
					<p class="text-sm text-red-700">{dropColumnError}</p>
				</div>
			{/if}
			<p class="text-sm text-gray-600 mb-5">
				Are you sure you want to drop column
				<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono font-semibold">{showDropColumnConfirm.name}</code>
				from <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono font-semibold">{selectedTable}</code>?
				All data in this column will be permanently deleted.
			</p>
			<div class="flex justify-end gap-3">
				<button
					type="button"
					class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors"
					onclick={() => (showDropColumnConfirm = null)}
				>
					Cancel
				</button>
				<button
					type="button"
					class="cursor-pointer rounded-lg bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700 transition-colors"
					onclick={handleDropColumn}
				>
					Drop Column
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Bulk Delete Confirmation Dialog -->
{#if showBulkDeleteConfirm}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button
			type="button"
			class="fixed inset-0 bg-black/50 cursor-default"
			onclick={() => (showBulkDeleteConfirm = false)}
			tabindex="-1"
			aria-label="Close dialog"
		></button>
		<div class="relative z-10 w-full max-w-sm rounded-xl bg-white shadow-2xl p-6">
			<div class="flex items-center gap-3 mb-4">
				<div class="flex h-10 w-10 items-center justify-center rounded-full bg-red-100">
					<svg class="h-5 w-5 text-red-600" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
					</svg>
				</div>
				<div>
					<h3 class="text-sm font-semibold text-gray-900">Delete {selectedIds.size} {selectedIds.size === 1 ? 'Row' : 'Rows'}</h3>
					<p class="text-xs text-gray-500">This action cannot be undone.</p>
				</div>
			</div>
			{#if bulkDeleteError}
				<div class="mb-4 flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-3 py-2">
					<svg class="h-4 w-4 mt-0.5 shrink-0 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
					</svg>
					<p class="text-sm text-red-700">{bulkDeleteError}</p>
				</div>
			{/if}
			<p class="text-sm text-gray-600 mb-5">
				Are you sure you want to delete <strong>{selectedIds.size}</strong> selected {selectedIds.size === 1 ? 'row' : 'rows'}
				from <code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono font-semibold">{selectedTable}</code>?
			</p>
			<div class="flex justify-end gap-3">
				<button
					type="button"
					class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors"
					onclick={() => (showBulkDeleteConfirm = false)}
				>
					Cancel
				</button>
				<button
					type="button"
					class="cursor-pointer rounded-lg bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700 transition-colors"
					onclick={handleBulkDelete}
				>
					Delete {selectedIds.size} {selectedIds.size === 1 ? 'Row' : 'Rows'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Insert Row Modal -->
{#if showInsertModal && selectedSchema}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button
			type="button"
			class="fixed inset-0 bg-black/50 cursor-default"
			onclick={() => (showInsertModal = false)}
			tabindex="-1"
			aria-label="Close modal"
		></button>
		<div class="relative z-10 w-full max-w-lg max-h-[80vh] overflow-y-auto rounded-xl bg-white shadow-2xl">
			<div class="flex items-center justify-between border-b border-gray-200 px-6 py-4">
				<h2 class="text-lg font-semibold text-gray-900">{insertLabel} — {selectedTable}</h2>
				<button
					type="button"
					class="cursor-pointer rounded-lg p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600"
					onclick={() => (showInsertModal = false)}
					aria-label="Close"
				>
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" />
					</svg>
				</button>
			</div>
			<div class="px-6 py-5 space-y-4">
				{#if insertError}
					<div class="flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-4 py-3">
						<svg class="h-4 w-4 mt-0.5 shrink-0 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
						</svg>
						<p class="text-sm text-red-700">{insertError}</p>
					</div>
				{/if}
				{#each selectedSchema.columns.filter(isEditableColumn) as col}
					<div>
						<div class="block text-sm font-medium text-gray-700 mb-1">
							<span class="font-mono">{col.name}</span>
							<span class="ml-1.5 text-xs text-gray-400 font-normal">{col.data_type}</span>
						</div>
						{#if col.data_type === 'boolean'}
							<select
								class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 focus:border-eurobase-500 focus:outline-none cursor-pointer"
								onchange={(e) => {
									const target = e.target as HTMLSelectElement;
									insertFormData[col.name] = target.value;
								}}
							>
								<option value="">-- select --</option>
								<option value="true">true</option>
								<option value="false">false</option>
							</select>
						{:else}
							<input
								type="text"
								value={insertFormData[col.name] ?? ''}
								oninput={(e) => {
									const target = e.target as HTMLInputElement;
									insertFormData[col.name] = target.value;
								}}
								placeholder={col.default_value ? `Default: ${col.default_value}` : col.is_nullable ? 'null' : 'Required'}
								class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-900 placeholder-gray-300 focus:border-eurobase-500 focus:ring-1 focus:ring-eurobase-500/20 focus:outline-none font-mono"
							/>
						{/if}
					</div>
				{/each}
			</div>
			<div class="flex items-center justify-end gap-3 border-t border-gray-200 px-6 py-4">
				<button
					type="button"
					class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors"
					onclick={() => (showInsertModal = false)}
				>
					Cancel
				</button>
				<button
					type="button"
					class="cursor-pointer rounded-lg bg-eurobase-600 px-4 py-2 text-sm font-medium text-white hover:bg-eurobase-700 transition-colors"
					onclick={handleInsertRow}
				>
					Insert
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Delete Confirmation Dialog -->
{#if showDeleteConfirm}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button
			type="button"
			class="fixed inset-0 bg-black/50 cursor-default"
			onclick={() => (showDeleteConfirm = null)}
			tabindex="-1"
			aria-label="Close dialog"
		></button>
		<div class="relative z-10 w-full max-w-sm rounded-xl bg-white shadow-2xl p-6">
			<div class="flex items-center gap-3 mb-4">
				<div class="flex h-10 w-10 items-center justify-center rounded-full bg-red-100">
					<svg class="h-5 w-5 text-red-600" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
					</svg>
				</div>
				<div>
					<h3 class="text-sm font-semibold text-gray-900">Delete Row</h3>
					<p class="text-xs text-gray-500">This action cannot be undone.</p>
				</div>
			</div>
			<p class="text-sm text-gray-600 mb-5">
				Are you sure you want to delete this row
				{#if showDeleteConfirm?.id}
					<code class="rounded bg-gray-100 px-1 py-0.5 text-xs font-mono">{String(showDeleteConfirm.id).substring(0, 8)}...</code>
				{/if}?
			</p>
			<div class="flex justify-end gap-3">
				<button
					type="button"
					class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors"
					onclick={() => (showDeleteConfirm = null)}
				>
					Cancel
				</button>
				<button
					type="button"
					class="cursor-pointer rounded-lg bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700 transition-colors"
					onclick={handleDeleteRow}
				>
					Delete
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Drop Table Confirmation Dialog -->
{#if showDropTableConfirm}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<button
			type="button"
			class="fixed inset-0 bg-black/50 cursor-default"
			onclick={() => (showDropTableConfirm = null)}
			tabindex="-1"
			aria-label="Close dialog"
		></button>
		<div class="relative z-10 w-full max-w-sm rounded-xl bg-white shadow-2xl p-6">
			<div class="flex items-center gap-3 mb-4">
				<div class="flex h-10 w-10 items-center justify-center rounded-full bg-red-100">
					<svg class="h-5 w-5 text-red-600" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z" />
					</svg>
				</div>
				<div>
					<h3 class="text-sm font-semibold text-gray-900">Drop Table</h3>
					<p class="text-xs text-gray-500">This action cannot be undone.</p>
				</div>
			</div>
			{#if dropTableError}
				<div class="mb-4 flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-3 py-2">
					<svg class="h-4 w-4 mt-0.5 shrink-0 text-red-500" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
					</svg>
					<p class="text-sm text-red-700">{dropTableError}</p>
				</div>
			{/if}
			<p class="text-sm text-gray-600 mb-5">
				Are you sure you want to drop
				<code class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono font-semibold">{showDropTableConfirm}</code>?
				All data in this table will be permanently deleted.
			</p>
			<div class="flex justify-end gap-3">
				<button
					type="button"
					class="cursor-pointer rounded-lg border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors"
					onclick={() => (showDropTableConfirm = null)}
				>
					Cancel
				</button>
				<button
					type="button"
					class="cursor-pointer rounded-lg bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700 transition-colors"
					onclick={handleDropTable}
				>
					Drop Table
				</button>
			</div>
		</div>
	</div>
{/if}
